package orchestrator_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"stackpilot/internal/domain"
	"stackpilot/internal/health"
	"stackpilot/internal/incident"
	"stackpilot/internal/orchestrator"
	"stackpilot/internal/storage"
)

func TestPhase2EFaultDiagnosticsPersistTraceableNonAutomaticAnalyses(t *testing.T) {
	t.Run("port conflict", testPortConflictDiagnostic)
	t.Run("process exit", testProcessExitDiagnostic)
	t.Run("readiness timeout", testReadinessTimeoutDiagnostic)
	for _, test := range []struct {
		name, message string
	}{
		{name: "java", message: "java.net.BindException: Address already in use"},
		{name: "node", message: "Error: listen EADDRINUSE: address already in use"},
		{name: "python", message: "ModuleNotFoundError: No module named 'fixture'"},
	} {
		t.Run("known "+test.name+" log", func(t *testing.T) { testKnownLogDiagnostic(t, test.message) })
	}
}

func testPortConflictDiagnostic(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	port := listener.Addr().(*net.TCPAddr).Port
	harness := newSystemServiceHarnessWithWriter(t, immediateReadiness{}, func(root string) {
		writePortConflictManifest(t, root, port)
	})
	result, err := harness.service.SubmitStart(context.Background(), orchestrator.StartSingleServiceInput{
		WorkspaceID: harness.workspace.ID, SystemID: harness.workspace.SystemID,
		IdempotencySubject: "test-user", IdempotencyKey: "port-conflict", Request: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	operation := awaitOperation(t, harness.service, result.Operation.ID, domain.OperationFailed)
	if operation.ErrorCode != "PORT_CONFLICT" {
		t.Fatalf("port conflict error code = %q", operation.ErrorCode)
	}
	assertIncidentRule(t, harness, incident.KindPortConflict, "port-conflict")
}

func testProcessExitDiagnostic(t *testing.T) {
	harness := newAutomaticRestartHarness(t, "on-failure", 2)
	startAutomaticRestartFixture(t, harness)
	exitCode := uint32(19)
	harness.driver.setObservation("exited", &exitCode)
	if err := harness.service.ReconcileRuntimes(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertIncidentRule(t, harness, incident.KindProcessExit, "process-exit")
}

func testReadinessTimeoutDiagnostic(t *testing.T) {
	harness := newSystemServiceHarnessWithWriter(t, failedReadiness{}, func(root string) {
		writeProcessReadinessManifest(t, root)
	})
	result, err := harness.service.SubmitStart(context.Background(), orchestrator.StartSingleServiceInput{
		WorkspaceID: harness.workspace.ID, SystemID: harness.workspace.SystemID,
		IdempotencySubject: "test-user", IdempotencyKey: "readiness-timeout", Request: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	operation := awaitOperation(t, harness.service, result.Operation.ID, domain.OperationFailed)
	if operation.ErrorCode != string(health.CodeReadinessTimeout) {
		t.Fatalf("readiness error code = %q", operation.ErrorCode)
	}
	assertIncidentRule(t, harness, incident.KindReadinessTimeout, "readiness-timeout")
}

func testKnownLogDiagnostic(t *testing.T, message string) {
	harness := newSystemServiceHarnessWithWriter(t, immediateReadiness{}, func(root string) {
		writeProcessReadinessManifest(t, root)
	})
	startAutomaticRestartFixture(t, harness)
	status, err := harness.service.Status(context.Background(), harness.workspace.ID)
	if err != nil || status.System == nil || len(status.Services) != 1 {
		t.Fatalf("fixture status = (%#v, %v)", status, err)
	}
	runtime := status.Services[0]
	var eventID domain.EventID
	if err := harness.database.QueryRow(`SELECT id FROM events WHERE service_instance_id=? ORDER BY id DESC LIMIT 1`, runtime.ID.String()).Scan(&eventID); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	repository, _ := storage.NewIncidentRepository(harness.database)
	coordinator, _ := incident.NewCoordinator(repository, nil)
	contextValue := incident.Context{
		SchemaVersion: "1", WorkspaceID: harness.workspace.ID, SystemInstanceID: status.System.ID,
		ServiceInstanceID: runtime.ID, ServiceID: runtime.ServiceID, Kind: incident.KindKnownLogError,
		TriggerCode: "KNOWN_STARTUP_ERROR", WindowStart: now.Add(-time.Minute), WindowEnd: now.Add(time.Minute),
		Dependencies: map[string]domain.ServiceState{}, Ports: map[string]int{},
		Evidence: []incident.EvidenceRef{{Type: "event", EventID: eventID, ServiceInstanceID: runtime.ID}},
		Logs:     []incident.LogLine{{ServiceInstanceID: runtime.ID, Sequence: 1, Timestamp: now, Stream: "stderr", Message: message}},
	}
	if _, _, err := coordinator.Report(context.Background(), incident.ReportInput{
		Context: contextValue, Severity: incident.SeverityCritical, OccurredAt: now, TriggerEventID: eventID,
	}); err != nil {
		t.Fatal(err)
	}
	assertIncidentRule(t, harness, incident.KindKnownLogError, "known-startup-log")
}

func assertIncidentRule(t *testing.T, harness systemServiceHarness, kind incident.Kind, ruleID string) {
	t.Helper()
	repository, _ := storage.NewIncidentRepository(harness.database)
	record := awaitIncidentKind(t, repository, harness.workspace.ID, kind)
	if len(record.Context.Evidence) == 0 {
		t.Fatalf("%s Incident has no traceable evidence", kind)
	}
	analyses, err := repository.ListAnalyses(context.Background(), record.ID, 20)
	if err != nil || len(analyses) == 0 {
		t.Fatalf("%s analyses = (%#v, %v)", kind, analyses, err)
	}
	var payload struct {
		Results []incident.RuleResult `json:"results"`
	}
	if err := json.Unmarshal(analyses[len(analyses)-1].Result, &payload); err != nil {
		t.Fatal(err)
	}
	for _, result := range payload.Results {
		if result.RuleID == ruleID {
			assertSuggestionsAreReadOnly(t, result)
			return
		}
	}
	t.Fatalf("%s analysis did not contain rule %q: %#v", kind, ruleID, payload.Results)
}

func awaitIncidentKind(t *testing.T, repository *storage.IncidentRepository, workspaceID domain.WorkspaceID, kind incident.Kind) incident.Record {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		records, err := repository.List(context.Background(), workspaceID, 20)
		if err != nil {
			t.Fatal(err)
		}
		for _, record := range records {
			if record.Context.Kind == kind {
				return record
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("Incident kind %q was not persisted", kind)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func assertSuggestionsAreReadOnly(t *testing.T, result incident.RuleResult) {
	t.Helper()
	if len(result.Evidence) == 0 || len(result.Suggestions) == 0 {
		t.Fatalf("rule %q omitted evidence or suggestions", result.RuleID)
	}
	for _, suggestion := range result.Suggestions {
		if suggestion.Automatic {
			t.Fatalf("rule %q exposed automatic high-risk action", result.RuleID)
		}
	}
}

func writeProcessReadinessManifest(t *testing.T, root string) {
	t.Helper()
	for _, directory := range []string{"backend", ".stackpilot"} {
		if err := os.Mkdir(filepath.Join(root, directory), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	contents := `apiVersion: stackpilot.io/v1alpha1
kind: System
metadata: {id: fixture, name: Fixture}
spec:
  services:
    backend:
      driver: process
      mode: daemon
      runner: java
      workingDirectory: backend
      arguments: ["-version"]
      stop: {gracefulTimeout: 1s}
      readiness: {type: process, timeout: 3s, interval: 100ms}
`
	if err := os.WriteFile(filepath.Join(root, ".stackpilot", "system.yaml"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writePortConflictManifest(t *testing.T, root string, port int) {
	t.Helper()
	for _, directory := range []string{"backend", ".stackpilot"} {
		if err := os.Mkdir(filepath.Join(root, directory), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	contents := fmt.Sprintf(`apiVersion: stackpilot.io/v1alpha1
kind: System
metadata: {id: fixture, name: Fixture}
spec:
  ports:
    backend: {protocol: tcp, preferred: %d, conflictPolicy: strict, exposure: loopback}
  services:
    backend:
      driver: process
      mode: daemon
      runner: java
      workingDirectory: backend
      arguments: ["--port", "${ports.backend}"]
      stop: {gracefulTimeout: 1s}
      readiness: {type: tcp, host: 127.0.0.1, port: "${ports.backend}", timeout: 3s, interval: 100ms}
`, port)
	if err := os.WriteFile(filepath.Join(root, ".stackpilot", "system.yaml"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}
