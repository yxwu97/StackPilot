package orchestrator

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"stackpilot/internal/changeplan"
	"stackpilot/internal/domain"
	"stackpilot/internal/driver"
	"stackpilot/internal/driver/compose"
	"stackpilot/internal/health"
	"stackpilot/internal/incident"
	"stackpilot/internal/logs"
	"stackpilot/internal/manifest"
	"stackpilot/internal/ports"
	"stackpilot/internal/revision"
	"stackpilot/internal/security"
	"stackpilot/internal/workspace"
)

const workerFinalizationTimeout = 10 * time.Second

const (
	defaultVerificationStableWindow = 30 * time.Second
	defaultVerificationTimeout      = 60 * time.Second
	defaultVerificationPollInterval = time.Second
)

var errUserCancellation = errors.New("operation cancellation requested by user")

type runtimeRepository interface {
	Create(context.Context, domain.OperationID, domain.SystemInstance, domain.ServiceInstance) error
	CreateSystem(context.Context, domain.OperationID, domain.SystemInstance, []domain.ServiceInstance) error
	GetActive(context.Context, domain.WorkspaceID) (*domain.SystemInstance, bool, error)
	GetService(context.Context, domain.ServiceInstanceID) (*domain.ServiceInstance, bool, error)
	ListServices(context.Context, domain.SystemInstanceID) ([]domain.ServiceInstance, error)
	AttachIdentity(context.Context, domain.OperationID, domain.ServiceInstanceID, int64, domain.ProcessIdentity, time.Time) (*domain.ServiceInstance, error)
	AttachComposeIdentity(context.Context, domain.OperationID, domain.ServiceInstanceID, int64, string, time.Time) (*domain.ServiceInstance, error)
	AttachComposeStopIdentity(context.Context, domain.OperationID, domain.ServiceInstanceID, int64, string, time.Time) (*domain.ServiceInstance, error)
	TransitionService(context.Context, domain.OperationID, domain.ServiceInstanceID, int64, domain.ServiceState, string, *uint32, time.Time) (*domain.ServiceInstance, error)
	TransitionSystem(context.Context, domain.OperationID, domain.SystemInstanceID, domain.SystemState, time.Time) (*domain.SystemInstance, error)
}

type portLeaseManager interface {
	ports.ReservationStore
	LoadPreferences(context.Context, domain.WorkspaceID) (ports.Preferences, error)
	MarkBound(context.Context, domain.PortLeaseID, domain.SystemInstanceID, time.Time) error
	Release(context.Context, domain.PortLeaseID, time.Time) error
	RecordSuccessfulPlan(context.Context, domain.PortPlanID, time.Time) error
}

type resolvedSpecStore interface {
	SaveResolvedSpec(context.Context, string, domain.WorkspaceID, string, []byte, time.Time) error
	LoadResolvedSpec(context.Context, string) ([]byte, error)
}

type readinessWaiter interface {
	Await(context.Context, health.Request) (health.Outcome, error)
}

type livenessMonitor interface {
	MonitorLiveness(context.Context, health.LivenessRequest, health.LivenessHandler) error
}

// CaptureSession is the owned log capture lifecycle needed by orchestration.
type CaptureSession interface {
	Close() error
}

// LogStartFunc starts capture after the Supervisor has created both spool files.
type LogStartFunc func(context.Context, logs.CaptureRequest) (CaptureSession, error)

type composeLifecycle interface {
	Preflight(context.Context, compose.PreflightRequest) (*compose.PreflightResult, error)
	Start(context.Context, compose.LifecycleRequest) (compose.ProjectIdentity, error)
	StartWithoutBuild(context.Context, compose.LifecycleRequest) (compose.ProjectIdentity, error)
	Prepare(context.Context, compose.LifecycleRequest) (compose.ProjectIdentity, error)
	Build(context.Context, compose.ProjectIdentity) error
	Up(context.Context, compose.ProjectIdentity) error
	Stop(context.Context, compose.ProjectIdentity) error
	Inspect(context.Context, compose.ProjectIdentity) (compose.ProjectObservation, error)
	Recover(context.Context, string) (compose.ProjectIdentity, compose.ProjectObservation, error)
	Discover(context.Context, compose.LifecycleRequest) (compose.ProjectIdentity, compose.ProjectObservation, error)
	FollowLogs(context.Context, compose.LogFollowRequest) (*compose.LogSession, error)
}

type reconciliationRuntimeRepository interface {
	ListActive(context.Context) ([]domain.SystemInstance, error)
	MarkReconciled(context.Context, domain.SystemInstanceID, time.Time) error
}

type runtimeStateConflictClassifier interface {
	IsRuntimeStateConflict(error) bool
}

type secretVersionStore interface {
	RecordServiceSecretVersions(context.Context, domain.ServiceInstanceID, []security.ServiceSecretVersion) error
	ListServiceSecretVersions(context.Context, domain.ServiceInstanceID) ([]security.ServiceSecretVersion, error)
}

type logCheckpointStore interface {
	LastTimestamp(context.Context, domain.ServiceInstanceID) (time.Time, bool, error)
}

type restartAttemptStore interface {
	Claim(context.Context, domain.ServiceInstanceID, time.Time, time.Duration, int) (int, bool, error)
	ReleaseClaim(context.Context, domain.ServiceInstanceID, int) error
	MarkReady(context.Context, domain.ServiceInstanceID, time.Time) error
}

type healthCompactor interface {
	CompactDefault(context.Context, time.Time) (int64, error)
}

type healthResultReader interface {
	ListRecentByPurpose(context.Context, domain.ServiceInstanceID, health.Purpose, int) ([]health.Result, error)
}

type incidentReporter interface {
	Report(context.Context, incident.ReportInput) (*incident.Record, []incident.RuleResult, error)
}

type incidentAnalyzer interface {
	Get(context.Context, domain.IncidentID) (*incident.Record, error)
	Reanalyze(context.Context, domain.IncidentID, incident.Context, time.Time) ([]incident.RuleResult, error)
}

type incidentLogReader interface {
	QueryWindow(context.Context, logs.WindowQuery) (logs.Window, error)
	Redact(string) (string, error)
}

type livenessRegistration struct {
	cancel  context.CancelFunc
	spec    health.ResolvedSpec
	restart ResolvedRestartPolicy
}

// SingleServiceConfig wires the Phase 1B vertical slice without exposing adapters to HTTP.
type SingleServiceConfig struct {
	Context                  context.Context
	Operations               *Manager
	Workspaces               *workspace.Manager
	Runner                   runnerResolver
	Driver                   driver.Driver
	Compose                  composeLifecycle
	Overrides                *compose.OverrideGenerator
	Runtime                  runtimeRepository
	Readiness                readinessWaiter
	Liveness                 livenessMonitor
	StartLogs                LogStartFunc
	ResumeLogs               LogStartFunc
	LogCheckpoints           logCheckpointStore
	PortPlanner              *ports.Planner
	PortLeases               portLeaseManager
	ResolvedSpecs            resolvedSpecStore
	Secrets                  security.SecretProvider
	SecretVersions           secretVersionStore
	RestartAttempts          restartAttemptStore
	HealthRetention          healthCompactor
	HealthResults            healthResultReader
	Incidents                incidentReporter
	IncidentAnalyzer         incidentAnalyzer
	IncidentLogs             incidentLogReader
	Revisions                *revision.Service
	ChangePlans              *changeplan.Service
	VerificationStableWindow time.Duration
	VerificationTimeout      time.Duration
	VerificationPollInterval time.Duration
	DataDir                  string
	Logger                   *slog.Logger
}

// StartSingleServiceInput is the trusted application command built by the API boundary.
type StartSingleServiceInput struct {
	WorkspaceID        domain.WorkspaceID
	SystemID           domain.SystemID
	PortOverrides      map[string]int
	IdempotencySubject string
	IdempotencyKey     string
	Request            []byte
	FailurePolicy      FailurePolicyOverride
}

// FailurePolicyOverride contains only the safe policy values accepted by the start API.
type FailurePolicyOverride struct {
	FailFast          *bool
	CleanupOnFailure  *bool
	KeepReadyServices *bool
}

// StopSingleServiceInput is the trusted stop application command.
type StopSingleServiceInput struct {
	WorkspaceID        domain.WorkspaceID
	SystemID           domain.SystemID
	IdempotencySubject string
	IdempotencyKey     string
	Request            []byte
}

// RestartSystemInput contains safe fresh-start overrides for one system restart.
type RestartSystemInput = StartSingleServiceInput

// RestartServiceInput identifies one target service and the safe idempotency scope.
type RestartServiceInput struct {
	WorkspaceID        domain.WorkspaceID
	SystemID           domain.SystemID
	ServiceID          domain.ServiceID
	IdempotencySubject string
	IdempotencyKey     string
	Request            []byte
	Delay              time.Duration
}

// RuntimeStatus is the safe application snapshot used by the status API.
type RuntimeStatus struct {
	System            *domain.SystemInstance
	Services          []domain.ServiceInstance
	Resolved          *ResolvedSystemSpec
	ComposeContainers map[domain.ServiceInstanceID][]ComposeContainerStatus
	HealthCoverage    map[domain.ServiceInstanceID]health.CoverageSummary
}

// ComposeContainerStatus is a safe live container projection for the status API.
type ComposeContainerStatus struct {
	Service  string
	State    string
	Health   string
	ExitCode int
}

// SingleService owns asynchronous Phase 1B start/stop workers and active capture sessions.
type SingleService struct {
	config             SingleServiceConfig
	mutex              sync.Mutex
	workers            map[domain.OperationID]context.CancelCauseFunc
	captures           map[domain.ServiceInstanceID]CaptureSession
	liveness           map[domain.ServiceInstanceID]livenessRegistration
	livenessGeneration map[domain.ServiceInstanceID]uint64
	waiters            sync.WaitGroup
}

// NewSingleService validates and constructs the Phase 1B vertical slice.
func NewSingleService(config SingleServiceConfig) (*SingleService, error) {
	if config.Context == nil || config.Operations == nil || config.Workspaces == nil || config.Runner == nil ||
		config.Driver == nil || config.Runtime == nil || config.Readiness == nil || config.StartLogs == nil || config.DataDir == "" {
		return nil, fmt.Errorf("single-service dependencies are required")
	}
	if config.ResumeLogs == nil {
		config.ResumeLogs = config.StartLogs
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	if config.VerificationStableWindow <= 0 {
		config.VerificationStableWindow = defaultVerificationStableWindow
	}
	if config.VerificationTimeout <= 0 {
		config.VerificationTimeout = defaultVerificationTimeout
	}
	if config.VerificationPollInterval <= 0 {
		config.VerificationPollInterval = defaultVerificationPollInterval
	}
	if config.VerificationTimeout < config.VerificationStableWindow {
		return nil, fmt.Errorf("verification timeout must cover the stable window")
	}
	return &SingleService{
		config: config, workers: make(map[domain.OperationID]context.CancelCauseFunc),
		captures: make(map[domain.ServiceInstanceID]CaptureSession), liveness: make(map[domain.ServiceInstanceID]livenessRegistration),
		livenessGeneration: make(map[domain.ServiceInstanceID]uint64),
	}, nil
}

// SubmitStart creates a queued Operation and dispatches a worker without waiting for lifecycle completion.
func (service *SingleService) SubmitStart(ctx context.Context, input StartSingleServiceInput) (*CreateResult, error) {
	record, manifestValue, err := service.config.Workspaces.ExecutionManifest(ctx, input.WorkspaceID)
	if err != nil {
		return nil, err
	}
	repeated, err := service.repeatedStart(ctx, *record)
	if err != nil {
		return nil, err
	}
	stepKeys, multiService, err := service.startSteps(*manifestValue)
	if err != nil {
		return nil, err
	}
	result, err := service.config.Operations.Create(ctx, CreateInput{
		WorkspaceID: input.WorkspaceID, SystemID: input.SystemID, Type: domain.OperationStart,
		IdempotencySubject: input.IdempotencySubject, RouteKey: "system:start", IdempotencyKey: input.IdempotencyKey,
		Request: input.Request, Cancellable: true, StepKeys: stepKeys,
	})
	if err != nil || !result.Created {
		return result, err
	}
	if repeated {
		service.launch(result.Operation.ID, func(worker context.Context) { service.runRepeatedStart(worker, result.Operation) })
		return result, nil
	}
	if multiService {
		service.launch(result.Operation.ID, func(worker context.Context) { service.runSystemStart(worker, result.Operation, input) })
	} else {
		service.launch(result.Operation.ID, func(worker context.Context) { service.runStart(worker, result.Operation, input) })
	}
	return result, nil
}

func (service *SingleService) repeatedStart(ctx context.Context, record workspace.Record) (bool, error) {
	active, found, err := service.config.Runtime.GetActive(ctx, record.ID)
	if err != nil || !found {
		return false, err
	}
	if active.ManifestDigest != record.LastValidDigest {
		return false, ErrManifestChanged
	}
	if active.State == domain.SystemRunning || active.State == domain.SystemDegraded {
		return true, nil
	}
	return false, ErrSystemAlreadyActive
}

func (service *SingleService) runRepeatedStart(ctx context.Context, operation Operation) {
	if _, err := service.config.Operations.Start(ctx, operation.ID); err != nil {
		return
	}
	for _, step := range operation.Steps {
		if _, err := service.config.Operations.TransitionStep(ctx, operation.ID, step.Number, domain.OperationStepSkipped, "", ""); err != nil {
			_, _ = service.config.Operations.Fail(ctx, operation.ID, "OPERATION_INVALID_STATE")
			return
		}
	}
	_, _ = service.config.Operations.Succeed(ctx, operation.ID)
}

func (service *SingleService) startSteps(value manifest.Manifest) ([]string, bool, error) {
	if len(value.Spec.Services) == 1 {
		rootID, definition, err := singleRootService(value)
		if err != nil {
			return nil, false, err
		}
		if definition.Driver != string(domain.DriverCompose) && definition.Liveness == nil && definition.Restart.Policy == "never" {
			return startStepKeys(rootID, domain.ProcessMode(definition.Mode)), false, nil
		}
	}
	if service.config.PortPlanner == nil || service.config.PortLeases == nil || service.config.ResolvedSpecs == nil {
		return nil, true, fmt.Errorf("%w: system orchestration", ErrInvalidInput)
	}
	graph, err := NewDAG(value.Spec.Services)
	if err != nil {
		return nil, true, err
	}
	return systemStartStepKeys(graph, value.Spec.Services), true, nil
}

// SubmitStop creates a non-cancellable queued stop Operation and dispatches its worker.
func (service *SingleService) SubmitStop(ctx context.Context, input StopSingleServiceInput) (*CreateResult, error) {
	stepKeys, multiService, err := service.stopSteps(ctx, input.WorkspaceID)
	if err != nil {
		return nil, err
	}
	result, err := service.config.Operations.Create(ctx, CreateInput{
		WorkspaceID: input.WorkspaceID, SystemID: input.SystemID, Type: domain.OperationStop,
		IdempotencySubject: input.IdempotencySubject, RouteKey: "system:stop", IdempotencyKey: input.IdempotencyKey,
		Request: input.Request, Cancellable: false, StepKeys: stepKeys,
	})
	if err != nil || !result.Created {
		return result, err
	}
	if multiService {
		service.launch(result.Operation.ID, func(worker context.Context) { service.runSystemStop(worker, result.Operation) })
	} else {
		service.launch(result.Operation.ID, func(worker context.Context) { service.runStop(worker, result.Operation) })
	}
	return result, nil
}

// SubmitRestart queues one stop-then-fresh-start Operation using old and new snapshots at their respective boundaries.
func (service *SingleService) SubmitRestart(ctx context.Context, input RestartSystemInput) (*CreateResult, error) {
	_, value, err := service.config.Workspaces.ExecutionManifest(ctx, input.WorkspaceID)
	if err != nil {
		return nil, err
	}
	graph, err := NewDAG(value.Spec.Services)
	if err != nil {
		return nil, err
	}
	stopKeys, err := service.restartStopKeys(ctx, input.WorkspaceID)
	if err != nil {
		return nil, err
	}
	stepKeys := append(stopKeys, systemStartStepKeys(graph, value.Spec.Services)...)
	result, err := service.config.Operations.Create(ctx, CreateInput{
		WorkspaceID: input.WorkspaceID, SystemID: input.SystemID, Type: domain.OperationRestart,
		IdempotencySubject: input.IdempotencySubject, RouteKey: "system:restart", IdempotencyKey: input.IdempotencyKey,
		Request: input.Request, Cancellable: false, StepKeys: stepKeys,
	})
	if err != nil || !result.Created {
		return result, err
	}
	service.launch(result.Operation.ID, func(worker context.Context) { service.runSystemRestart(worker, result.Operation, input) })
	return result, nil
}

// SubmitServiceRestart queues a target-and-downstream restart against the running snapshot.
func (service *SingleService) SubmitServiceRestart(ctx context.Context, input RestartServiceInput) (*CreateResult, error) {
	closure, modes, err := service.serviceRestartClosure(ctx, input)
	if err != nil {
		return nil, err
	}
	stepKeys := serviceRestartStepKeys(closure, modes)
	result, err := service.config.Operations.Create(ctx, CreateInput{
		WorkspaceID: input.WorkspaceID, SystemID: input.SystemID, Type: domain.OperationServiceRestart,
		IdempotencySubject: input.IdempotencySubject, RouteKey: "service:restart:" + input.ServiceID.String(), IdempotencyKey: input.IdempotencyKey,
		Request: input.Request, Cancellable: false, StepKeys: stepKeys,
	})
	if err != nil || !result.Created {
		return result, err
	}
	service.launch(result.Operation.ID, func(worker context.Context) {
		if input.Delay > 0 {
			if err := waitDelay(worker, input.Delay); err != nil {
				return
			}
		}
		service.runServiceRestart(worker, result.Operation, input)
	})
	return result, nil
}

// GetOperation returns the persisted Operation snapshot.
func (service *SingleService) GetOperation(ctx context.Context, id domain.OperationID) (*Operation, error) {
	return service.config.Operations.Get(ctx, id)
}

// ListOperations returns a bounded newest-first operation center snapshot.
func (service *SingleService) ListOperations(ctx context.Context, workspaceID *domain.WorkspaceID, limit int) ([]Operation, error) {
	return service.config.Operations.List(ctx, workspaceID, limit)
}

// Status returns the active runtime, or an empty stopped snapshot when none exists.
func (service *SingleService) Status(ctx context.Context, workspaceID domain.WorkspaceID) (RuntimeStatus, error) {
	system, found, err := service.config.Runtime.GetActive(ctx, workspaceID)
	if err != nil {
		return RuntimeStatus{}, err
	}
	if !found {
		return RuntimeStatus{}, nil
	}
	services, err := service.config.Runtime.ListServices(ctx, system.ID)
	if err != nil {
		return RuntimeStatus{}, err
	}
	status := RuntimeStatus{System: system, Services: services}
	if service.config.ResolvedSpecs != nil && len(services) > 1 {
		status.Resolved, err = service.loadRuntimeSpec(ctx, system.ResolvedSpecDigest)
		if err != nil {
			return RuntimeStatus{}, err
		}
	}
	status.ComposeContainers = service.observeComposeContainers(ctx, services)
	status.HealthCoverage, err = service.healthCoverage(ctx, services, status.Resolved)
	if err != nil {
		return RuntimeStatus{}, err
	}
	return status, nil
}

func (service *SingleService) healthCoverage(ctx context.Context, services []domain.ServiceInstance, resolved *ResolvedSystemSpec) (map[domain.ServiceInstanceID]health.CoverageSummary, error) {
	result := make(map[domain.ServiceInstanceID]health.CoverageSummary, len(services))
	if resolved == nil {
		for _, runtime := range services {
			result[runtime.ID] = health.CoverageSummary{Coverage: domain.HealthCoverageUnavailable}
		}
		return result, nil
	}
	for _, runtime := range services {
		spec, exists := resolved.Services[runtime.ServiceID.String()]
		if !exists {
			return nil, ErrInvalidInput
		}
		latest, err := service.latestLiveness(ctx, runtime.ID)
		if err != nil {
			return nil, err
		}
		result[runtime.ID] = health.SummarizeCoverage(health.CoverageInput{
			Driver: runtime.Driver, Mode: runtime.ProcessMode, Required: spec.Required, State: runtime.State,
			ReadinessKind: spec.Readiness.Kind, Liveness: spec.Liveness, Latest: latest,
		})
	}
	return result, nil
}

func (service *SingleService) latestLiveness(ctx context.Context, id domain.ServiceInstanceID) (*health.Result, error) {
	if service.config.HealthResults == nil {
		return nil, nil
	}
	results, err := service.config.HealthResults.ListRecentByPurpose(ctx, id, health.PurposeLiveness, 1)
	if err != nil {
		return nil, fmt.Errorf("load latest liveness result: %w", err)
	}
	if len(results) == 0 {
		return nil, nil
	}
	return &results[0], nil
}

func (service *SingleService) observeComposeContainers(ctx context.Context, services []domain.ServiceInstance) map[domain.ServiceInstanceID][]ComposeContainerStatus {
	result := make(map[domain.ServiceInstanceID][]ComposeContainerStatus)
	if service.config.Compose == nil {
		return result
	}
	for _, runtime := range services {
		if runtime.Driver != domain.DriverCompose || runtime.ComposeIdentity == "" {
			continue
		}
		identity, err := compose.DecodeProjectIdentity(runtime.ComposeIdentity)
		if err != nil {
			continue
		}
		observation, err := service.config.Compose.Inspect(ctx, identity)
		if err != nil {
			continue
		}
		result[runtime.ID] = mapComposeContainers(observation)
	}
	return result
}

func mapComposeContainers(observation compose.ProjectObservation) []ComposeContainerStatus {
	result := make([]ComposeContainerStatus, 0, len(observation.Containers))
	for _, container := range observation.Containers {
		result = append(result, ComposeContainerStatus{
			Service: container.Service, State: container.State, Health: container.Health, ExitCode: container.ExitCode,
		})
	}
	return result
}

// CancelOperation records cancellation durably before signalling the in-memory worker.
func (service *SingleService) CancelOperation(ctx context.Context, id domain.OperationID) (*Operation, error) {
	operation, err := service.config.Operations.RequestCancel(ctx, id)
	if err != nil {
		return nil, err
	}
	if operation.State == domain.OperationCancelling {
		service.mutex.Lock()
		cancel := service.workers[id]
		service.mutex.Unlock()
		if cancel != nil {
			cancel(errUserCancellation)
		}
	}
	return operation, nil
}

// Wait blocks for workers, then closes active capture sessions without stopping managed processes.
func (service *SingleService) Wait() {
	service.waiters.Wait()
	service.mutex.Lock()
	sessions := make([]CaptureSession, 0, len(service.captures))
	for id, session := range service.captures {
		sessions = append(sessions, session)
		delete(service.captures, id)
	}
	service.mutex.Unlock()
	for _, session := range sessions {
		_ = session.Close()
	}
}

func (service *SingleService) launch(id domain.OperationID, run func(context.Context)) {
	worker, cancel := context.WithCancelCause(service.config.Context)
	service.mutex.Lock()
	service.workers[id] = cancel
	service.mutex.Unlock()
	service.waiters.Add(1)
	go func() {
		defer service.waiters.Done()
		defer service.removeWorker(id)
		run(worker)
	}()
}

func (service *SingleService) removeWorker(id domain.OperationID) {
	service.mutex.Lock()
	delete(service.workers, id)
	service.mutex.Unlock()
}

func startStepKeys(serviceID domain.ServiceID, mode domain.ProcessMode) []string {
	wait := "wait-ready:"
	if mode == domain.ProcessOneshot {
		wait = "wait-complete:"
	}
	return []string{"validate-manifest", "preflight-runner", "resolve-spec", "start:" + serviceID.String(), wait + serviceID.String(), "aggregate-state"}
}

func (service *SingleService) runStart(ctx context.Context, operation Operation, input StartSingleServiceInput) {
	if _, err := service.config.Operations.Start(ctx, operation.ID); err != nil {
		return
	}
	runtime, currentStep, err := service.prepareStart(ctx, operation, input)
	if err == nil {
		startCtx, cancel := startDeadline(ctx, runtime.Policies.StartTimeout)
		err = service.executeStart(startCtx, operation, &runtime, &currentStep)
		cancel()
	}
	if err == nil {
		_, err = service.config.Operations.Succeed(ctx, operation.ID)
	}
	if err != nil {
		service.finishStartFailure(ctx, operation, input, runtime, currentStep, err)
	}
}

func (service *SingleService) prepareStart(ctx context.Context, operation Operation, input StartSingleServiceInput) (resolvedSingleService, int, error) {
	var record *workspace.Record
	var manifestValue *manifest.Manifest
	err := service.runStep(ctx, operation.ID, 1, func() error {
		var loadErr error
		record, manifestValue, loadErr = service.config.Workspaces.ExecutionManifest(ctx, input.WorkspaceID)
		if loadErr == nil && record.SystemID != input.SystemID {
			loadErr = workspace.ErrNotFound
		}
		return loadErr
	})
	if err != nil {
		return resolvedSingleService{}, 1, err
	}
	systemID, serviceID, err := newRuntimeIDs()
	if err != nil {
		return resolvedSingleService{}, 2, err
	}
	var resolved resolvedSingleService
	err = service.runStep(ctx, operation.ID, 2, func() error {
		var resolveErr error
		resolved, resolveErr = resolveSingleService(ctx, service.config.Runner, resolveSingleInput{
			Workspace: *record, Manifest: *manifestValue, SystemID: systemID, ServiceID: serviceID,
			DataDir: service.config.DataDir, PortOverrides: input.PortOverrides,
		})
		if resolveErr != nil {
			return resolveErr
		}
		return service.config.Driver.Preflight(ctx, resolved.Process)
	})
	if err != nil {
		return resolved, 2, err
	}
	err = service.runStep(ctx, operation.ID, 3, func() error {
		return service.config.Runtime.Create(ctx, operation.ID, resolved.System, resolved.Service)
	})
	return resolved, 3, err
}

func (service *SingleService) executeStart(ctx context.Context, operation Operation, resolved *resolvedSingleService, currentStep *int) error {
	*currentStep = 4
	var identity driver.RuntimeIdentity
	if err := service.runStep(ctx, operation.ID, 4, func() error {
		launch, err := service.prepareProcessLaunch(ctx, operation.SystemID, resolved.Service.ID, resolved.Process)
		if err != nil {
			return err
		}
		defer launch.clear()
		var startErr error
		identity, startErr = service.config.Driver.Start(ctx, driver.StartRequest{Spec: launch.spec})
		if startErr != nil {
			return startErr
		}
		updated, attachErr := service.config.Runtime.AttachIdentity(ctx, operation.ID, resolved.Service.ID, resolved.Service.StateVersion, identity, time.Now().UTC())
		if attachErr == nil {
			resolved.Service = *updated
		}
		if attachErr != nil {
			return attachErr
		}
		return service.startCapture(operation, *resolved, launch.redactionValues)
	}); err != nil {
		return err
	}
	*currentStep = 5
	if err := service.awaitReady(ctx, operation, resolved, identity); err != nil {
		return err
	}
	*currentStep = 6
	return service.runStep(ctx, operation.ID, 6, func() error {
		_, err := service.config.Runtime.TransitionSystem(ctx, operation.ID, resolved.System.ID, domain.SystemRunning, time.Now().UTC())
		return err
	})
}

func (service *SingleService) startCapture(operation Operation, resolved resolvedSingleService, secretValues [][]byte) error {
	session, err := service.config.StartLogs(service.config.Context, logs.CaptureRequest{
		Scope: logs.Scope{
			SystemID: operation.SystemID, InstanceID: resolved.System.ID,
			ServiceID: resolved.Service.ServiceID, ServiceInstanceID: resolved.Service.ID, OperationID: operation.ID,
		},
		Spools: map[logs.Stream]string{logs.StreamStdout: resolved.Process.StdoutPath, logs.StreamStderr: resolved.Process.StderrPath}, SecretValues: secretValues,
	})
	if err != nil {
		return err
	}
	service.mutex.Lock()
	service.captures[resolved.Service.ID] = session
	service.mutex.Unlock()
	return nil
}

func (service *SingleService) awaitReady(ctx context.Context, operation Operation, resolved *resolvedSingleService, identity driver.RuntimeIdentity) error {
	return service.runStep(ctx, operation.ID, 5, func() error {
		if resolved.Process.Mode == domain.ProcessOneshot {
			updated, err := service.awaitOneshot(ctx, operation.ID, resolved.Service, identity)
			resolved.Service = updated
			return err
		}
		resolved.Readiness.Identity = identity
		outcome, err := service.config.Readiness.Await(ctx, health.Request{ServiceInstanceID: resolved.Service.ID, Spec: resolved.Readiness})
		if err != nil {
			return err
		}
		if !outcome.Ready {
			service.reportServiceIncident(ctx, resolved.System, resolved.Service, incident.KindReadinessTimeout, incident.SeverityCritical, string(outcome.ErrorCode), outcome.LastResult)
			return readinessFailure{code: string(outcome.ErrorCode)}
		}
		updated, err := service.config.Runtime.TransitionService(ctx, operation.ID, resolved.Service.ID, resolved.Service.StateVersion, domain.ServiceReady, "", nil, time.Now().UTC())
		if err == nil {
			resolved.Service = *updated
		}
		return err
	})
}

func (service *SingleService) finishStartFailure(ctx context.Context, operation Operation, input StartSingleServiceInput, resolved resolvedSingleService, step int, failure error) {
	finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), workerFinalizationTimeout)
	defer cancel()
	if errors.Is(context.Cause(ctx), errUserCancellation) {
		service.compensateCancelledStart(finalCtx, operation, resolved, step)
		return
	}
	code := singleServiceErrorCode(failure)
	service.failStep(finalCtx, operation.ID, step, code)
	service.skipStepsAfter(finalCtx, operation, step)
	if effectiveCleanupPolicy(resolved.Policies, input.FailurePolicy) {
		service.cleanupFailedStart(finalCtx, operation.ID, resolved, code)
	} else {
		service.markRuntimeFailed(finalCtx, operation.ID, resolved, code, oneshotFailureExitCode(failure))
	}
	if _, err := service.config.Operations.Fail(finalCtx, operation.ID, code); err != nil {
		service.logWorkerError(operation.ID, code, err)
	} else if code == "PORT_CONFLICT" {
		service.reportOperationIncident(finalCtx, operation, incident.KindPortConflict, incident.SeverityCritical, code)
	}
}

func effectiveCleanupPolicy(policies manifest.Policies, override FailurePolicyOverride) bool {
	if override.CleanupOnFailure != nil {
		return *override.CleanupOnFailure
	}
	return policies.CleanupOnFailure != nil && *policies.CleanupOnFailure
}

func (service *SingleService) cleanupFailedStart(ctx context.Context, operationID domain.OperationID, resolved resolvedSingleService, code string) {
	current, found, err := service.config.Runtime.GetService(ctx, resolved.Service.ID)
	if err != nil || !found {
		return
	}
	current, err = service.beginRuntimeStop(ctx, operationID, resolved.System.ID, *current)
	if err != nil {
		service.markRuntimeFailed(ctx, operationID, resolved, code, nil)
		return
	}
	if current.Identity != nil {
		if err := service.config.Driver.Stop(ctx, driver.StopRequest{Identity: *current.Identity, GracefulTimeout: current.GracefulTimeout}); err != nil && !errors.Is(err, driver.ErrRuntimeNotFound) {
			service.markRuntimeFailed(ctx, operationID, resolved, code, nil)
			return
		}
	}
	service.closeCapture(current.ID)
	if _, err := service.finishRuntimeStop(ctx, operationID, resolved.System.ID, *current); err != nil {
		service.markRuntimeFailed(ctx, operationID, resolved, code, nil)
	}
}

func (service *SingleService) compensateCancelledStart(ctx context.Context, operation Operation, resolved resolvedSingleService, step int) {
	service.cancelStepSet(ctx, operation.ID, step, len(operation.Steps))
	if resolved.Service.ID != "" {
		current, found, err := service.config.Runtime.GetService(ctx, resolved.Service.ID)
		if err == nil && found {
			err = service.stopRuntimeForCancellation(ctx, operation.ID, resolved.System, *current)
		}
		if err == nil && !found {
			service.completeCancellation(ctx, operation.ID)
			return
		}
		if err != nil {
			code := singleServiceStopErrorCode(err)
			service.markRuntimeFailed(ctx, operation.ID, resolved, code, nil)
			if _, failErr := service.config.Operations.Fail(ctx, operation.ID, code); failErr != nil {
				service.logWorkerError(operation.ID, code, failErr)
			}
			return
		}
	}
	service.completeCancellation(ctx, operation.ID)
}

func (service *SingleService) completeCancellation(ctx context.Context, id domain.OperationID) {
	if _, err := service.config.Operations.CompleteCancellation(ctx, id); err != nil {
		service.logWorkerError(id, "OPERATION_INVALID_STATE", err)
	}
}

func (service *SingleService) stopRuntimeForCancellation(ctx context.Context, operationID domain.OperationID, system domain.SystemInstance, current domain.ServiceInstance) error {
	updated, err := service.beginRuntimeStop(ctx, operationID, system.ID, current)
	if err != nil {
		return err
	}
	current = *updated
	if current.Identity != nil {
		err := service.config.Driver.Stop(ctx, driver.StopRequest{Identity: *current.Identity, GracefulTimeout: current.GracefulTimeout})
		if err != nil && !errors.Is(err, driver.ErrRuntimeNotFound) {
			return err
		}
	}
	service.closeCapture(current.ID)
	_, err = service.finishRuntimeStop(ctx, operationID, system.ID, current)
	return err
}

func (service *SingleService) beginRuntimeStop(ctx context.Context, operationID domain.OperationID, systemID domain.SystemInstanceID, current domain.ServiceInstance) (*domain.ServiceInstance, error) {
	if systemID != "" {
		if _, err := service.config.Runtime.TransitionSystem(ctx, operationID, systemID, domain.SystemStopping, time.Now().UTC()); err != nil {
			return nil, err
		}
	}
	if !current.State.CanTransitionTo(domain.ServiceStopping) {
		return &current, nil
	}
	return service.config.Runtime.TransitionService(ctx, operationID, current.ID, current.StateVersion, domain.ServiceStopping, "", nil, time.Now().UTC())
}

func (service *SingleService) finishRuntimeStop(ctx context.Context, operationID domain.OperationID, systemID domain.SystemInstanceID, current domain.ServiceInstance) (*domain.ServiceInstance, error) {
	updated := &current
	var err error
	if current.State.CanTransitionTo(domain.ServiceStopped) {
		updated, err = service.config.Runtime.TransitionService(ctx, operationID, current.ID, current.StateVersion, domain.ServiceStopped, "", nil, time.Now().UTC())
		if err != nil {
			return nil, err
		}
	}
	if systemID != "" {
		if _, err := service.config.Runtime.TransitionSystem(ctx, operationID, systemID, domain.SystemStopped, time.Now().UTC()); err != nil {
			return nil, err
		}
	}
	return updated, nil
}

func (service *SingleService) markRuntimeFailed(ctx context.Context, operationID domain.OperationID, resolved resolvedSingleService, code string, exitCode *uint32) {
	if resolved.Service.ID == "" {
		return
	}
	current, found, err := service.config.Runtime.GetService(ctx, resolved.Service.ID)
	if err == nil && found && current.State.CanTransitionTo(domain.ServiceFailed) {
		_, _ = service.config.Runtime.TransitionService(ctx, operationID, current.ID, current.StateVersion, domain.ServiceFailed, code, exitCode, time.Now().UTC())
	}
	if resolved.System.ID != "" && resolved.System.State == domain.SystemStarting {
		_, _ = service.config.Runtime.TransitionSystem(ctx, operationID, resolved.System.ID, domain.SystemFailed, time.Now().UTC())
	}
}

func (service *SingleService) runStop(ctx context.Context, operation Operation) {
	if _, err := service.config.Operations.Start(ctx, operation.ID); err != nil {
		return
	}
	instance, services, err := service.loadStopRuntime(ctx, operation)
	if errors.Is(err, errNoActiveRuntime) {
		service.finishNoActiveStop(ctx, operation)
		return
	}
	if err == nil {
		err = service.executeStop(ctx, operation, instance, services[0])
	}
	if err == nil {
		_, err = service.config.Operations.Succeed(ctx, operation.ID)
	}
	if err != nil {
		service.finishStopFailure(ctx, operation, err)
	}
}

var errNoActiveRuntime = errors.New("no active runtime")

func (service *SingleService) loadStopRuntime(ctx context.Context, operation Operation) (*domain.SystemInstance, []domain.ServiceInstance, error) {
	var instance *domain.SystemInstance
	var services []domain.ServiceInstance
	err := service.runStep(ctx, operation.ID, 1, func() error {
		var err error
		var found bool
		instance, found, err = service.config.Runtime.GetActive(ctx, operation.WorkspaceID)
		if err != nil {
			return err
		}
		if err == nil && !found {
			return errNoActiveRuntime
		}
		services, err = service.config.Runtime.ListServices(ctx, instance.ID)
		if err == nil && len(services) != 1 {
			err = ErrSingleServiceScope
		}
		return err
	})
	return instance, services, err
}

func (service *SingleService) executeStop(ctx context.Context, operation Operation, system *domain.SystemInstance, runtime domain.ServiceInstance) error {
	if _, err := service.config.Runtime.TransitionSystem(ctx, operation.ID, system.ID, domain.SystemStopping, time.Now().UTC()); err != nil {
		return err
	}
	if err := service.runStep(ctx, operation.ID, 2, func() error { return service.stopOne(ctx, operation.ID, &runtime) }); err != nil {
		return err
	}
	return service.runStep(ctx, operation.ID, 3, func() error {
		_, err := service.config.Runtime.TransitionSystem(ctx, operation.ID, system.ID, domain.SystemStopped, time.Now().UTC())
		return err
	})
}

func (service *SingleService) stopOne(ctx context.Context, operationID domain.OperationID, runtime *domain.ServiceInstance) error {
	if err := service.beginServiceStop(ctx, operationID, runtime); err != nil {
		return err
	}
	if runtime.Driver == domain.DriverCompose {
		if err := service.stopCompose(ctx, *runtime); err != nil {
			return err
		}
	} else if runtime.Identity != nil {
		if err := service.config.Driver.Stop(ctx, driver.StopRequest{Identity: *runtime.Identity, GracefulTimeout: runtime.GracefulTimeout}); err != nil && !errors.Is(err, driver.ErrRuntimeNotFound) {
			return err
		}
	}
	return service.completeServiceStop(ctx, operationID, runtime)
}

func (service *SingleService) beginServiceStop(ctx context.Context, operationID domain.OperationID, runtime *domain.ServiceInstance) error {
	if !runtime.State.CanTransitionTo(domain.ServiceStopping) {
		return nil
	}
	updated, err := service.config.Runtime.TransitionService(ctx, operationID, runtime.ID, runtime.StateVersion, domain.ServiceStopping, "", nil, time.Now().UTC())
	if err == nil {
		*runtime = *updated
	}
	return err
}

func (service *SingleService) completeServiceStop(ctx context.Context, operationID domain.OperationID, runtime *domain.ServiceInstance) error {
	service.closeCapture(runtime.ID)
	updated, err := service.config.Runtime.TransitionService(ctx, operationID, runtime.ID, runtime.StateVersion, domain.ServiceStopped, "", nil, time.Now().UTC())
	if err == nil {
		*runtime = *updated
	}
	return err
}

func (service *SingleService) finishStopFailure(ctx context.Context, operation Operation, failure error) {
	finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), workerFinalizationTimeout)
	defer cancel()
	code := singleServiceStopErrorCode(failure)
	current, getErr := service.config.Operations.Get(finalCtx, operation.ID)
	if getErr == nil {
		for _, step := range current.Steps {
			if step.State == domain.OperationStepRunning {
				service.failStep(finalCtx, operation.ID, step.Number, code)
			}
			if step.State == domain.OperationStepPending {
				_, _ = service.config.Operations.TransitionStep(finalCtx, operation.ID, step.Number, domain.OperationStepSkipped, "", "")
			}
		}
	}
	service.markActiveStopFailed(finalCtx, operation.ID, operation.WorkspaceID, code)
	if _, err := service.config.Operations.Fail(finalCtx, operation.ID, code); err != nil {
		service.logWorkerError(operation.ID, code, err)
	}
}

func (service *SingleService) runStep(ctx context.Context, id domain.OperationID, number int, action func() error) error {
	if _, err := service.config.Operations.TransitionStep(ctx, id, number, domain.OperationStepRunning, "", ""); err != nil {
		return err
	}
	if err := action(); err != nil {
		return err
	}
	_, err := service.config.Operations.TransitionStep(ctx, id, number, domain.OperationStepSucceeded, "", "")
	return err
}

func (service *SingleService) failStep(ctx context.Context, id domain.OperationID, number int, code string) {
	if number < 1 {
		return
	}
	if _, err := service.config.Operations.TransitionStep(ctx, id, number, domain.OperationStepFailed, code, ""); err != nil {
		service.logWorkerError(id, code, err)
	}
}

func (service *SingleService) skipStepsAfter(ctx context.Context, operation Operation, completed int) {
	for _, step := range operation.Steps {
		if step.Number > completed && step.State == domain.OperationStepPending {
			_, _ = service.config.Operations.TransitionStep(ctx, operation.ID, step.Number, domain.OperationStepSkipped, "", "")
		}
	}
}

func (service *SingleService) cancelStepSet(ctx context.Context, id domain.OperationID, active, total int) {
	for number := active; number <= total; number++ {
		_, _ = service.config.Operations.TransitionStep(ctx, id, number, domain.OperationStepCancelled, "", "")
	}
}

func (service *SingleService) finishNoActiveStop(ctx context.Context, operation Operation) {
	_, _ = service.config.Operations.TransitionStep(ctx, operation.ID, 1, domain.OperationStepSucceeded, "", "")
	for number := 2; number <= 3; number++ {
		_, _ = service.config.Operations.TransitionStep(ctx, operation.ID, number, domain.OperationStepSkipped, "", "")
	}
	_, _ = service.config.Operations.Succeed(ctx, operation.ID)
}

func (service *SingleService) markActiveStopFailed(ctx context.Context, operationID domain.OperationID, workspaceID domain.WorkspaceID, code string) {
	system, found, err := service.config.Runtime.GetActive(ctx, workspaceID)
	if err != nil || !found {
		return
	}
	services, _ := service.config.Runtime.ListServices(ctx, system.ID)
	for _, runtime := range services {
		if runtime.State.CanTransitionTo(domain.ServiceFailed) {
			_, _ = service.config.Runtime.TransitionService(ctx, operationID, runtime.ID, runtime.StateVersion, domain.ServiceFailed, code, nil, time.Now().UTC())
		}
	}
	if system.State.CanTransitionTo(domain.SystemFailed) {
		_, _ = service.config.Runtime.TransitionSystem(ctx, operationID, system.ID, domain.SystemFailed, time.Now().UTC())
	}
}

func (service *SingleService) closeCapture(id domain.ServiceInstanceID) {
	service.stopLiveness(id)
	service.mutex.Lock()
	session := service.captures[id]
	delete(service.captures, id)
	service.mutex.Unlock()
	if session != nil {
		_ = session.Close()
	}
}

func (service *SingleService) logWorkerError(id domain.OperationID, code string, err error) {
	service.config.Logger.Error("single-service worker finalization failed", "operation_id", id.String(), "error_code", code, "error", err)
}

func newRuntimeIDs() (domain.SystemInstanceID, domain.ServiceInstanceID, error) {
	now := time.Now().UTC()
	systemID, err := domain.NewSystemInstanceID(now, rand.Reader)
	if err != nil {
		return "", "", err
	}
	serviceID, err := domain.NewServiceInstanceID(now, rand.Reader)
	return systemID, serviceID, err
}

type readinessFailure struct{ code string }

func (failure readinessFailure) Error() string { return failure.code }

func singleServiceErrorCode(err error) string {
	if code := composeErrorCode(err); code != "" {
		return code
	}
	if code := secretErrorCode(err); code != "" {
		return code
	}
	var oneshotExit oneshotExitFailure
	if errors.As(err, &oneshotExit) {
		return "PROCESS_EXITED"
	}
	var oneshotTimeout oneshotTimeoutFailure
	if errors.As(err, &oneshotTimeout) {
		return "HEALTH_READINESS_TIMEOUT"
	}
	var readiness readinessFailure
	if errors.As(err, &readiness) && readiness.code != "" {
		return readiness.code
	}
	if errors.Is(err, driver.ErrIdentityMismatch) {
		return "PROCESS_IDENTITY_MISMATCH"
	}
	if errors.Is(err, driver.ErrRuntimeNotFound) {
		return "PROCESS_EXITED"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "HEALTH_READINESS_TIMEOUT"
	}
	if code, ok := manifest.ErrorCode(err); ok {
		return code
	}
	return resolutionErrorCode(err)
}

func singleServiceStopErrorCode(err error) string {
	if code := composeErrorCode(err); code != "" {
		return code
	}
	if errors.Is(err, driver.ErrIdentityMismatch) {
		return "PROCESS_IDENTITY_MISMATCH"
	}
	return "PROCESS_STOP_FAILED"
}

func composeErrorCode(err error) string {
	values := []struct {
		cause error
		code  string
	}{
		{compose.ErrDockerNotFound, "DOCKER_NOT_FOUND"}, {compose.ErrDockerVersionUnsupported, "DOCKER_VERSION_UNSUPPORTED"},
		{compose.ErrComposeNotFound, "COMPOSE_NOT_FOUND"}, {compose.ErrComposeVersionUnsupported, "COMPOSE_VERSION_UNSUPPORTED"},
		{compose.ErrDaemonUnavailable, "DOCKER_DAEMON_UNAVAILABLE"}, {compose.ErrConfigInvalid, "COMPOSE_CONFIG_INVALID"},
		{compose.ErrBuildConfigInvalid, "COMPOSE_BUILD_CONFIG_INVALID"}, {compose.ErrComposeBuildFailed, "COMPOSE_BUILD_FAILED"},
		{compose.ErrComposeBuildTimeout, "COMPOSE_BUILD_TIMEOUT"},
		{compose.ErrPreflightTimeout, "COMPOSE_PREFLIGHT_TIMEOUT"}, {compose.ErrOverrideInvalid, "COMPOSE_OVERRIDE_INVALID"},
		{compose.ErrOverrideConflict, "COMPOSE_OVERRIDE_CONFLICT"}, {compose.ErrLifecycleInvalid, "COMPOSE_LIFECYCLE_INVALID"},
		{compose.ErrLifecycleTimeout, "COMPOSE_LIFECYCLE_TIMEOUT"}, {compose.ErrComposeStartFailed, "COMPOSE_START_FAILED"},
		{compose.ErrComposeInspectFailed, "COMPOSE_INSPECT_FAILED"}, {compose.ErrComposeStopFailed, "COMPOSE_STOP_FAILED"},
		{compose.ErrProjectIdentityMismatch, "COMPOSE_PROJECT_IDENTITY_MISMATCH"}, {compose.ErrProjectNotFound, "COMPOSE_PROJECT_NOT_FOUND"},
		{compose.ErrLogFollowFailed, "COMPOSE_LOG_FOLLOW_FAILED"}, {compose.ErrDiscoveryFailed, "COMPOSE_DISCOVERY_FAILED"},
	}
	for _, value := range values {
		if errors.Is(err, value.cause) {
			return value.code
		}
	}
	return ""
}
