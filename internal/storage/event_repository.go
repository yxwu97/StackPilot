package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"stackpilot/internal/domain"
	"stackpilot/internal/events"
)

// EventRepository persists low-frequency domain events for SSE recovery.
type EventRepository struct {
	database *sql.DB
	notifier events.Notifier
}

// NewEventRepository constructs an event repository over a migrated database.
func NewEventRepository(database *sql.DB, notifier events.Notifier) (*EventRepository, error) {
	if database == nil {
		return nil, fmt.Errorf("event repository database is required")
	}
	return &EventRepository{database: database, notifier: notifier}, nil
}

// Append commits one standalone event and then publishes its ID non-blockingly.
func (repository *EventRepository) Append(ctx context.Context, event events.Event) (events.Event, error) {
	var id domain.EventID
	err := executeImmediate(ctx, repository.database, func(connection *sql.Conn) error {
		var err error
		id, err = insertEvent(ctx, connection, event)
		return err
	})
	if err != nil {
		return events.Event{}, err
	}
	event.ID = id
	repository.notify(id)
	return event, nil
}

// Bounds returns the current retained event ID interval.
func (repository *EventRepository) Bounds(ctx context.Context) (domain.EventID, domain.EventID, bool, error) {
	var first, last sql.NullInt64
	if err := repository.database.QueryRowContext(ctx, `SELECT MIN(id), MAX(id) FROM events`).Scan(&first, &last); err != nil {
		return 0, 0, false, fmt.Errorf("query event bounds: %w", err)
	}
	if !first.Valid || !last.Valid {
		return 0, 0, false, nil
	}
	return domain.EventID(first.Int64), domain.EventID(last.Int64), true, nil
}

// ListRange returns ascending events in `(after, through]` with a hard page bound.
func (repository *EventRepository) ListRange(ctx context.Context, after, through domain.EventID, limit int) ([]events.Event, error) {
	if after < 0 || through <= after || limit < 1 || limit > events.MaximumPageSize {
		return nil, events.ErrInvalidCursor
	}
	rows, err := repository.database.QueryContext(ctx, eventSelect+`
        WHERE id > ? AND id <= ? ORDER BY id LIMIT ?`, int64(after), int64(through), limit)
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	defer rows.Close()
	return scanEvents(rows)
}

const eventSelect = `SELECT id, event_type, workspace_id, system_id, instance_id,
    service_instance_id, operation_id, payload_json, occurred_at FROM events`

func insertEvent(ctx context.Context, executor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, event events.Event) (domain.EventID, error) {
	if err := events.Validate(event); err != nil {
		return 0, err
	}
	result, err := executor.ExecContext(ctx, `INSERT INTO events
        (event_type, workspace_id, system_id, instance_id, service_instance_id, operation_id, payload_json, occurred_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, event.Type, event.WorkspaceID.String(), event.SystemID.String(),
		nullableText(event.InstanceID.String()), nullableText(event.ServiceInstanceID.String()),
		nullableText(event.OperationID.String()), string(event.Data), formatDatabaseTime(event.OccurredAt))
	if err != nil {
		return 0, fmt.Errorf("insert domain event: %w", err)
	}
	value, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("read domain event ID: %w", err)
	}
	return domain.NewEventID(value)
}

func scanEvents(rows *sql.Rows) ([]events.Event, error) {
	result := make([]events.Event, 0)
	for rows.Next() {
		event, err := scanEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		result = append(result, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate events: %w", err)
	}
	return result, nil
}

func scanEvent(scanner rowScanner) (events.Event, error) {
	var event events.Event
	var id int64
	var workspaceID, systemID, payload, occurredAt string
	var instanceID, serviceInstanceID, operationID sql.NullString
	err := scanner.Scan(&id, &event.Type, &workspaceID, &systemID, &instanceID,
		&serviceInstanceID, &operationID, &payload, &occurredAt)
	if err != nil {
		return events.Event{}, err
	}
	event.ID, event.WorkspaceID, event.SystemID = domain.EventID(id), domain.WorkspaceID(workspaceID), domain.SystemID(systemID)
	event.InstanceID, event.ServiceInstanceID = domain.SystemInstanceID(instanceID.String), domain.ServiceInstanceID(serviceInstanceID.String)
	event.OperationID, event.Data = domain.OperationID(operationID.String), json.RawMessage(payload)
	event.OccurredAt, err = parseDatabaseTime(occurredAt)
	if err != nil {
		return events.Event{}, err
	}
	if err := events.Validate(event); err != nil {
		return events.Event{}, err
	}
	return event, nil
}

func (repository *EventRepository) notify(id domain.EventID) {
	if repository.notifier != nil {
		repository.notifier.Notify(id)
	}
}

var _ events.Store = (*EventRepository)(nil)
