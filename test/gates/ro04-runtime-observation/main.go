// ro04-runtime-observation executes a read-only observation against persisted runtime identities.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"time"

	"stackpilot/internal/domain"
	composedriver "stackpilot/internal/driver/compose"
	processdriver "stackpilot/internal/driver/process"
	"stackpilot/internal/metrics"
	"stackpilot/internal/security"
	"stackpilot/internal/storage"
)

type gateReport struct {
	SchemaVersion   string          `json:"schemaVersion"`
	GeneratedAt     time.Time       `json:"generatedAt"`
	Platform        string          `json:"platform"`
	GoVersion       string          `json:"goVersion"`
	ClientTrust     string          `json:"clientTrust"`
	DatabaseVersion int             `json:"databaseVersion"`
	GateStatus      string          `json:"gateStatus"`
	Blockers        []string        `json:"blockers"`
	Systems         int             `json:"activeSystems"`
	Services        []serviceReport `json:"services"`
}

type serviceReport struct {
	SystemID       domain.SystemID     `json:"systemId"`
	ServiceID      domain.ServiceID    `json:"serviceId"`
	Driver         domain.DriverKind   `json:"driver"`
	RuntimeState   domain.ServiceState `json:"runtimeState"`
	MetricStatus   string              `json:"metricStatus"`
	ReasonCode     string              `json:"reasonCode,omitempty"`
	ObservedAt     *time.Time          `json:"observedAt,omitempty"`
	CPUPercent     *float64            `json:"cpuPercent,omitempty"`
	MemoryBytes    *int64              `json:"memoryBytes,omitempty"`
	ProcessCount   *int64              `json:"processCount,omitempty"`
	ContainerCount *int64              `json:"containerCount,omitempty"`
}

func main() {
	databasePath := flag.String("database", "", "absolute control database opened read-only")
	expectedVersion := flag.Int("expected-version", 0, "required schema version; zero accepts the current version")
	flag.Parse()
	if err := run(*databasePath, *expectedVersion); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(path string, expectedVersion int) error {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	database, err := openReadOnly(ctx, path)
	if err != nil {
		return err
	}
	defer database.Close()
	version, err := databaseVersion(ctx, database)
	if err != nil {
		return err
	}
	if expectedVersion > 0 && version != expectedVersion {
		return fmt.Errorf("control database version is %d, expected %d", version, expectedVersion)
	}
	report, err := observe(ctx, database, version)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(report)
}

func openReadOnly(ctx context.Context, path string) (*sql.DB, error) {
	if path == "" || !filepath.IsAbs(path) {
		return nil, fmt.Errorf("absolute control database path is required")
	}
	canonical, err := security.CanonicalExistingPath(path)
	if err != nil {
		return nil, fmt.Errorf("canonicalize control database: %w", err)
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("control database is not a regular file")
	}
	database, err := sql.Open("sqlite", readOnlyDSN(canonical))
	if err != nil {
		return nil, fmt.Errorf("open control database read-only: %w", err)
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	if err := database.PingContext(ctx); err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("connect to control database read-only: %w", err)
	}
	var queryOnly int
	if err := database.QueryRowContext(ctx, "PRAGMA query_only").Scan(&queryOnly); err != nil || queryOnly != 1 {
		_ = database.Close()
		return nil, fmt.Errorf("control database query-only enforcement failed")
	}
	return database, nil
}

func readOnlyDSN(path string) string {
	location := filepath.ToSlash(path)
	if filepath.VolumeName(path) != "" {
		location = "/" + location
	}
	endpoint := url.URL{Scheme: "file", Path: location}
	query := url.Values{}
	query.Add("mode", "ro")
	query.Add("_pragma", "query_only(ON)")
	query.Add("_pragma", "busy_timeout(5000)")
	endpoint.RawQuery = query.Encode()
	return endpoint.String()
}

func databaseVersion(ctx context.Context, database *sql.DB) (int, error) {
	var version sql.NullInt64
	if err := database.QueryRowContext(ctx, "SELECT MAX(version) FROM schema_migrations").Scan(&version); err != nil {
		return 0, fmt.Errorf("read control database version: %w", err)
	}
	if !version.Valid {
		return 0, fmt.Errorf("control database migration history is empty")
	}
	return int(version.Int64), nil
}

func observe(ctx context.Context, database *sql.DB, version int) (gateReport, error) {
	repository, err := storage.NewRuntimeInstanceRepository(database, nil)
	if err != nil {
		return gateReport{}, err
	}
	instances, err := repository.ListActive(ctx)
	if err != nil {
		return gateReport{}, err
	}
	processSource, err := metrics.NewProcessSource(processdriver.New(processdriver.Config{}), runtime.NumCPU())
	if err != nil {
		return gateReport{}, err
	}
	composeLifecycle, composeErr := composedriver.NewLifecycle(composedriver.LifecycleConfig{})
	var composeSource *metrics.ComposeSource
	if composeErr == nil {
		composeSource, composeErr = metrics.NewComposeSource(composeLifecycle)
	}
	report := gateReport{
		SchemaVersion: "ro04-runtime-observation/v1", GeneratedAt: time.Now().UTC(),
		Platform: runtime.GOOS + "/" + runtime.GOARCH, GoVersion: runtime.Version(),
		ClientTrust:     "development-build-untrusted-by-installed-supervisor",
		DatabaseVersion: version, Systems: len(instances), Services: make([]serviceReport, 0),
	}
	for _, instance := range instances {
		services, err := repository.ListServices(ctx, instance.ID)
		if err != nil {
			return gateReport{}, err
		}
		for _, service := range services {
			report.Services = append(report.Services, observeService(ctx, instance.SystemID, service, processSource, composeSource))
		}
	}
	finalize(&report)
	return report, nil
}

func observeService(ctx context.Context, systemID domain.SystemID, service domain.ServiceInstance, process *metrics.ProcessSource, compose *metrics.ComposeSource) serviceReport {
	report := serviceReport{SystemID: systemID, ServiceID: service.ServiceID, Driver: service.Driver, RuntimeState: service.State}
	if service.ProcessMode != domain.ProcessDaemon || service.State != domain.ServiceReady && service.State != domain.ServiceDegraded {
		report.MetricStatus, report.ReasonCode = "skipped", "RUNTIME_NOT_ELIGIBLE"
		return report
	}
	probeContext, cancel := context.WithTimeout(ctx, metrics.DefaultSampleTimeout)
	defer cancel()
	var sample metrics.Sample
	if service.Driver == domain.DriverCompose {
		if compose == nil {
			report.MetricStatus, report.ReasonCode = string(domain.MetricUnavailable), "COMPOSE_ADAPTER_UNAVAILABLE"
			return report
		}
		sample = compose.Observe(probeContext, service, metrics.DefaultInterval)
	} else {
		sample = process.Observe(probeContext, service, metrics.DefaultInterval)
	}
	if err := metrics.ValidateSample(sample); err != nil {
		report.MetricStatus, report.ReasonCode = string(domain.MetricUnavailable), "INVALID_OBSERVATION"
		return report
	}
	report.MetricStatus, report.ReasonCode = string(sample.Status), sample.ReasonCode
	report.ObservedAt, report.CPUPercent = &sample.ObservedAt, sample.CPUPercent
	report.MemoryBytes, report.ProcessCount, report.ContainerCount = sample.MemoryBytes, sample.ProcessCount, sample.ContainerCount
	return report
}

func finalize(report *gateReport) {
	sort.Slice(report.Services, func(i, j int) bool {
		if report.Services[i].SystemID != report.Services[j].SystemID {
			return report.Services[i].SystemID < report.Services[j].SystemID
		}
		return report.Services[i].ServiceID < report.Services[j].ServiceID
	})
	available := map[domain.DriverKind]bool{}
	reasons := map[string]bool{}
	for _, service := range report.Services {
		if service.MetricStatus == string(domain.MetricAvailable) {
			available[service.Driver] = true
		} else if service.MetricStatus != "skipped" {
			reasons[service.ReasonCode] = true
		}
	}
	if !available[domain.DriverProcess] {
		reasons["PROCESS_REAL_OBSERVATION_REQUIRED"] = true
	}
	if !available[domain.DriverCompose] {
		reasons["COMPOSE_REAL_OBSERVATION_REQUIRED"] = true
	}
	for reason := range reasons {
		if reason != "" {
			report.Blockers = append(report.Blockers, reason)
		}
	}
	if reasons["PROCESS_IDENTITY_MISMATCH"] {
		report.Blockers = append(report.Blockers, "TRUSTED_INSTALL_RUNTIME_GATE_REQUIRED")
	}
	sort.Strings(report.Blockers)
	report.GateStatus = "blocked"
	if len(report.Blockers) == 0 {
		report.GateStatus = "passed"
	}
}
