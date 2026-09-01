// ro04-sqlite-contention measures bounded control writes under concurrent metric persistence.
package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"sort"
	"sync"
	"time"

	"stackpilot/internal/domain"
	"stackpilot/internal/health"
	"stackpilot/internal/metrics"
	"stackpilot/internal/storage"
)

const (
	fixtureServices        = 19
	baselineCycles         = 40
	concurrentCycles       = 60
	preloadCycles          = 120
	controlP95Limit        = 250 * time.Millisecond
	controlMaximumLimit    = time.Second
	backgroundMaximumLimit = 2 * time.Second
)

var fixtureNow = time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)

type gateReport struct {
	SchemaVersion string          `json:"schemaVersion"`
	GeneratedAt   time.Time       `json:"generatedAt"`
	Scope         string          `json:"scope"`
	Platform      string          `json:"platform"`
	GoVersion     string          `json:"goVersion"`
	DatabaseMode  string          `json:"databaseMode"`
	ServiceCount  int             `json:"serviceCount"`
	PreloadedRows int             `json:"preloadedMetricRows"`
	GateStatus    string          `json:"gateStatus"`
	Blockers      []string        `json:"blockers,omitempty"`
	Limits        latencyLimits   `json:"limits"`
	Baseline      controlLatency  `json:"baseline"`
	Concurrent    concurrentStats `json:"concurrent"`
}

type latencyLimits struct {
	ControlP95Millis        float64 `json:"controlP95Millis"`
	ControlMaximumMillis    float64 `json:"controlMaximumMillis"`
	BackgroundMaximumMillis float64 `json:"backgroundMaximumMillis"`
}

type controlLatency struct {
	HealthWrite      latencySummary `json:"healthWrite"`
	RuntimeReconcile latencySummary `json:"runtimeReconcile"`
}

type concurrentStats struct {
	controlLatency
	MetricBatch latencySummary `json:"metricBatch"`
	Compaction  latencySummary `json:"compaction"`
}

type latencySummary struct {
	Count         int     `json:"count"`
	P50Millis     float64 `json:"p50Millis"`
	P95Millis     float64 `json:"p95Millis"`
	MaximumMillis float64 `json:"maximumMillis"`
}

type repositories struct {
	metrics *storage.MetricRepository
	health  *storage.HealthResultRepository
	runtime *storage.RuntimeInstanceRepository
}

type taskResult struct {
	name      string
	durations []time.Duration
	err       error
}

func main() {
	report, err := execute(context.Background())
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func execute(parent context.Context) (report gateReport, err error) {
	ctx, cancel := context.WithTimeout(parent, 45*time.Second)
	defer cancel()
	dataDir, err := os.MkdirTemp("", "stackpilot-ro04-contention-")
	if err != nil {
		return gateReport{}, fmt.Errorf("create isolated data directory: %w", err)
	}
	defer func() {
		if removeErr := os.RemoveAll(dataDir); removeErr != nil {
			err = errors.Join(err, fmt.Errorf("remove isolated data directory: %w", removeErr))
		}
	}()
	database, err := storage.OpenDataDir(ctx, dataDir)
	if err != nil {
		return gateReport{}, err
	}
	defer func() {
		if closeErr := database.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close isolated database: %w", closeErr))
		}
	}()
	if err := verifyWAL(ctx, database); err != nil {
		return gateReport{}, err
	}
	serviceIDs, systemID, err := seedFixture(ctx, database)
	if err != nil {
		return gateReport{}, err
	}
	repositories, err := newRepositories(database)
	if err != nil {
		return gateReport{}, err
	}
	if err := preloadMetrics(ctx, repositories.metrics, serviceIDs); err != nil {
		return gateReport{}, err
	}
	if err := requireRowCount(ctx, database, "runtime_metric_samples", fixtureServices*preloadCycles); err != nil {
		return gateReport{}, err
	}
	baseline, err := runBaseline(ctx, repositories, serviceIDs[0], systemID)
	if err != nil {
		return gateReport{}, err
	}
	concurrent, err := runConcurrent(ctx, repositories, serviceIDs, systemID)
	if err != nil {
		return gateReport{}, err
	}
	if err := verifyFinalState(ctx, database, systemID); err != nil {
		return gateReport{}, err
	}
	return evaluateReport(baseline, concurrent), nil
}

func verifyWAL(ctx context.Context, database *sql.DB) error {
	var mode string
	if err := database.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
		return fmt.Errorf("read isolated database journal mode: %w", err)
	}
	if mode != "wal" {
		return fmt.Errorf("isolated database journal mode is %q, want wal", mode)
	}
	return nil
}

func requireRowCount(ctx context.Context, database *sql.DB, table string, expected int) error {
	allowed := map[string]bool{"runtime_metric_samples": true, "health_results": true}
	if !allowed[table] {
		return fmt.Errorf("row-count table is not allowed")
	}
	var count int
	if err := database.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
		return fmt.Errorf("count %s: %w", table, err)
	}
	if count != expected {
		return fmt.Errorf("%s row count is %d, want %d", table, count, expected)
	}
	return nil
}

func verifyFinalState(ctx context.Context, database *sql.DB, systemID domain.SystemInstanceID) error {
	if err := requireRowCount(ctx, database, "runtime_metric_samples", fixtureServices*concurrentCycles); err != nil {
		return err
	}
	if err := requireRowCount(ctx, database, "health_results", baselineCycles+concurrentCycles); err != nil {
		return err
	}
	var reconciled string
	if err := database.QueryRowContext(ctx, "SELECT last_reconciled_at FROM system_instances WHERE id = ?", systemID.String()).Scan(&reconciled); err != nil {
		return fmt.Errorf("read final reconciliation marker: %w", err)
	}
	if reconciled == "" {
		return fmt.Errorf("final reconciliation marker is empty")
	}
	return nil
}

func newRepositories(database *sql.DB) (repositories, error) {
	metricRepository, err := storage.NewMetricRepository(database)
	if err != nil {
		return repositories{}, err
	}
	healthRepository, err := storage.NewHealthResultRepository(database)
	if err != nil {
		return repositories{}, err
	}
	runtimeRepository, err := storage.NewRuntimeInstanceRepository(database, nil)
	if err != nil {
		return repositories{}, err
	}
	return repositories{metrics: metricRepository, health: healthRepository, runtime: runtimeRepository}, nil
}

func seedFixture(ctx context.Context, database *sql.DB) (serviceIDs []domain.ServiceInstanceID, systemInstanceID domain.SystemInstanceID, err error) {
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		return nil, "", err
	}
	defer func() {
		if rollbackErr := transaction.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			err = errors.Join(err, fmt.Errorf("rollback fixture transaction: %w", rollbackErr))
		}
	}()
	const workspaceID = "ws_01ARZ3NDEKTSV4RRFFQ69G5FAV"
	const systemID = "si_01ARZ3NDEKTSV4RRFFQ69G5FAV"
	const digest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	now := fixtureNow.Format(time.RFC3339)
	statements := []string{
		`INSERT INTO systems(id,name,created_at,updated_at) VALUES ('gate','RO04 Gate',?,?)`,
		`INSERT INTO manifest_snapshots(digest,system_id,api_version,normalized_yaml,parsed_json,created_at) VALUES (?,'gate','stackpilot.io/v1alpha1','{}','{}',?)`,
		`INSERT INTO workspaces(id,system_id,root_path,canonical_path,manifest_status,last_valid_digest,created_at,updated_at) VALUES (?,'gate','C:\fixture','C:\fixture','valid',?,?,?)`,
		`INSERT INTO system_instances(id,workspace_id,manifest_digest,resolved_spec_digest,state,started_at) VALUES (?,?,?,?, 'running',?)`,
	}
	arguments := [][]any{{now, now}, {digest, now}, {workspaceID, digest, now, now}, {systemID, workspaceID, digest, digest, now}}
	for index, statement := range statements {
		if _, err := transaction.ExecContext(ctx, statement, arguments[index]...); err != nil {
			return nil, "", fmt.Errorf("seed fixture catalog: %w", err)
		}
	}
	serviceIDs = make([]domain.ServiceInstanceID, 0, fixtureServices)
	for index := 0; index < fixtureServices; index++ {
		serviceID := fmt.Sprintf("service-%02d", index+1)
		instanceID, err := domain.NewServiceInstanceID(fixtureNow.Add(time.Duration(index)*time.Millisecond), bytes.NewReader(make([]byte, 10)))
		if err != nil {
			return nil, "", err
		}
		if _, err := transaction.ExecContext(ctx, `INSERT INTO services(workspace_id,service_id,driver,mode,required,definition_digest) VALUES (?,?,'process','daemon',1,?)`, workspaceID, serviceID, digest); err != nil {
			return nil, "", fmt.Errorf("seed fixture service definition: %w", err)
		}
		if _, err := transaction.ExecContext(ctx, `INSERT INTO service_instances(id,system_instance_id,service_id,state,state_version,created_at,updated_at) VALUES (?,?,?,'ready',1,?,?)`, instanceID.String(), systemID, serviceID, now, now); err != nil {
			return nil, "", fmt.Errorf("seed fixture service instance: %w", err)
		}
		serviceIDs = append(serviceIDs, instanceID)
	}
	if err := transaction.Commit(); err != nil {
		return nil, "", err
	}
	return serviceIDs, domain.SystemInstanceID(systemID), nil
}

func preloadMetrics(ctx context.Context, repository *storage.MetricRepository, ids []domain.ServiceInstanceID) error {
	start := fixtureNow.Add(-26 * time.Hour)
	for cycle := 0; cycle < preloadCycles; cycle++ {
		if err := repository.SaveBatch(ctx, metricBatch(ids, start.Add(time.Duration(cycle)*metrics.DefaultInterval))); err != nil {
			return fmt.Errorf("preload metric batch: %w", err)
		}
	}
	return nil
}

func runBaseline(ctx context.Context, repositories repositories, serviceID domain.ServiceInstanceID, systemID domain.SystemInstanceID) (controlLatency, error) {
	healthDurations, err := measureHealth(ctx, repositories.health, serviceID, fixtureNow.Add(time.Hour), baselineCycles)
	if err != nil {
		return controlLatency{}, err
	}
	runtimeDurations, err := measureRuntime(ctx, repositories.runtime, systemID, fixtureNow.Add(time.Hour), baselineCycles)
	if err != nil {
		return controlLatency{}, err
	}
	return controlLatency{HealthWrite: summarize(healthDurations), RuntimeReconcile: summarize(runtimeDurations)}, nil
}

func runConcurrent(ctx context.Context, repositories repositories, ids []domain.ServiceInstanceID, systemID domain.SystemInstanceID) (concurrentStats, error) {
	start := make(chan struct{})
	results := make(chan taskResult, 4)
	var workers sync.WaitGroup
	launch := func(name string, task func() ([]time.Duration, error)) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			durations, err := task()
			results <- taskResult{name: name, durations: durations, err: err}
		}()
	}
	launch("health", func() ([]time.Duration, error) {
		return measureHealth(ctx, repositories.health, ids[0], fixtureNow.Add(2*time.Hour), concurrentCycles)
	})
	launch("runtime", func() ([]time.Duration, error) {
		return measureRuntime(ctx, repositories.runtime, systemID, fixtureNow.Add(2*time.Hour), concurrentCycles)
	})
	launch("metrics", func() ([]time.Duration, error) {
		return measureMetricBatches(ctx, repositories.metrics, ids, fixtureNow.Add(3*time.Hour), concurrentCycles)
	})
	launch("compaction", func() ([]time.Duration, error) { return measureCompaction(ctx, repositories.metrics, 4) })
	close(start)
	workers.Wait()
	close(results)
	return collectConcurrent(results)
}

func collectConcurrent(results <-chan taskResult) (concurrentStats, error) {
	var report concurrentStats
	for result := range results {
		if result.err != nil {
			return concurrentStats{}, fmt.Errorf("%s workload: %w", result.name, result.err)
		}
		summary := summarize(result.durations)
		switch result.name {
		case "health":
			report.HealthWrite = summary
		case "runtime":
			report.RuntimeReconcile = summary
		case "metrics":
			report.MetricBatch = summary
		case "compaction":
			report.Compaction = summary
		}
	}
	return report, nil
}

func measureHealth(ctx context.Context, repository *storage.HealthResultRepository, id domain.ServiceInstanceID, start time.Time, cycles int) ([]time.Duration, error) {
	return measure(cycles, func(index int) error {
		result := health.Result{Purpose: health.PurposeLiveness, Kind: health.KindProcess, Success: true, Duration: 2 * time.Millisecond, Summary: "fixture healthy", CheckedAt: start.Add(time.Duration(index) * time.Second)}
		return repository.Record(ctx, id, result)
	})
}

func measureRuntime(ctx context.Context, repository *storage.RuntimeInstanceRepository, id domain.SystemInstanceID, start time.Time, cycles int) ([]time.Duration, error) {
	return measure(cycles, func(index int) error {
		return repository.MarkReconciled(ctx, id, start.Add(time.Duration(index)*time.Second))
	})
}

func measureMetricBatches(ctx context.Context, repository *storage.MetricRepository, ids []domain.ServiceInstanceID, start time.Time, cycles int) ([]time.Duration, error) {
	return measure(cycles, func(index int) error {
		return repository.SaveBatch(ctx, metricBatch(ids, start.Add(time.Duration(index)*metrics.DefaultInterval)))
	})
}

func measureCompaction(ctx context.Context, repository *storage.MetricRepository, cycles int) ([]time.Duration, error) {
	return measure(cycles, func(int) error { _, err := repository.CompactDefault(ctx, fixtureNow); return err })
}

func measure(cycles int, operation func(int) error) ([]time.Duration, error) {
	durations := make([]time.Duration, 0, cycles)
	for index := 0; index < cycles; index++ {
		started := time.Now()
		if err := operation(index); err != nil {
			return nil, err
		}
		durations = append(durations, time.Since(started))
	}
	return durations, nil
}

func metricBatch(ids []domain.ServiceInstanceID, observedAt time.Time) []metrics.Sample {
	result := make([]metrics.Sample, 0, len(ids))
	for index, id := range ids {
		cpu, memory, processes := float64(index+1), int64(1<<20)*(int64(index)+1), int64(1)
		result = append(result, metrics.Sample{ServiceInstanceID: id, Source: domain.MetricSourceProcessJob, Status: domain.MetricAvailable, ObservedAt: observedAt, Interval: metrics.DefaultInterval, CPUPercent: &cpu, MemoryBytes: &memory, ProcessCount: &processes})
	}
	return result
}

func summarize(values []time.Duration) latencySummary {
	ordered := append([]time.Duration(nil), values...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	return latencySummary{Count: len(ordered), P50Millis: milliseconds(percentile(ordered, 50)), P95Millis: milliseconds(percentile(ordered, 95)), MaximumMillis: milliseconds(ordered[len(ordered)-1])}
}

func percentile(values []time.Duration, percent int) time.Duration {
	index := (len(values)*percent + 99) / 100
	if index < 1 {
		index = 1
	}
	return values[index-1]
}

func milliseconds(value time.Duration) float64 {
	return float64(value) / float64(time.Millisecond)
}

func evaluateReport(baseline controlLatency, concurrent concurrentStats) gateReport {
	report := gateReport{
		SchemaVersion: "ro04-sqlite-contention/v1", GeneratedAt: time.Now().UTC(), Scope: "isolated-sqlite-wal",
		Platform: runtime.GOOS + "/" + runtime.GOARCH, GoVersion: runtime.Version(), DatabaseMode: "temporary-wal",
		ServiceCount: fixtureServices, PreloadedRows: fixtureServices * preloadCycles,
		Limits:   latencyLimits{ControlP95Millis: milliseconds(controlP95Limit), ControlMaximumMillis: milliseconds(controlMaximumLimit), BackgroundMaximumMillis: milliseconds(backgroundMaximumLimit)},
		Baseline: baseline, Concurrent: concurrent,
	}
	for name, summary := range map[string]latencySummary{"HEALTH_WRITE": concurrent.HealthWrite, "RUNTIME_RECONCILE": concurrent.RuntimeReconcile} {
		if summary.P95Millis > report.Limits.ControlP95Millis || summary.MaximumMillis > report.Limits.ControlMaximumMillis {
			report.Blockers = append(report.Blockers, name+"_LATENCY_LIMIT_EXCEEDED")
		}
	}
	for name, summary := range map[string]latencySummary{"METRIC_BATCH": concurrent.MetricBatch, "METRIC_COMPACTION": concurrent.Compaction} {
		if summary.MaximumMillis > report.Limits.BackgroundMaximumMillis {
			report.Blockers = append(report.Blockers, name+"_LATENCY_LIMIT_EXCEEDED")
		}
	}
	sort.Strings(report.Blockers)
	report.GateStatus = "passed"
	if len(report.Blockers) > 0 {
		report.GateStatus = "blocked"
	}
	return report
}
