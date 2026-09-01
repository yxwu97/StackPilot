package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	"stackpilot/internal/api"
	"stackpilot/internal/buildinfo"
	"stackpilot/internal/capability"
	"stackpilot/internal/changeplan"
	"stackpilot/internal/domain"
	composedriver "stackpilot/internal/driver/compose"
	processdriver "stackpilot/internal/driver/process"
	"stackpilot/internal/events"
	"stackpilot/internal/health"
	"stackpilot/internal/importer"
	"stackpilot/internal/incident"
	"stackpilot/internal/logs"
	"stackpilot/internal/manifest"
	"stackpilot/internal/metrics"
	"stackpilot/internal/orchestrator"
	"stackpilot/internal/platform"
	"stackpilot/internal/ports"
	"stackpilot/internal/revision"
	"stackpilot/internal/runner"
	"stackpilot/internal/security"
	"stackpilot/internal/storage"
	"stackpilot/internal/workspace"
)

const (
	defaultServerPort             = 32100
	shutdownTimeout               = 15 * time.Second
	defaultReconcileInterval      = 10 * time.Second
	defaultLeaseReconcileInterval = 30 * time.Second
)

type serverConfig struct {
	port                   int
	dataDir                string
	reconcileInterval      time.Duration
	leaseReconcileInterval time.Duration
}

type controlPlane struct {
	database         *sql.DB
	singleService    *orchestrator.SingleService
	workspaceImports *workspace.ImportService
	metricSampler    *metrics.Sampler
	handler          http.Handler
}

type streamRuntime struct {
	audit       *storage.AuditRepository
	events      *storage.EventRepository
	eventBroker *events.Broker
	logManager  *logs.Manager
	logScopes   *storage.RuntimeLogScopeRepository
	logBroker   *logs.Broker
	incidents   *storage.IncidentRepository
}

type orchestrationDependencies struct {
	operations      *orchestrator.Manager
	runtime         *storage.RuntimeInstanceRepository
	runner          *runner.Resolver
	driver          *processdriver.Driver
	compose         *composedriver.Lifecycle
	overrides       *composedriver.OverrideGenerator
	readiness       *health.Engine
	portPlanner     *ports.Planner
	portLeases      *storage.PortLeaseRepository
	resolvedSpecs   *storage.ResolvedSpecRepository
	secretVersions  *storage.ServiceSecretVersionRepository
	restartAttempts *storage.RestartAttemptRepository
	healthResults   *storage.HealthResultRepository
	incidents       *incident.Coordinator
}

func runServer(ctx context.Context, args []string, stdout, stderr io.Writer) (exitCode int) {
	config, err := parseServerConfig(args, stderr)
	if err != nil {
		return 2
	}
	logger := newLogger(stderr)
	runtimeContext, cancel := context.WithCancel(ctx)
	plane, err := newControlPlane(runtimeContext, config, logger)
	if err != nil {
		cancel()
		logger.Error("control plane initialization failed", "reason", "startup_error", "error_code", "CONTROL_PLANE_INITIALIZATION_FAILED", "error", err)
		return 1
	}
	defer shutdownControlPlane(cancel, plane, logger, &exitCode)
	if err := plane.singleService.Reconcile(runtimeContext); err != nil {
		logger.Error("startup reconciliation failed", "reason", "startup_error", "error_code", "STARTUP_RECONCILIATION_FAILED", "error", err)
		return 1
	}
	if err := plane.singleService.StartPeriodicReconciliation(config.reconcileInterval, config.leaseReconcileInterval); err != nil {
		logger.Error("periodic reconciliation initialization failed", "reason", "startup_error", "error_code", "PERIODIC_RECONCILIATION_START_FAILED", "error", err)
		return 1
	}
	if plane.metricSampler != nil {
		if err := plane.metricSampler.Start(runtimeContext); err != nil {
			logger.Error("metric sampler initialization failed", "reason", "startup_error", "error_code", "METRIC_SAMPLER_START_FAILED", "error", err)
			return 1
		}
	}
	return serveControlPlane(runtimeContext, config.port, plane.handler, stdout, logger)
}

func shutdownControlPlane(cancel context.CancelFunc, plane *controlPlane, logger *slog.Logger, exitCode *int) {
	cancel()
	if plane.metricSampler != nil {
		plane.metricSampler.Wait()
	}
	plane.workspaceImports.Wait()
	plane.singleService.Wait()
	if err := plane.database.Close(); err != nil {
		logger.Error("control database close failed", "error", err)
		*exitCode = 1
	}
}

func newControlPlane(ctx context.Context, config serverConfig, logger *slog.Logger) (*controlPlane, error) {
	database, err := storage.OpenDataDir(ctx, config.dataDir)
	if err != nil {
		return nil, fmt.Errorf("initialize control database: %w", err)
	}
	plane, err := assembleControlPlane(ctx, database, config.dataDir, logger)
	if err != nil {
		return nil, errors.Join(err, database.Close())
	}
	return plane, nil
}

func assembleControlPlane(ctx context.Context, database *sql.DB, dataDir string, logger *slog.Logger) (*controlPlane, error) {
	if err := storage.CheckWritable(ctx, database); err != nil {
		return nil, fmt.Errorf("check control database readiness: %w", err)
	}
	workspaces, err := newWorkspaceManager(database)
	if err != nil {
		return nil, fmt.Errorf("initialize workspace catalog: %w", err)
	}
	auth, err := newAuthManager(ctx, database, dataDir)
	if err != nil {
		return nil, fmt.Errorf("initialize local authentication: %w", err)
	}
	secrets, err := newSecretProvider(database, dataDir)
	if err != nil {
		return nil, fmt.Errorf("initialize Secret provider: %w", err)
	}
	streams, err := newStreamRuntime(database, dataDir, logger)
	if err != nil {
		return nil, err
	}
	service, dependencies, err := newSingleService(ctx, database, workspaces, secrets, streams.eventBroker, streams.logManager, dataDir, logger)
	if err != nil {
		return nil, fmt.Errorf("initialize single-service orchestration: %w", err)
	}
	var metricSampler *metrics.Sampler
	var metricQueries *metrics.QueryService
	if capability.PublishedName(capability.Phase3ResourceMonitoring) {
		metricRepository, repositoryErr := storage.NewMetricRepository(database)
		if repositoryErr != nil {
			return nil, fmt.Errorf("initialize resource metric repository: %w", repositoryErr)
		}
		metricSampler, err = newMetricSampler(metricRepository, dependencies, logger)
		if err != nil {
			return nil, fmt.Errorf("initialize resource sampler: %w", err)
		}
		metricQueries, err = metrics.NewQueryService(dependencies.runtime, metricRepository)
		if err != nil {
			return nil, fmt.Errorf("initialize resource metric queries: %w", err)
		}
	}
	analyzer, err := importer.NewAnalyzer()
	if err != nil {
		return nil, fmt.Errorf("initialize workspace importer: %w", err)
	}
	importRepository, err := storage.NewWorkspaceImportRepository(database)
	if err != nil {
		return nil, fmt.Errorf("initialize workspace import repository: %w", err)
	}
	workspaceImports, err := workspace.NewImportService(workspace.ImportServiceConfig{
		Context: ctx, Analyzer: analyzer, Repository: importRepository, Workspaces: workspaces, Logger: logger,
		CanEdit: func(checkContext context.Context, id domain.WorkspaceID) error {
			return canEditWorkspace(checkContext, service, id)
		},
	})
	if err != nil {
		return nil, fmt.Errorf("initialize workspace import service: %w", err)
	}
	handler, err := newAPIHandler(logger, workspaces, workspaceImports, auth, secrets, streams, service, metricQueries)
	if err != nil {
		return nil, fmt.Errorf("initialize web console: %w", err)
	}
	return &controlPlane{database: database, singleService: service, workspaceImports: workspaceImports, metricSampler: metricSampler, handler: handler}, nil
}

func canEditWorkspace(ctx context.Context, service *orchestrator.SingleService, id domain.WorkspaceID) error {
	status, err := service.Status(ctx, id)
	if err != nil {
		return err
	}
	if status.System != nil && status.System.State != domain.SystemStopped {
		return workspace.ErrEditRuntimeActive
	}
	operations, err := service.ListOperations(ctx, &id, 50)
	if err != nil {
		return err
	}
	for _, operation := range operations {
		if !operation.State.Terminal() {
			return workspace.ErrEditRuntimeActive
		}
	}
	return nil
}

func newStreamRuntime(database *sql.DB, dataDir string, logger *slog.Logger) (*streamRuntime, error) {
	audit, err := storage.NewAuditRepository(database)
	if err != nil {
		return nil, fmt.Errorf("initialize audit repository: %w", err)
	}
	eventBroker := events.NewBroker(32)
	eventRepository, err := storage.NewEventRepository(database, eventBroker)
	if err != nil {
		return nil, fmt.Errorf("initialize event repository: %w", err)
	}
	logBroker := logs.NewBroker(256, 5000)
	logIndex, err := storage.NewLogSegmentRepository(database)
	if err != nil {
		return nil, fmt.Errorf("initialize log segment repository: %w", err)
	}
	logManager, err := logs.NewManager(logs.Config{DataDir: dataDir, Index: logIndex, Publisher: logBroker, Logger: logger})
	if err != nil {
		return nil, fmt.Errorf("initialize log manager: %w", err)
	}
	logScopes, err := storage.NewRuntimeLogScopeRepository(database)
	if err != nil {
		return nil, fmt.Errorf("initialize log scope repository: %w", err)
	}
	incidentRepository, err := storage.NewIncidentRepository(database)
	if err != nil {
		return nil, fmt.Errorf("initialize incident repository: %w", err)
	}
	return &streamRuntime{audit: audit, events: eventRepository, eventBroker: eventBroker, logManager: logManager, logScopes: logScopes, logBroker: logBroker, incidents: incidentRepository}, nil
}

func newAPIHandler(logger *slog.Logger, workspaces *workspace.Manager, workspaceImports *workspace.ImportService, auth *security.AuthManager, secrets security.SecretProvider, streams *streamRuntime, service *orchestrator.SingleService, metricQueries *metrics.QueryService) (http.Handler, error) {
	return api.NewHandler(api.Config{
		BuildInfo: buildinfo.Current(), Capabilities: capability.Published(), Logger: logger, Readiness: func(context.Context) bool { return true },
		Workspaces: workspaces, WorkspaceImports: workspaceImports, EventStore: streams.events, EventBroker: streams.eventBroker,
		LogManager: streams.logManager, LogScopes: streams.logScopes, LogBroker: streams.logBroker,
		SingleService: service, Auth: auth, Audit: streams.audit, Secrets: secrets, Incidents: streams.incidents,
		MetricQueries: metricQueries,
	})
}

func newSecretProvider(database *sql.DB, dataDir string) (security.SecretProvider, error) {
	metadata, err := storage.NewSecretMetadataRepository(database)
	if err != nil {
		return nil, err
	}
	return security.NewOSSecretProvider(dataDir, metadata, time.Now)
}

func serveControlPlane(ctx context.Context, port int, handler http.Handler, stdout io.Writer, logger *slog.Logger) int {
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	listener, err := net.Listen("tcp", address)
	if err != nil {
		logger.Error("http listener creation failed", "reason", "startup_error", "error_code", "HTTP_LISTENER_CREATE_FAILED", "address", address, "error", err)
		return 1
	}
	server := newHTTPServer(address, handler)
	logger.Info("http server started", "address", address)
	fmt.Fprintf(stdout, "StackPilot is available at http://%s\n", address)
	serveErrors := make(chan error, 1)
	go func() { serveErrors <- server.Serve(listener) }()
	return waitForServer(ctx, server, serveErrors, logger)
}

func newAuthManager(ctx context.Context, database *sql.DB, dataDir string) (*security.AuthManager, error) {
	repository, err := storage.NewAuthTokenRepository(database)
	if err != nil {
		return nil, err
	}
	store, err := security.NewOSTokenStore(dataDir)
	if err != nil {
		return nil, err
	}
	manager, err := security.NewAuthManager(security.AuthConfig{Repository: repository, Store: store})
	if err != nil {
		return nil, err
	}
	if err := manager.Initialize(ctx); err != nil {
		return nil, err
	}
	return manager, nil
}

func newSingleService(ctx context.Context, database *sql.DB, workspaces *workspace.Manager, secrets security.SecretProvider, eventBroker *events.Broker, logManager *logs.Manager, dataDir string, logger *slog.Logger) (*orchestrator.SingleService, *orchestrationDependencies, error) {
	dependencies, err := newOrchestrationDependencies(database, eventBroker, dataDir)
	if err != nil {
		return nil, nil, err
	}
	revisions, changePlans, err := newChangePlanDependencies(database, workspaces, dependencies)
	if err != nil {
		return nil, nil, err
	}
	canonicalDataDir, err := filepath.EvalSymlinks(dataDir)
	if err != nil {
		return nil, nil, fmt.Errorf("canonicalize runtime data directory: %w", err)
	}
	logCheckpoints, err := storage.NewLogSegmentRepository(database)
	if err != nil {
		return nil, nil, err
	}
	service, err := orchestrator.NewSingleService(orchestrator.SingleServiceConfig{
		Context: ctx, Operations: dependencies.operations, Workspaces: workspaces, Runner: dependencies.runner,
		Driver: dependencies.driver, Compose: dependencies.compose, Overrides: dependencies.overrides,
		Runtime: dependencies.runtime, Readiness: dependencies.readiness, Liveness: dependencies.readiness,
		DataDir: canonicalDataDir, Logger: logger, PortPlanner: dependencies.portPlanner,
		PortLeases: dependencies.portLeases, ResolvedSpecs: dependencies.resolvedSpecs,
		Secrets: secrets, SecretVersions: dependencies.secretVersions,
		RestartAttempts:  dependencies.restartAttempts,
		HealthRetention:  dependencies.healthResults,
		HealthResults:    dependencies.healthResults,
		Incidents:        dependencies.incidents,
		IncidentAnalyzer: dependencies.incidents,
		IncidentLogs:     logManager,
		Revisions:        revisions,
		ChangePlans:      changePlans,
		LogCheckpoints:   logCheckpoints,
		StartLogs: func(captureContext context.Context, request logs.CaptureRequest) (orchestrator.CaptureSession, error) {
			return logManager.Start(captureContext, request)
		},
		ResumeLogs: func(captureContext context.Context, request logs.CaptureRequest) (orchestrator.CaptureSession, error) {
			return logManager.Resume(captureContext, request)
		},
	})
	if err != nil {
		return nil, nil, err
	}
	return service, dependencies, nil
}

func newChangePlanDependencies(database *sql.DB, workspaces *workspace.Manager, dependencies *orchestrationDependencies) (*revision.Service, *changeplan.Service, error) {
	gitProbe, err := revision.NewGitProbe("")
	if err != nil {
		return nil, nil, err
	}
	collector, err := revision.NewCollector(revision.CollectorConfig{
		Workspaces: workspaces, Runtime: dependencies.runtime, ResolvedSpecs: dependencies.resolvedSpecs,
		SecretVersions: dependencies.secretVersions, Runners: dependencies.runner, Git: gitProbe,
	})
	if err != nil {
		return nil, nil, err
	}
	revisionRepository, err := storage.NewRevisionRepository(database)
	if err != nil {
		return nil, nil, err
	}
	revisions, err := revision.NewService(collector, revisionRepository)
	if err != nil {
		return nil, nil, err
	}
	planRepository, err := storage.NewChangePlanRepository(database)
	if err != nil {
		return nil, nil, err
	}
	plans, err := changeplan.NewService(planRepository, revisions)
	return revisions, plans, err
}

func newMetricSampler(repository *storage.MetricRepository, dependencies *orchestrationDependencies, logger *slog.Logger) (*metrics.Sampler, error) {
	processSource, err := metrics.NewProcessSource(dependencies.driver, runtime.NumCPU())
	if err != nil {
		return nil, err
	}
	composeSource, err := metrics.NewComposeSource(dependencies.compose)
	if err != nil {
		return nil, err
	}
	return metrics.NewSampler(metrics.SamplerConfig{
		Runtime: dependencies.runtime, Store: repository, Retention: repository,
		Process: processSource, Compose: composeSource, Logger: logger,
	})
}

func newOrchestrationDependencies(database *sql.DB, eventBroker *events.Broker, dataDir string) (*orchestrationDependencies, error) {
	operationRepository, err := storage.NewOperationRepositoryWithNotifier(database, eventBroker)
	if err != nil {
		return nil, err
	}
	operationManager, err := orchestrator.NewManager(operationRepository)
	if err != nil {
		return nil, err
	}
	runtimeRepository, err := storage.NewRuntimeInstanceRepository(database, eventBroker)
	if err != nil {
		return nil, err
	}
	resolvedRunner, err := runner.NewResolver(runner.Config{})
	if err != nil {
		return nil, err
	}
	process := processdriver.New(processdriver.Config{})
	composeLifecycle, err := composedriver.NewLifecycle(composedriver.LifecycleConfig{})
	if err != nil {
		return nil, err
	}
	overrides, err := composedriver.NewOverrideGenerator(dataDir)
	if err != nil {
		return nil, err
	}
	readiness, healthResults, err := newReadinessEngine(database, process, composeLifecycle)
	if err != nil {
		return nil, err
	}
	portPlanner, portLeases, resolvedSpecs, err := newPortDependencies(database)
	if err != nil {
		return nil, err
	}
	secretVersions, err := storage.NewServiceSecretVersionRepository(database)
	if err != nil {
		return nil, err
	}
	restartAttempts, err := storage.NewRestartAttemptRepository(database)
	if err != nil {
		return nil, err
	}
	incidentRepository, err := storage.NewIncidentRepository(database)
	if err != nil {
		return nil, err
	}
	incidentCoordinator, err := incident.NewCoordinator(incidentRepository, nil)
	if err != nil {
		return nil, err
	}
	return &orchestrationDependencies{
		operations: operationManager, runtime: runtimeRepository, runner: resolvedRunner, driver: process,
		compose: composeLifecycle, overrides: overrides,
		readiness: readiness, portPlanner: portPlanner, portLeases: portLeases, resolvedSpecs: resolvedSpecs,
		secretVersions: secretVersions, restartAttempts: restartAttempts, healthResults: healthResults, incidents: incidentCoordinator,
	}, nil
}

func newReadinessEngine(database *sql.DB, process *processdriver.Driver, composeLifecycle *composedriver.Lifecycle) (*health.Engine, *storage.HealthResultRepository, error) {
	healthRepository, err := storage.NewHealthResultRepository(database)
	if err != nil {
		return nil, nil, err
	}
	redactor, err := logs.NewDefaultRedactor(nil)
	if err != nil {
		return nil, nil, err
	}
	engine, err := health.NewEngine(health.NewCheckerWithCompose(process, composeLifecycle, redactor), healthRepository, nil)
	return engine, healthRepository, err
}

func newPortDependencies(database *sql.DB) (*ports.Planner, *storage.PortLeaseRepository, *storage.ResolvedSpecRepository, error) {
	portLeases, err := storage.NewPortLeaseRepository(database)
	if err != nil {
		return nil, nil, nil, err
	}
	portPlanner, err := ports.NewPlanner(ports.Config{Store: portLeases})
	if err != nil {
		return nil, nil, nil, err
	}
	resolvedSpecs, err := storage.NewResolvedSpecRepository(database)
	if err != nil {
		return nil, nil, nil, err
	}
	return portPlanner, portLeases, resolvedSpecs, nil
}

func newWorkspaceManager(database *sql.DB) (*workspace.Manager, error) {
	loader, err := manifest.NewLoader()
	if err != nil {
		return nil, err
	}
	repository, err := storage.NewWorkspaceRepository(database)
	if err != nil {
		return nil, err
	}
	return workspace.NewManager(repository, loader, manifest.NewValidatorWithCapabilities(capability.PublishedManifestAliases()...))
}

func newLogger(output io.Writer) *slog.Logger {
	options := &slog.HandlerOptions{
		ReplaceAttr: func(_ []string, attribute slog.Attr) slog.Attr {
			if attribute.Key == slog.TimeKey {
				attribute.Value = slog.TimeValue(attribute.Value.Time().UTC())
			}
			return attribute
		},
	}
	return slog.New(slog.NewJSONHandler(output, options))
}

func parseServerConfig(args []string, output io.Writer) (serverConfig, error) {
	config := serverConfig{
		port: defaultServerPort, reconcileInterval: defaultReconcileInterval,
		leaseReconcileInterval: defaultLeaseReconcileInterval,
	}
	flags := flag.NewFlagSet("server", flag.ContinueOnError)
	flags.SetOutput(output)
	flags.IntVar(&config.port, "port", defaultServerPort, "loopback HTTP port")
	flags.StringVar(&config.dataDir, "data-dir", "", "control-plane data directory")
	flags.DurationVar(&config.reconcileInterval, "reconcile-interval", defaultReconcileInterval, "managed process reconciliation interval")
	flags.DurationVar(&config.leaseReconcileInterval, "lease-reconcile-interval", defaultLeaseReconcileInterval, "port lease reconciliation interval")
	if err := flags.Parse(args); err != nil {
		return serverConfig{}, err
	}
	if flags.NArg() != 0 {
		return serverConfig{}, fmt.Errorf("server does not accept positional arguments")
	}
	if config.port < 1 || config.port > 65535 {
		return serverConfig{}, fmt.Errorf("port must be between 1 and 65535")
	}
	if config.reconcileInterval < defaultReconcileInterval || config.leaseReconcileInterval < defaultLeaseReconcileInterval {
		return serverConfig{}, fmt.Errorf("reconciliation intervals must be at least 10s and 30s")
	}
	if config.dataDir == "" {
		defaultDataDir, err := platform.DefaultDataDir()
		if err != nil {
			return serverConfig{}, fmt.Errorf("resolve default data directory: %w", err)
		}
		config.dataDir = defaultDataDir
	}
	absoluteDataDir, err := filepath.Abs(config.dataDir)
	if err != nil {
		return serverConfig{}, fmt.Errorf("resolve data directory: %w", err)
	}
	config.dataDir = absoluteDataDir
	return config, nil
}

func newHTTPServer(address string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              address,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      0,
		IdleTimeout:       60 * time.Second,
	}
}

func waitForServer(ctx context.Context, server *http.Server, serveErrors <-chan error, logger *slog.Logger) int {
	select {
	case err := <-serveErrors:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server stopped", "reason", "serve_error", "error_code", "HTTP_SERVE_FAILED", "error", err)
			return 1
		}
		return 0
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
			logger.Error("http server shutdown failed", "reason", "context_cancelled", "error_code", "HTTP_SHUTDOWN_FAILED", "error", err)
			return 1
		}
		if err := <-serveErrors; err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server stopped during shutdown", "reason", "serve_error", "error_code", "HTTP_SERVE_FAILED", "error", err)
			return 1
		}
		logger.Info("http server stopped", "reason", "context_cancelled", "exit_code", 0)
		return 0
	}
}
