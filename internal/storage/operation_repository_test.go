package storage

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"stackpilot/internal/domain"
	"stackpilot/internal/orchestrator"
)

const (
	testWorkspaceID = domain.WorkspaceID("ws_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	testSystemID    = domain.SystemID("sample")
)

func TestOperationManagerCreatesQueuedOperationWithOrderedSteps(t *testing.T) {
	database := openTestDatabase(t)
	seedOperationWorkspace(t, database, testWorkspaceID, testSystemID)
	manager := newOperationManager(t, database)

	result, err := manager.Create(context.Background(), operationInput("create-1", []byte(`{"action":"start"}`)))
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if !result.Created || result.Operation.State != domain.OperationQueued {
		t.Fatalf("Create() result = %#v, want newly queued Operation", result)
	}
	wantKeys := []string{"validate-manifest", "preflight-runners", "start:backend"}
	if len(result.Operation.Steps) != len(wantKeys) {
		t.Fatalf("step count = %d, want %d", len(result.Operation.Steps), len(wantKeys))
	}
	for index, step := range result.Operation.Steps {
		if step.Number != index+1 || step.Key != wantKeys[index] || step.State != domain.OperationStepPending || step.Attempt != 0 {
			t.Errorf("step %d = %#v", index, step)
		}
	}
}

func TestOperationIdempotencyReplayAndReuse(t *testing.T) {
	database := openTestDatabase(t)
	seedOperationWorkspace(t, database, testWorkspaceID, testSystemID)
	manager := newOperationManager(t, database)
	input := operationInput("same-key", []byte(`{"action":"start"}`))

	first, err := manager.Create(context.Background(), input)
	if err != nil {
		t.Fatalf("first Create() error = %v", err)
	}
	replay, err := manager.Create(context.Background(), input)
	if err != nil {
		t.Fatalf("replay Create() error = %v", err)
	}
	if replay.Created || replay.Operation.ID != first.Operation.ID {
		t.Fatalf("replay = %#v, want original %s", replay, first.Operation.ID)
	}
	var eventCount int
	if err := database.QueryRow(`SELECT COUNT(*) FROM events WHERE operation_id = ?`, first.Operation.ID.String()).Scan(&eventCount); err != nil || eventCount != 1 {
		t.Fatalf("idempotent event count = %d, error = %v", eventCount, err)
	}

	input.Request = []byte(`{"action":"start","changed":true}`)
	if _, err := manager.Create(context.Background(), input); !errors.Is(err, orchestrator.ErrIdempotencyKeyReused) {
		t.Fatalf("changed Create() error = %v, want ErrIdempotencyKeyReused", err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM events WHERE operation_id = ?`, first.Operation.ID.String()).Scan(&eventCount); err != nil || eventCount != 1 {
		t.Fatalf("reused-key event count = %d, error = %v", eventCount, err)
	}
}

func TestOperationWorkspaceLockReleasedAtTerminalState(t *testing.T) {
	database := openTestDatabase(t)
	seedOperationWorkspace(t, database, testWorkspaceID, testSystemID)
	manager := newOperationManager(t, database)
	first, err := manager.Create(context.Background(), operationInput("lock-1", []byte(`{"request":1}`)))
	if err != nil {
		t.Fatalf("first Create() error = %v", err)
	}
	if _, err := manager.Create(context.Background(), operationInput("lock-2", []byte(`{"request":2}`))); !errors.Is(err, orchestrator.ErrOperationAlreadyActive) {
		t.Fatalf("concurrent Create() error = %v, want ErrOperationAlreadyActive", err)
	}
	if _, err := manager.Start(context.Background(), first.Operation.ID); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if _, err := manager.Succeed(context.Background(), first.Operation.ID); err != nil {
		t.Fatalf("Succeed() error = %v", err)
	}
	if _, err := manager.Create(context.Background(), operationInput("lock-2", []byte(`{"request":2}`))); err != nil {
		t.Fatalf("Create() after terminal error = %v", err)
	}
}

func TestOperationCancellationSemantics(t *testing.T) {
	database := openTestDatabase(t)
	seedOperationWorkspace(t, database, testWorkspaceID, testSystemID)
	manager := newOperationManager(t, database)

	queued, err := manager.Create(context.Background(), operationInput("cancel-queued", []byte(`{"request":1}`)))
	if err != nil {
		t.Fatalf("create queued Operation: %v", err)
	}
	cancelled, err := manager.RequestCancel(context.Background(), queued.Operation.ID)
	if err != nil {
		t.Fatalf("cancel queued Operation: %v", err)
	}
	if cancelled.State != domain.OperationCancelled || cancelled.CancelRequestedAt == nil || cancelled.FinishedAt == nil {
		t.Fatalf("cancelled Operation = %#v", cancelled)
	}

	running, err := manager.Create(context.Background(), operationInput("cancel-running", []byte(`{"request":2}`)))
	if err != nil {
		t.Fatalf("create running Operation: %v", err)
	}
	if runningOperation, err := manager.Start(context.Background(), running.Operation.ID); err != nil || runningOperation.State != domain.OperationRunning {
		t.Fatalf("Start() = (%#v, %v)", runningOperation, err)
	}
	cancelling, err := manager.RequestCancel(context.Background(), running.Operation.ID)
	if err != nil || cancelling.State != domain.OperationCancelling || cancelling.CancelRequestedAt == nil {
		t.Fatalf("RequestCancel() = (%#v, %v)", cancelling, err)
	}
	if repeated, err := manager.RequestCancel(context.Background(), running.Operation.ID); err != nil || repeated.State != domain.OperationCancelling {
		t.Fatalf("repeated RequestCancel() = (%#v, %v)", repeated, err)
	}
	if _, err := manager.Fail(context.Background(), running.Operation.ID, "CANCEL_COMPENSATION_FAILED"); err != nil {
		t.Fatalf("Fail(cancelling) error = %v", err)
	}

	stopInput := operationInput("stop", []byte(`{"action":"stop"}`))
	stopInput.Type = domain.OperationStop
	stopOperation, err := manager.Create(context.Background(), stopInput)
	if err != nil {
		t.Fatalf("create stop Operation: %v", err)
	}
	if stopOperation.Operation.Cancellable {
		t.Fatal("stop Operation unexpectedly cancellable")
	}
	if _, err := manager.RequestCancel(context.Background(), stopOperation.Operation.ID); !errors.Is(err, orchestrator.ErrNotCancellable) {
		t.Fatalf("cancel stop Operation error = %v, want ErrNotCancellable", err)
	}
}

func TestOperationRecoveryFailsInterruptedWorkAndReleasesLock(t *testing.T) {
	database := openTestDatabase(t)
	seedOperationWorkspace(t, database, testWorkspaceID, testSystemID)
	manager := newOperationManager(t, database)

	queued, err := manager.Create(context.Background(), operationInput("recover-queued", []byte(`{"request":1}`)))
	if err != nil {
		t.Fatalf("create queued Operation: %v", err)
	}
	ids, err := manager.RecoverInterrupted(context.Background())
	if err != nil || len(ids) != 1 || ids[0] != queued.Operation.ID {
		t.Fatalf("RecoverInterrupted(queued) = (%v, %v)", ids, err)
	}
	assertRecoveredOperation(t, manager, queued.Operation.ID, false)

	running, err := manager.Create(context.Background(), operationInput("recover-running", []byte(`{"request":2}`)))
	if err != nil {
		t.Fatalf("create running Operation: %v", err)
	}
	if _, err := manager.Start(context.Background(), running.Operation.ID); err != nil {
		t.Fatalf("start Operation: %v", err)
	}
	if _, err := manager.TransitionStep(context.Background(), running.Operation.ID, 1, domain.OperationStepRunning, "", ""); err != nil {
		t.Fatalf("start recovery step: %v", err)
	}
	ids, err = manager.RecoverInterrupted(context.Background())
	if err != nil || len(ids) != 1 || ids[0] != running.Operation.ID {
		t.Fatalf("RecoverInterrupted(running) = (%v, %v)", ids, err)
	}
	assertRecoveredOperation(t, manager, running.Operation.ID, true)
	if ids, err := manager.RecoverInterrupted(context.Background()); err != nil || len(ids) != 0 {
		t.Fatalf("repeated RecoverInterrupted() = (%v, %v)", ids, err)
	}
}

func assertRecoveredOperation(t *testing.T, manager *orchestrator.Manager, id domain.OperationID, hadRunningStep bool) {
	t.Helper()
	operation, err := manager.Get(context.Background(), id)
	if err != nil || operation.State != domain.OperationFailed || operation.ErrorCode != "CONTROL_PLANE_RESTARTED" {
		t.Fatalf("recovered Operation = (%#v, %v)", operation, err)
	}
	for index, step := range operation.Steps {
		want := domain.OperationStepSkipped
		if hadRunningStep && index == 0 {
			want = domain.OperationStepFailed
			if step.ErrorCode != "CONTROL_PLANE_RESTARTED" {
				t.Fatalf("recovered running step code = %q", step.ErrorCode)
			}
		}
		if step.State != want {
			t.Fatalf("recovered step %d = %#v, want %s", index, step, want)
		}
	}
}

func TestOperationAndStepTransitions(t *testing.T) {
	database := openTestDatabase(t)
	seedOperationWorkspace(t, database, testWorkspaceID, testSystemID)
	repository := newOperationRepository(t, database)
	createdAt := time.Date(2026, 8, 18, 1, 0, 0, 0, time.UTC)
	operation := fixedOperation("op_01ARZ3NDEKTSV4RRFFQ69G5FAV", "steps", createdAt)
	created, err := repository.Create(context.Background(), orchestrator.CreateCommand{
		Operation: operation, StepKeys: []string{"preflight", "start"},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if _, err := repository.Transition(context.Background(), created.Operation.ID, domain.OperationSucceeded, "", createdAt); !errors.Is(err, orchestrator.ErrInvalidTransition) {
		t.Fatalf("queued -> succeeded error = %v, want ErrInvalidTransition", err)
	}

	runningAt := createdAt.Add(time.Second)
	current, err := repository.TransitionStep(context.Background(), created.Operation.ID, 1, domain.OperationStepRunning, "", "", runningAt)
	if err != nil || current.Steps[0].Attempt != 1 {
		t.Fatalf("start step = (%#v, %v)", current, err)
	}
	failedAt := runningAt.Add(1500 * time.Millisecond)
	current, err = repository.TransitionStep(context.Background(), created.Operation.ID, 1, domain.OperationStepFailed, "RUNNER_FAILED", "detail-1", failedAt)
	if err != nil || current.Steps[0].DurationMillis == nil || *current.Steps[0].DurationMillis != 1500 {
		t.Fatalf("fail step = (%#v, %v)", current, err)
	}
	retryAt := failedAt.Add(time.Second)
	current, err = repository.TransitionStep(context.Background(), created.Operation.ID, 1, domain.OperationStepRunning, "ignored", "ignored", retryAt)
	if err != nil || current.Steps[0].Attempt != 2 || current.Steps[0].ErrorCode != "" || current.Steps[0].DetailRef != "" {
		t.Fatalf("retry step = (%#v, %v)", current, err)
	}
	current, err = repository.TransitionStep(context.Background(), created.Operation.ID, 1, domain.OperationStepSucceeded, "", "", retryAt.Add(time.Second))
	if err != nil || current.Steps[0].State != domain.OperationStepSucceeded {
		t.Fatalf("succeed step = (%#v, %v)", current, err)
	}
	current, err = repository.TransitionStep(context.Background(), created.Operation.ID, 2, domain.OperationStepSkipped, "", "", retryAt)
	if err != nil || current.Steps[1].DurationMillis == nil || *current.Steps[1].DurationMillis != 0 {
		t.Fatalf("skip unstarted step = (%#v, %v)", current, err)
	}
	if _, err := repository.TransitionStep(context.Background(), created.Operation.ID, 2, domain.OperationStepRunning, "", "", retryAt); !errors.Is(err, orchestrator.ErrInvalidTransition) {
		t.Fatalf("skipped -> running error = %v, want ErrInvalidTransition", err)
	}
	if _, err := repository.TransitionStep(context.Background(), created.Operation.ID, 3, domain.OperationStepRunning, "", "", retryAt); !errors.Is(err, orchestrator.ErrStepNotFound) {
		t.Fatalf("missing step error = %v, want ErrStepNotFound", err)
	}
}

func TestOperationRepositoryListFiltersOrdersAndLimits(t *testing.T) {
	database := openTestDatabase(t)
	seedOperationWorkspace(t, database, testWorkspaceID, testSystemID)
	otherWorkspace := domain.WorkspaceID("ws_01ARZ3NDEKTSV4RRFFQ69G5FAW")
	seedOperationWorkspace(t, database, otherWorkspace, domain.SystemID("other"))
	repository := newOperationRepository(t, database)
	base := time.Date(2026, 8, 18, 1, 0, 0, 0, time.UTC)

	first := fixedOperation("op_01ARZ3NDEKTSV4RRFFQ69G5FAV", "list-a", base)
	createTerminalOperation(t, repository, first, []string{"validate", "start"})
	second := fixedOperation("op_01ARZ3NDEKTSV4RRFFQ69G5FAW", "list-b", base.Add(time.Hour))
	createTerminalOperation(t, repository, second, []string{"validate", "restart"})
	third := fixedOperation("op_01ARZ3NDEKTSV4RRFFQ69G5FAX", "list-c", base.Add(2*time.Hour))
	third.WorkspaceID, third.SystemID = otherWorkspace, "other"
	createTerminalOperation(t, repository, third, []string{"validate", "stop"})

	global, err := repository.List(context.Background(), nil, 2)
	if err != nil || len(global) != 2 || global[0].ID != third.ID || global[1].ID != second.ID {
		t.Fatalf("global List() = (%+v, %v)", global, err)
	}
	scoped, err := repository.List(context.Background(), &first.WorkspaceID, 10)
	if err != nil || len(scoped) != 2 || scoped[0].ID != second.ID || scoped[1].ID != first.ID {
		t.Fatalf("scoped List() = (%+v, %v)", scoped, err)
	}
	if len(scoped[0].Steps) != 2 || scoped[0].Steps[1].Key != "restart" {
		t.Fatalf("listed steps = %+v", scoped[0].Steps)
	}
}

func createTerminalOperation(t *testing.T, repository *OperationRepository, operation orchestrator.Operation, steps []string) {
	t.Helper()
	if _, err := repository.Create(context.Background(), orchestrator.CreateCommand{Operation: operation, StepKeys: steps}); err != nil {
		t.Fatalf("create Operation %s: %v", operation.ID, err)
	}
	if _, err := repository.Transition(context.Background(), operation.ID, domain.OperationRunning, "", operation.CreatedAt.Add(time.Second)); err != nil {
		t.Fatalf("start Operation %s: %v", operation.ID, err)
	}
	if _, err := repository.Transition(context.Background(), operation.ID, domain.OperationSucceeded, "", operation.CreatedAt.Add(2*time.Second)); err != nil {
		t.Fatalf("finish Operation %s: %v", operation.ID, err)
	}
}

func TestConcurrentOperationCreateEnforcesWorkspaceLock(t *testing.T) {
	database := openTestDatabase(t)
	seedOperationWorkspace(t, database, testWorkspaceID, testSystemID)
	manager := newOperationManager(t, database)
	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for index := 0; index < 2; index++ {
		workers.Add(1)
		go func(index int) {
			defer workers.Done()
			<-start
			_, err := manager.Create(context.Background(), operationInput(
				"concurrent-"+string(rune('a'+index)), []byte{byte(index + 1)},
			))
			results <- err
		}(index)
	}
	close(start)
	workers.Wait()
	close(results)
	succeeded, conflicted := 0, 0
	for err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, orchestrator.ErrOperationAlreadyActive):
			conflicted++
		default:
			t.Fatalf("concurrent Create() error = %v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("concurrent outcomes = %d succeeded, %d conflicted", succeeded, conflicted)
	}
}

func TestOperationPersistsAcrossDatabaseReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stackpilot.db")
	database, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	seedOperationWorkspace(t, database, testWorkspaceID, testSystemID)
	manager := newOperationManager(t, database)
	created, err := manager.Create(context.Background(), operationInput("persist", []byte(`{"persist":true}`)))
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}
	database, err = Open(context.Background(), path)
	if err != nil {
		t.Fatalf("reopen database: %v", err)
	}
	defer closeDatabase(t, database)
	repository := newOperationRepository(t, database)
	reloaded, err := repository.Get(context.Background(), created.Operation.ID)
	if err != nil || reloaded.ID != created.Operation.ID || len(reloaded.Steps) != 3 {
		t.Fatalf("Get() after reopen = (%#v, %v)", reloaded, err)
	}
}

func TestExpiredIdempotencyKeyCanBeReused(t *testing.T) {
	database := openTestDatabase(t)
	seedOperationWorkspace(t, database, testWorkspaceID, testSystemID)
	repository := newOperationRepository(t, database)
	createdAt := time.Date(2026, 8, 18, 1, 0, 0, 0, time.UTC)
	first := fixedOperation("op_01ARZ3NDEKTSV4RRFFQ69G5FAV", "expiring", createdAt)
	if _, err := repository.Create(context.Background(), orchestrator.CreateCommand{Operation: first, StepKeys: []string{"start"}}); err != nil {
		t.Fatalf("create first Operation: %v", err)
	}
	if _, err := repository.Transition(context.Background(), first.ID, domain.OperationRunning, "", createdAt.Add(time.Second)); err != nil {
		t.Fatalf("start first Operation: %v", err)
	}
	if _, err := repository.Transition(context.Background(), first.ID, domain.OperationSucceeded, "", createdAt.Add(2*time.Second)); err != nil {
		t.Fatalf("finish first Operation: %v", err)
	}
	second := fixedOperation("op_01ARZ3NDEKTSV4RRFFQ69G5FAW", "expiring", createdAt.Add(25*time.Hour))
	second.RequestDigest = strings.Repeat("b", 64)
	result, err := repository.Create(context.Background(), orchestrator.CreateCommand{Operation: second, StepKeys: []string{"start"}})
	if err != nil || !result.Created || result.Operation.ID != second.ID {
		t.Fatalf("reuse expired key = (%#v, %v)", result, err)
	}
}

func operationInput(key string, request []byte) orchestrator.CreateInput {
	return orchestrator.CreateInput{
		WorkspaceID: testWorkspaceID, SystemID: testSystemID, Type: domain.OperationStart,
		IdempotencySubject: "web-session", RouteKey: "system:start", IdempotencyKey: key,
		Request: request, Cancellable: true,
		StepKeys: []string{"validate-manifest", "preflight-runners", "start:backend"},
	}
}

func fixedOperation(id, key string, createdAt time.Time) orchestrator.Operation {
	return orchestrator.Operation{
		ID: domain.OperationID(id), WorkspaceID: testWorkspaceID, SystemID: testSystemID,
		Type: domain.OperationStart, State: domain.OperationQueued,
		IdempotencySubject: "web-session", RouteKey: "system:start", IdempotencyKey: key,
		RequestDigest: strings.Repeat("a", 64), Cancellable: true, CreatedAt: createdAt,
	}
}

func newOperationManager(t *testing.T, database *sql.DB) *orchestrator.Manager {
	t.Helper()
	repository := newOperationRepository(t, database)
	manager, err := orchestrator.NewManager(repository)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	return manager
}

func newOperationRepository(t *testing.T, database *sql.DB) *OperationRepository {
	t.Helper()
	repository, err := NewOperationRepository(database)
	if err != nil {
		t.Fatalf("NewOperationRepository() error = %v", err)
	}
	return repository
}

func seedOperationWorkspace(t *testing.T, database *sql.DB, workspaceID domain.WorkspaceID, systemID domain.SystemID) {
	t.Helper()
	now := formatDatabaseTime(time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC))
	rootPath := `E:\fixtures\` + workspaceID.String()
	if _, err := database.Exec(`INSERT INTO systems (id, name, created_at, updated_at) VALUES (?, ?, ?, ?)`,
		systemID.String(), "Sample", now, now); err != nil {
		t.Fatalf("seed system: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO workspaces
        (id, system_id, root_path, canonical_path, manifest_status, created_at, updated_at)
        VALUES (?, ?, ?, ?, 'valid', ?, ?)`, workspaceID.String(), systemID.String(),
		rootPath, strings.ToLower(rootPath), now, now); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
}
