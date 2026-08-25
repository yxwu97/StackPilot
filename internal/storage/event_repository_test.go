package storage

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"stackpilot/internal/domain"
	"stackpilot/internal/events"
	"stackpilot/internal/orchestrator"
)

func TestEventRepositoryAppendsQueriesAndNotifiesAfterCommit(t *testing.T) {
	database := openTestDatabase(t)
	seedOperationWorkspace(t, database, testWorkspaceID, testSystemID)
	broker := events.NewBroker(4)
	subscription := broker.Subscribe()
	defer subscription.Close()
	repository, err := NewEventRepository(database, broker)
	if err != nil {
		t.Fatalf("NewEventRepository() error = %v", err)
	}
	first := testDomainEvent("system.registered", time.Date(2026, 8, 18, 14, 0, 0, 0, time.UTC))
	second := testDomainEvent("manifest.refreshed", first.OccurredAt.Add(time.Second))
	for _, event := range []events.Event{first, second} {
		stored, err := repository.Append(context.Background(), event)
		if err != nil {
			t.Fatalf("Append() error = %v", err)
		}
		if notified := <-subscription.Events(); notified != stored.ID {
			t.Fatalf("notification = %d, want %d", notified, stored.ID)
		}
	}
	low, high, found, err := repository.Bounds(context.Background())
	if err != nil || !found || low != 1 || high != 2 {
		t.Fatalf("Bounds() = (%d, %d, %v, %v)", low, high, found, err)
	}
	stored, err := repository.ListRange(context.Background(), 0, high, 1)
	if err != nil || len(stored) != 1 || stored[0].Type != first.Type || stored[0].OccurredAt.Location() != time.UTC {
		t.Fatalf("ListRange() = (%#v, %v)", stored, err)
	}
	stored, err = repository.ListRange(context.Background(), stored[0].ID, high, events.MaximumPageSize)
	if err != nil || len(stored) != 1 || stored[0].Type != second.Type {
		t.Fatalf("second ListRange() = (%#v, %v)", stored, err)
	}
}

func TestEventRepositoryRejectsInvalidEventsAndCursors(t *testing.T) {
	database := openTestDatabase(t)
	seedOperationWorkspace(t, database, testWorkspaceID, testSystemID)
	repository, _ := NewEventRepository(database, nil)
	invalid := testDomainEvent("invalid", time.Now().UTC())
	if _, err := repository.Append(context.Background(), invalid); !errors.Is(err, events.ErrInvalidEvent) {
		t.Fatalf("Append(invalid) error = %v", err)
	}
	if _, err := repository.ListRange(context.Background(), 2, 1, 10); !errors.Is(err, events.ErrInvalidCursor) {
		t.Fatalf("ListRange(invalid) error = %v", err)
	}
	_, _, found, err := repository.Bounds(context.Background())
	if err != nil || found {
		t.Fatalf("empty Bounds() = (found %v, error %v)", found, err)
	}
}

func TestOperationTransitionsCommitEventsAtomically(t *testing.T) {
	database := openTestDatabase(t)
	seedOperationWorkspace(t, database, testWorkspaceID, testSystemID)
	broker := events.NewBroker(8)
	repository, err := NewOperationRepositoryWithNotifier(database, broker)
	if err != nil {
		t.Fatalf("NewOperationRepositoryWithNotifier() error = %v", err)
	}
	manager, err := orchestrator.NewManager(repository)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	created, err := manager.Create(context.Background(), operationInput("evented", []byte(`{"start":true}`)))
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if _, err := manager.Start(context.Background(), created.Operation.ID); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if _, err := repository.TransitionStep(context.Background(), created.Operation.ID, 1,
		domain.OperationStepRunning, "", "", time.Now().UTC()); err != nil {
		t.Fatalf("TransitionStep() error = %v", err)
	}
	eventRepository, _ := NewEventRepository(database, nil)
	_, high, found, err := eventRepository.Bounds(context.Background())
	if err != nil || !found || high != 3 {
		t.Fatalf("event Bounds() = (high %d, found %v, error %v)", high, found, err)
	}
	stored, err := eventRepository.ListRange(context.Background(), 0, high, events.MaximumPageSize)
	if err != nil || len(stored) != 3 {
		t.Fatalf("ListRange() = (%#v, %v)", stored, err)
	}
	want := []string{events.TypeOperationCreated, events.TypeOperationStateChanged, events.TypeOperationStepChanged}
	for index, event := range stored {
		if event.Type != want[index] || event.OperationID != created.Operation.ID {
			t.Errorf("event %d = %#v", index, event)
		}
	}
}

func TestOperationStateRollsBackWhenEventInsertFails(t *testing.T) {
	database := openTestDatabase(t)
	seedOperationWorkspace(t, database, testWorkspaceID, testSystemID)
	repository := newOperationRepository(t, database)
	createdAt := time.Date(2026, 8, 18, 15, 0, 0, 0, time.UTC)
	operation := fixedOperation("op_01ARZ3NDEKTSV4RRFFQ69G5FAX", "rollback-event", createdAt)
	created, err := repository.Create(context.Background(), orchestrator.CreateCommand{Operation: operation, StepKeys: []string{"start"}})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if _, err := database.Exec(`CREATE TRIGGER reject_event BEFORE INSERT ON events BEGIN SELECT RAISE(ABORT, 'rejected'); END`); err != nil {
		t.Fatalf("create rejecting trigger: %v", err)
	}
	if _, err := repository.Transition(context.Background(), created.Operation.ID, domain.OperationRunning, "", createdAt.Add(time.Second)); err == nil {
		t.Fatal("Transition() unexpectedly succeeded")
	}
	reloaded, err := repository.Get(context.Background(), created.Operation.ID)
	if err != nil || reloaded.State != domain.OperationQueued {
		t.Fatalf("Get() after rollback = (%#v, %v)", reloaded, err)
	}
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("event count after rollback = %d, error = %v", count, err)
	}
}

func testDomainEvent(eventType string, at time.Time) events.Event {
	return events.Event{
		Type: eventType, OccurredAt: at.UTC(), WorkspaceID: testWorkspaceID,
		SystemID: testSystemID, Data: json.RawMessage(`{"version":1}`),
	}
}
