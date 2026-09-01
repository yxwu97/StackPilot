package orchestrator_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"stackpilot/internal/changeplan"
	"stackpilot/internal/domain"
	"stackpilot/internal/health"
	"stackpilot/internal/incident"
	"stackpilot/internal/orchestrator"
	"stackpilot/internal/storage"
)

func TestVerifiedRestartUsesPlanAndPersistsSuccessfulObservation(t *testing.T) {
	harness := newSystemServiceHarness(t, immediateReadiness{})
	start := submitSystemHarnessStart(t, harness, "verified-start")
	awaitOperation(t, harness.service, start.Operation.ID, domain.OperationSucceeded)
	waitForVerificationCoverage(t, harness)
	plan := createVerifiedRestartPlan(t, harness, "verified-plan")
	before, _ := harness.service.Status(context.Background(), harness.workspace.ID)

	created, err := harness.service.SubmitVerifiedRestart(context.Background(), orchestrator.VerifiedRestartInput{
		WorkspaceID: harness.workspace.ID, SystemID: harness.workspace.SystemID, ChangePlanID: plan.Record.ID,
		IdempotencySubject: "test-user", IdempotencyKey: "verified-restart", Request: []byte(`{"changePlanId":"` + plan.Record.ID.String() + `"}`),
	})
	if err != nil {
		t.Fatalf("SubmitVerifiedRestart() error = %v", err)
	}
	operation := awaitOperation(t, harness.service, created.Operation.ID, domain.OperationSucceeded)
	if operation.Type != domain.OperationVerifiedRestart || operation.Steps[len(operation.Steps)-1].DetailRef != string(domain.VerificationPassed) {
		t.Fatalf("verified restart Operation = %#v", operation)
	}
	after, _ := harness.service.Status(context.Background(), harness.workspace.ID)
	if before.System == nil || after.System == nil || before.System.ID == after.System.ID || after.System.State != domain.SystemRunning {
		t.Fatalf("verified restart instances = before %#v after %#v", before.System, after.System)
	}
	started, stopped := harness.driver.serviceOrder()
	if !reflect.DeepEqual(started, []string{"backend", "web", "backend", "web"}) || !reflect.DeepEqual(stopped, []string{"web", "backend"}) {
		t.Fatalf("verified restart order = start %#v, stop %#v", started, stopped)
	}
}

func TestVerifiedRestartRejectsStalePlanBeforeLifecycle(t *testing.T) {
	harness := newSystemServiceHarness(t, immediateReadiness{})
	start := submitSystemHarnessStart(t, harness, "stale-verified-start")
	awaitOperation(t, harness.service, start.Operation.ID, domain.OperationSucceeded)
	waitForVerificationCoverage(t, harness)
	plan := createVerifiedRestartPlan(t, harness, "stale-verified-plan")
	startedBefore, stoppedBefore := harness.driver.serviceOrder()
	manifestPath := filepath.Join(harness.workspace.CanonicalPath, ".stackpilot", "system.yaml")
	contents, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	contents = []byte(strings.Replace(string(contents), "name: Fixture", "name: Changed Fixture", 1))
	if err := os.WriteFile(manifestPath, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := harness.manager.Refresh(context.Background(), harness.workspace.ID); err != nil {
		t.Fatal(err)
	}
	created, err := harness.service.SubmitVerifiedRestart(context.Background(), orchestrator.VerifiedRestartInput{
		WorkspaceID: harness.workspace.ID, SystemID: harness.workspace.SystemID, ChangePlanID: plan.Record.ID,
		IdempotencySubject: "test-user", IdempotencyKey: "stale-verified-restart", Request: []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("SubmitVerifiedRestart() error = %v", err)
	}
	operation := awaitOperation(t, harness.service, created.Operation.ID, domain.OperationFailed)
	if operation.ErrorCode != "CHANGE_PLAN_STALE" {
		t.Fatalf("stale verified restart code = %q", operation.ErrorCode)
	}
	startedAfter, stoppedAfter := harness.driver.serviceOrder()
	if !reflect.DeepEqual(startedAfter, startedBefore) || !reflect.DeepEqual(stoppedAfter, stoppedBefore) {
		t.Fatalf("stale plan lifecycle changed: start %#v -> %#v stop %#v -> %#v", startedBefore, startedAfter, stoppedBefore, stoppedAfter)
	}
}

func TestVerifiedRestartRejectsBlockedPlanAtSubmission(t *testing.T) {
	harness := newSystemServiceHarness(t, immediateReadiness{})
	start := submitSystemHarnessStart(t, harness, "blocked-verified-start")
	awaitOperation(t, harness.service, start.Operation.ID, domain.OperationSucceeded)
	waitForVerificationCoverage(t, harness)
	manifestPath := filepath.Join(harness.workspace.CanonicalPath, ".stackpilot", "system.yaml")
	contents, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	contents = []byte(strings.Replace(string(contents), "      liveness: {type: tcp, host: 127.0.0.1, port: \"${ports.backend}\", timeout: 3s, interval: 100ms}\n", "", 1))
	if err := os.WriteFile(manifestPath, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := harness.manager.Refresh(context.Background(), harness.workspace.ID); err != nil {
		t.Fatal(err)
	}
	plan := createVerifiedRestartPlan(t, harness, "blocked-verified-plan")
	if plan.Record.State != domain.ChangePlanBlocked {
		t.Fatalf("ChangePlan state = %s, want blocked", plan.Record.State)
	}
	_, err = harness.service.SubmitVerifiedRestart(context.Background(), orchestrator.VerifiedRestartInput{
		WorkspaceID: harness.workspace.ID, SystemID: harness.workspace.SystemID, ChangePlanID: plan.Record.ID,
		IdempotencySubject: "test-user", IdempotencyKey: "blocked-verified-restart", Request: []byte(`{}`),
	})
	if !errors.Is(err, orchestrator.ErrChangePlanBlocked) {
		t.Fatalf("SubmitVerifiedRestart() error = %v, want ErrChangePlanBlocked", err)
	}
}

func TestVerifiedRestartFailureKeepsNewRuntimeWithoutRollback(t *testing.T) {
	harness := newSystemServiceHarness(t, immediateReadiness{})
	start := submitSystemHarnessStart(t, harness, "failed-observation-start")
	awaitOperation(t, harness.service, start.Operation.ID, domain.OperationSucceeded)
	waitForVerificationCoverage(t, harness)
	plan := createVerifiedRestartPlan(t, harness, "failed-observation-plan")
	harness.liveness.configure(true, false)
	created := submitVerifiedRestartForTest(t, harness, plan.Record.ID, "failed-observation-restart")
	operation := awaitOperation(t, harness.service, created.Operation.ID, domain.OperationFailed)
	if operation.ErrorCode != "VERIFICATION_FAILED" {
		t.Fatalf("verification failure code = %q", operation.ErrorCode)
	}
	status, _ := harness.service.Status(context.Background(), harness.workspace.ID)
	if status.System == nil || status.System.State != domain.SystemRunning {
		t.Fatalf("verification failure rolled back or stopped new runtime: %#v", status.System)
	}
	repository, err := storage.NewIncidentRepository(harness.database)
	if err != nil {
		t.Fatal(err)
	}
	records := waitForVerificationIncident(t, repository, harness.workspace.ID)
	if len(records) != 1 || records[0].Kind() != incident.KindVerification ||
		records[0].Context.OperationID != operation.ID || records[0].Context.ChangePlanID != plan.Record.ID ||
		records[0].Context.RevisionID != plan.To.ID || records[0].Context.SystemInstanceID != status.System.ID ||
		!containsIncidentEvidence(records[0].Context.Evidence, "event") || !containsIncidentEvidence(records[0].Context.Evidence, "health") {
		t.Fatalf("verification Incident = %#v", records)
	}
}

func waitForVerificationIncident(t *testing.T, repository *storage.IncidentRepository, workspaceID domain.WorkspaceID) []incident.Record {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		records, err := repository.List(context.Background(), workspaceID, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(records) > 0 {
			return records
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("verification Incident was not persisted")
	return nil
}

func containsIncidentEvidence(values []incident.EvidenceRef, kind string) bool {
	for _, value := range values {
		if value.Type == kind {
			return true
		}
	}
	return false
}

func TestVerifiedRestartCancellationDuringObservationKeepsNewRuntime(t *testing.T) {
	harness := newSystemServiceHarness(t, immediateReadiness{})
	start := submitSystemHarnessStart(t, harness, "cancel-observation-start")
	awaitOperation(t, harness.service, start.Operation.ID, domain.OperationSucceeded)
	waitForVerificationCoverage(t, harness)
	plan := createVerifiedRestartPlan(t, harness, "cancel-observation-plan")
	harness.liveness.configure(false, false)
	created := submitVerifiedRestartForTest(t, harness, plan.Record.ID, "cancel-observation-restart")
	waitForOperationStep(t, harness, created.Operation.ID, "stability-observation", domain.OperationStepRunning)
	if _, err := harness.service.CancelOperation(context.Background(), created.Operation.ID); err != nil {
		t.Fatalf("CancelOperation() error = %v", err)
	}
	awaitOperation(t, harness.service, created.Operation.ID, domain.OperationCancelled)
	status, _ := harness.service.Status(context.Background(), harness.workspace.ID)
	if status.System == nil || status.System.State != domain.SystemRunning {
		t.Fatalf("cancelled observation did not preserve new runtime: %#v", status.System)
	}
}

func TestVerifiedRestartReadinessFailureUsesExistingStartFailureState(t *testing.T) {
	harness := newSystemServiceHarness(t, &sequenceReadiness{outcomes: []bool{true, true, true, false}})
	start := submitSystemHarnessStart(t, harness, "readiness-failure-start")
	awaitOperation(t, harness.service, start.Operation.ID, domain.OperationSucceeded)
	waitForVerificationCoverage(t, harness)
	plan := createVerifiedRestartPlan(t, harness, "readiness-failure-plan")
	created := submitVerifiedRestartForTest(t, harness, plan.Record.ID, "readiness-failure-restart")
	operation := awaitOperation(t, harness.service, created.Operation.ID, domain.OperationFailed)
	if operation.ErrorCode != string(health.CodeReadinessTimeout) {
		t.Fatalf("verified restart readiness code = %q", operation.ErrorCode)
	}
	status, _ := harness.service.Status(context.Background(), harness.workspace.ID)
	if status.System == nil || status.System.State != domain.SystemFailed {
		t.Fatalf("verified restart readiness state = %#v", status.System)
	}
}

func TestVerifiedRestartHoldsWorkspaceOperationLockDuringObservation(t *testing.T) {
	harness := newSystemServiceHarness(t, immediateReadiness{})
	start := submitSystemHarnessStart(t, harness, "locked-observation-start")
	awaitOperation(t, harness.service, start.Operation.ID, domain.OperationSucceeded)
	waitForVerificationCoverage(t, harness)
	plan := createVerifiedRestartPlan(t, harness, "locked-observation-plan")
	harness.liveness.configure(false, false)
	created := submitVerifiedRestartForTest(t, harness, plan.Record.ID, "locked-observation-restart")
	waitForOperationStep(t, harness, created.Operation.ID, "stability-observation", domain.OperationStepRunning)
	_, err := harness.service.SubmitRestart(context.Background(), orchestrator.RestartSystemInput{
		WorkspaceID: harness.workspace.ID, SystemID: harness.workspace.SystemID,
		IdempotencySubject: "test-user", IdempotencyKey: "concurrent-restart", Request: []byte(`{}`),
	})
	if !errors.Is(err, orchestrator.ErrOperationAlreadyActive) {
		t.Fatalf("concurrent restart error = %v, want ErrOperationAlreadyActive", err)
	}
	if _, err := harness.service.CancelOperation(context.Background(), created.Operation.ID); err != nil {
		t.Fatal(err)
	}
	awaitOperation(t, harness.service, created.Operation.ID, domain.OperationCancelled)
}

func createVerifiedRestartPlan(t *testing.T, harness systemServiceHarness, key string) *changeplan.Plan {
	t.Helper()
	created, err := harness.service.SubmitChangePlan(context.Background(), orchestrator.ChangePlanInput{
		WorkspaceID: harness.workspace.ID, SystemID: harness.workspace.SystemID,
		IdempotencySubject: "test-user", IdempotencyKey: key, Request: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	operation := awaitOperation(t, harness.service, created.Operation.ID, domain.OperationSucceeded)
	planID, err := domain.ParseChangePlanID(operation.Steps[4].DetailRef)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := harness.service.GetChangePlan(context.Background(), planID)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func submitVerifiedRestartForTest(t *testing.T, harness systemServiceHarness, planID domain.ChangePlanID, key string) *orchestrator.CreateResult {
	t.Helper()
	created, err := harness.service.SubmitVerifiedRestart(context.Background(), orchestrator.VerifiedRestartInput{
		WorkspaceID: harness.workspace.ID, SystemID: harness.workspace.SystemID, ChangePlanID: planID,
		IdempotencySubject: "test-user", IdempotencyKey: key, Request: []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("SubmitVerifiedRestart() error = %v", err)
	}
	return created
}

func waitForOperationStep(t *testing.T, harness systemServiceHarness, operationID domain.OperationID, key string, state domain.OperationStepState) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		operation, err := harness.service.GetOperation(context.Background(), operationID)
		if err == nil {
			for _, step := range operation.Steps {
				if step.Key == key && step.State == state {
					return
				}
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("operation %s step %s did not reach %s", operationID, key, state)
}

func waitForVerificationCoverage(t *testing.T, harness systemServiceHarness) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status, err := harness.service.Status(context.Background(), harness.workspace.ID)
		if err == nil && len(status.HealthCoverage) == len(status.Services) {
			complete := true
			for _, runtime := range status.Services {
				complete = complete && status.HealthCoverage[runtime.ID].SatisfiesVerification
			}
			if complete {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("liveness coverage did not become verification-ready")
}
