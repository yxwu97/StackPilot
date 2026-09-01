package orchestrator_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"stackpilot/internal/domain"
	"stackpilot/internal/orchestrator"
	"stackpilot/internal/runner"
)

func TestChangePlanOperationPersistsFiveReadOnlySteps(t *testing.T) {
	harness := newSystemServiceHarness(t, immediateReadiness{})
	start := submitSystemHarnessStart(t, harness, "change-plan-start")
	awaitOperation(t, harness.service, start.Operation.ID, domain.OperationSucceeded)
	startedBefore, stoppedBefore := harness.driver.serviceOrder()
	portsBefore := countRows(t, harness, "port_leases")

	created, err := harness.service.SubmitChangePlan(context.Background(), orchestrator.ChangePlanInput{
		WorkspaceID: harness.workspace.ID, SystemID: harness.workspace.SystemID,
		IdempotencySubject: "test-user", IdempotencyKey: "change-plan-success", Request: []byte(`{"workspaceId":"fixture"}`),
	})
	if err != nil {
		t.Fatalf("SubmitChangePlan() error = %v", err)
	}
	operation := awaitOperation(t, harness.service, created.Operation.ID, domain.OperationSucceeded)
	wantKeys := []string{"collect-running", "collect-workspace", "compare", "classify-risk", "persist-plan"}
	if len(operation.Steps) != len(wantKeys) {
		t.Fatalf("ChangePlan steps = %#v", operation.Steps)
	}
	for index, step := range operation.Steps {
		if step.Key != wantKeys[index] || step.State != domain.OperationStepSucceeded {
			t.Fatalf("step %d = %#v, want %q succeeded", index+1, step, wantKeys[index])
		}
	}
	planID, err := domain.ParseChangePlanID(operation.Steps[4].DetailRef)
	if err != nil {
		t.Fatalf("persist-plan detailRef = %q: %v", operation.Steps[4].DetailRef, err)
	}
	plan, err := harness.service.GetChangePlan(context.Background(), planID)
	if err != nil || plan.Record.CreatedByOperationID != operation.ID || plan.Record.State != domain.ChangePlanReady {
		t.Fatalf("GetChangePlan() = %#v, %v", plan, err)
	}
	startedAfter, stoppedAfter := harness.driver.serviceOrder()
	if !reflect.DeepEqual(startedAfter, startedBefore) || !reflect.DeepEqual(stoppedAfter, stoppedBefore) ||
		countRows(t, harness, "port_leases") != portsBefore {
		t.Fatalf("ChangePlan caused lifecycle/port side effects: starts=%#v stops=%#v ports=%d", startedAfter, stoppedAfter, countRows(t, harness, "port_leases"))
	}
}

func TestBlockedChangePlanStillPersistsAsSuccessfulReadOnlyOperation(t *testing.T) {
	harness := newSystemServiceHarness(t, immediateReadiness{})
	start := submitSystemHarnessStart(t, harness, "blocked-plan-start")
	awaitOperation(t, harness.service, start.Operation.ID, domain.OperationSucceeded)
	manifestPath := filepath.Join(harness.workspace.CanonicalPath, ".stackpilot", "system.yaml")
	encoded, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	withoutLiveness := removeManifestLines(string(encoded), "      liveness:")
	if err := os.WriteFile(manifestPath, []byte(withoutLiveness), 0o600); err != nil {
		t.Fatal(err)
	}
	startedBefore, stoppedBefore := harness.driver.serviceOrder()

	created, err := harness.service.SubmitChangePlan(context.Background(), orchestrator.ChangePlanInput{
		WorkspaceID: harness.workspace.ID, SystemID: harness.workspace.SystemID,
		IdempotencySubject: "test-user", IdempotencyKey: "change-plan-blocked", Request: []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("SubmitChangePlan() error = %v", err)
	}
	operation := awaitOperation(t, harness.service, created.Operation.ID, domain.OperationSucceeded)
	planID, err := domain.ParseChangePlanID(operation.Steps[4].DetailRef)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := harness.service.GetChangePlan(context.Background(), planID)
	if err != nil || plan.Record.State != domain.ChangePlanBlocked || plan.Record.BlockedCount == 0 {
		t.Fatalf("blocked ChangePlan = %#v, %v", plan, err)
	}
	startedAfter, stoppedAfter := harness.driver.serviceOrder()
	if !reflect.DeepEqual(startedAfter, startedBefore) || !reflect.DeepEqual(stoppedAfter, stoppedBefore) {
		t.Fatalf("blocked ChangePlan caused lifecycle side effects: starts=%#v stops=%#v", startedAfter, stoppedAfter)
	}
}

func TestChangePlanCancellationDuringCollectionHasNoLifecycleSideEffects(t *testing.T) {
	controlled := newControlledRunner(2)
	harness := newSystemServiceHarnessWithRunner(t, immediateReadiness{}, func(root string) {
		first, second := distinctAvailablePorts(t)
		writeSystemManifest(t, root, first, second)
	}, controlled)
	start := submitSystemHarnessStart(t, harness, "cancel-plan-start")
	awaitOperation(t, harness.service, start.Operation.ID, domain.OperationSucceeded)
	startedBefore, stoppedBefore := harness.driver.serviceOrder()
	created, err := harness.service.SubmitChangePlan(context.Background(), orchestrator.ChangePlanInput{
		WorkspaceID: harness.workspace.ID, SystemID: harness.workspace.SystemID,
		IdempotencySubject: "test-user", IdempotencyKey: "cancel-change-plan", Request: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	<-controlled.entered
	if _, err := harness.service.CancelOperation(context.Background(), created.Operation.ID); err != nil {
		t.Fatalf("CancelOperation() error = %v", err)
	}
	operation := awaitOperation(t, harness.service, created.Operation.ID, domain.OperationCancelled)
	if operation.Steps[1].State != domain.OperationStepCancelled {
		t.Fatalf("cancelled ChangePlan steps = %#v", operation.Steps)
	}
	startedAfter, stoppedAfter := harness.driver.serviceOrder()
	if !reflect.DeepEqual(startedAfter, startedBefore) || !reflect.DeepEqual(stoppedAfter, stoppedBefore) {
		t.Fatalf("cancelled ChangePlan caused lifecycle side effects: starts=%#v stops=%#v", startedAfter, stoppedAfter)
	}
}

func TestChangePlanRejectsManifestChangeDuringCollection(t *testing.T) {
	controlled := newControlledRunner(2)
	harness := newSystemServiceHarnessWithRunner(t, immediateReadiness{}, func(root string) {
		first, second := distinctAvailablePorts(t)
		writeSystemManifest(t, root, first, second)
	}, controlled)
	start := submitSystemHarnessStart(t, harness, "stale-plan-start")
	awaitOperation(t, harness.service, start.Operation.ID, domain.OperationSucceeded)
	created, err := harness.service.SubmitChangePlan(context.Background(), orchestrator.ChangePlanInput{
		WorkspaceID: harness.workspace.ID, SystemID: harness.workspace.SystemID,
		IdempotencySubject: "test-user", IdempotencyKey: "stale-change-plan", Request: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	<-controlled.entered
	manifestPath := filepath.Join(harness.workspace.CanonicalPath, ".stackpilot", "system.yaml")
	encoded, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	changed := strings.Replace(string(encoded), "name: Fixture", "name: Changed Fixture", 1)
	if err := os.WriteFile(manifestPath, []byte(changed), 0o600); err != nil {
		t.Fatal(err)
	}
	close(controlled.release)
	operation := awaitOperation(t, harness.service, created.Operation.ID, domain.OperationFailed)
	if operation.ErrorCode != "CHANGE_PLAN_STALE" || operation.Steps[1].ErrorCode != "CHANGE_PLAN_STALE" {
		t.Fatalf("stale ChangePlan operation = %#v", operation)
	}
	var count int
	if err := harness.database.QueryRow(`SELECT COUNT(*) FROM change_plans WHERE created_by_operation_id=?`, created.Operation.ID.String()).Scan(&count); err != nil || count != 0 {
		t.Fatalf("stale ChangePlan persisted mixed result: count=%d error=%v", count, err)
	}
}

func TestChangePlanCollectionDeadlineFailsWithoutLifecycleSideEffects(t *testing.T) {
	runnerValue := newControlledRunner(2)
	serviceContext, cancel := context.WithCancelCause(context.Background())
	harness := newSystemServiceHarnessWithContext(t, serviceContext, immediateReadiness{}, func(root string) {
		first, second := distinctAvailablePorts(t)
		writeSystemManifest(t, root, first, second)
	}, runnerValue)
	start := submitSystemHarnessStart(t, harness, "timeout-plan-start")
	awaitOperation(t, harness.service, start.Operation.ID, domain.OperationSucceeded)
	startedBefore, stoppedBefore := harness.driver.serviceOrder()
	created, err := harness.service.SubmitChangePlan(context.Background(), orchestrator.ChangePlanInput{
		WorkspaceID: harness.workspace.ID, SystemID: harness.workspace.SystemID,
		IdempotencySubject: "test-user", IdempotencyKey: "timeout-change-plan", Request: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	<-runnerValue.entered
	cancel(context.DeadlineExceeded)
	operation := awaitOperation(t, harness.service, created.Operation.ID, domain.OperationFailed)
	if operation.ErrorCode != "REVISION_SOURCE_UNAVAILABLE" {
		t.Fatalf("deadline ChangePlan operation = %#v", operation)
	}
	startedAfter, stoppedAfter := harness.driver.serviceOrder()
	if !reflect.DeepEqual(startedAfter, startedBefore) || !reflect.DeepEqual(stoppedAfter, stoppedBefore) {
		t.Fatalf("timed out ChangePlan caused lifecycle side effects: starts=%#v stops=%#v", startedAfter, stoppedAfter)
	}
}

type controlledRunner struct {
	mutex   sync.Mutex
	allowed int
	calls   int
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func newControlledRunner(allowed int) *controlledRunner {
	return &controlledRunner{allowed: allowed, entered: make(chan struct{}), release: make(chan struct{})}
}

func distinctAvailablePorts(t *testing.T) (int, int) {
	t.Helper()
	first, second := availablePort(t), availablePort(t)
	for first == second {
		second = availablePort(t)
	}
	return first, second
}

func (value *controlledRunner) Resolve(ctx context.Context, request runner.ResolveRequest) (*runner.ResolvedCommand, error) {
	value.mutex.Lock()
	value.calls++
	blocked := value.calls > value.allowed
	value.mutex.Unlock()
	if !blocked {
		return fakeRunner{}.Resolve(ctx, request)
	}
	value.once.Do(func() { close(value.entered) })
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-value.release:
		return fakeRunner{}.Resolve(ctx, request)
	}
}

func countRows(t *testing.T, harness systemServiceHarness, table string) int {
	t.Helper()
	allowed := map[string]bool{"port_leases": true}
	if !allowed[table] {
		t.Fatalf("unsupported test table %q", table)
	}
	var count int
	if err := harness.database.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return count
}

func removeManifestLines(value, prefix string) string {
	lines := strings.Split(value, "\n")
	kept := lines[:0]
	for _, line := range lines {
		if !strings.HasPrefix(line, prefix) {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}
