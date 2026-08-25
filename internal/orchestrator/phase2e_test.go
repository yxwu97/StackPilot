package orchestrator

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"stackpilot/internal/domain"
	"stackpilot/internal/health"
	"stackpilot/internal/incident"
	"stackpilot/internal/logs"
)

func TestEnsureRuntimeLivenessKeepsMatchingMonitorAndReplacesChangedSpec(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	monitor := &recordingLivenessMonitor{started: make(chan health.ResolvedSpec, 2)}
	service := &SingleService{
		config:   SingleServiceConfig{Context: ctx, Liveness: monitor, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		liveness: make(map[domain.ServiceInstanceID]livenessRegistration), livenessGeneration: make(map[domain.ServiceInstanceID]uint64),
	}
	runtime := phase2ERuntime()
	resolved := phase2EResolvedService(runtime)
	system := ResolvedSystemSpec{WorkspaceID: phase2EWorkspaceID, SystemID: "btc", InstanceID: phase2ESystemID}

	service.ensureRuntimeLiveness(system, runtime, resolved)
	awaitMonitorStart(t, monitor.started)
	service.ensureRuntimeLiveness(system, runtime, resolved)
	assertNoMonitorStart(t, monitor.started)

	changed := resolved
	changedSpec := *resolved.Liveness
	changedSpec.Interval = 3 * time.Second
	changed.Liveness = &changedSpec
	service.ensureRuntimeLiveness(system, runtime, changed)
	awaitMonitorStart(t, monitor.started)
	service.stopLiveness(runtime.ID)
	service.waiters.Wait()
	if monitor.startCount() != 2 {
		t.Fatalf("monitor starts = %d, want 2", monitor.startCount())
	}
}

func TestReportServiceIncidentBuildsBoundedRedactedLogEvidence(t *testing.T) {
	now := time.Date(2026, 8, 18, 8, 0, 0, 0, time.UTC)
	reporter := &recordingIncidentReporter{}
	reader := &incidentLogFixture{window: logs.Window{Entries: []logs.Entry{
		{Timestamp: now, SystemID: "btc", InstanceID: phase2ESystemID, ServiceID: "backend", Stream: logs.StreamStderr, Message: "token=secret", Sequence: 7},
		{Timestamp: now.Add(time.Second), SystemID: "btc", InstanceID: phase2ESystemID, ServiceID: "backend", Stream: logs.StreamStderr, Message: "token=secret", Sequence: 8},
	}}}
	service := &SingleService{config: SingleServiceConfig{Incidents: reporter, IncidentLogs: reader, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}}
	runtime := phase2ERuntime()
	system := domain.SystemInstance{ID: phase2ESystemID, WorkspaceID: phase2EWorkspaceID, SystemID: "btc"}
	service.reportServiceIncident(context.Background(), system, runtime, incident.KindKnownLogError, incident.SeverityCritical, "KNOWN_LOG", health.Result{CheckedAt: now})

	value := reporter.input.Context
	if len(value.Logs) != 1 || value.Logs[0].RepeatCount != 2 || strings.Contains(value.Logs[0].Message, "secret") {
		t.Fatalf("incident logs = %#v", value.Logs)
	}
	if len(value.Evidence) != 1 || value.Evidence[0].Type != "log" || value.Evidence[0].LogSequence != 7 {
		t.Fatalf("incident evidence = %#v", value.Evidence)
	}
	if reader.query.Limit != incident.MaximumLogLines || reader.query.From == nil || reader.query.To == nil ||
		!reader.query.From.Equal(now.Add(-incident.DefaultBeforeWindow)) || !reader.query.To.Equal(now.Add(incident.DefaultAfterWindow)) {
		t.Fatalf("incident query = %#v", reader.query)
	}
}

func TestRestartPolicyMatchesExit(t *testing.T) {
	zero, failed := uint32(0), uint32(17)
	tests := []struct {
		policy string
		code   *uint32
		want   bool
	}{
		{policy: "never", code: &failed, want: false},
		{policy: "on-failure", code: &zero, want: false},
		{policy: "on-failure", code: &failed, want: true},
		{policy: "on-failure", code: nil, want: true},
		{policy: "always", code: &zero, want: true},
	}
	for _, test := range tests {
		if got := restartPolicyMatchesExit(test.policy, test.code); got != test.want {
			t.Errorf("restartPolicyMatchesExit(%q, %v) = %v, want %v", test.policy, test.code, got, test.want)
		}
	}
}

type recordingLivenessMonitor struct {
	mutex   sync.Mutex
	starts  int
	started chan health.ResolvedSpec
}

func (monitor *recordingLivenessMonitor) MonitorLiveness(ctx context.Context, request health.LivenessRequest, _ health.LivenessHandler) error {
	monitor.mutex.Lock()
	monitor.starts++
	monitor.mutex.Unlock()
	monitor.started <- request.Spec
	<-ctx.Done()
	return ctx.Err()
}

func (monitor *recordingLivenessMonitor) startCount() int {
	monitor.mutex.Lock()
	defer monitor.mutex.Unlock()
	return monitor.starts
}

type incidentLogFixture struct {
	query  logs.WindowQuery
	window logs.Window
}

func (fixture *incidentLogFixture) QueryWindow(_ context.Context, query logs.WindowQuery) (logs.Window, error) {
	fixture.query = query
	return fixture.window, nil
}

func (*incidentLogFixture) Redact(value string) (string, error) {
	return strings.ReplaceAll(value, "secret", "[REDACTED]"), nil
}

type recordingIncidentReporter struct{ input incident.ReportInput }

func (reporter *recordingIncidentReporter) Report(_ context.Context, input incident.ReportInput) (*incident.Record, []incident.RuleResult, error) {
	reporter.input = input
	return &incident.Record{}, nil, nil
}

func phase2ERuntime() domain.ServiceInstance {
	return domain.ServiceInstance{
		ID: phase2EServiceInstanceID, SystemInstanceID: phase2ESystemID, ServiceID: "backend",
		ProcessMode: domain.ProcessDaemon, State: domain.ServiceReady,
		Identity: &domain.ProcessIdentity{PID: 42, StartedAt: time.Date(2026, 8, 18, 7, 0, 0, 0, time.UTC), ExecutablePath: `C:\fixture.exe`, CommandDigest: strings.Repeat("a", 64), PlatformToken: "token"},
	}
}

func phase2EResolvedService(runtime domain.ServiceInstance) ResolvedService {
	check := health.ResolvedSpec{
		Kind: health.KindProcess, Identity: *runtime.Identity, CheckTimeout: time.Second, Interval: 2 * time.Second,
		SuccessThreshold: 1, FailureThreshold: 2,
	}
	return ResolvedService{ServiceID: runtime.ServiceID, Liveness: &check, Restart: ResolvedRestartPolicy{Policy: "on-failure", InitialBackoff: time.Second, MaxBackoff: time.Minute, MaxAttempts: 3, StableWindow: 5 * time.Minute}}
}

func awaitMonitorStart(t *testing.T, started <-chan health.ResolvedSpec) {
	t.Helper()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("liveness monitor did not start")
	}
}

func assertNoMonitorStart(t *testing.T, started <-chan health.ResolvedSpec) {
	t.Helper()
	select {
	case <-started:
		t.Fatal("matching liveness monitor was restarted")
	case <-time.After(50 * time.Millisecond):
	}
}

const (
	phase2EWorkspaceID       = domain.WorkspaceID("ws_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	phase2ESystemID          = domain.SystemInstanceID("si_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	phase2EServiceInstanceID = domain.ServiceInstanceID("svi_01ARZ3NDEKTSV4RRFFQ69G5FAV")
)
