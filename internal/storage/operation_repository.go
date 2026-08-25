package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"stackpilot/internal/domain"
	"stackpilot/internal/events"
	"stackpilot/internal/orchestrator"
)

const idempotencyRetention = 24 * time.Hour

// OperationRepository persists Operation lifecycle state in SQLite.
type OperationRepository struct {
	database *sql.DB
	notifier events.Notifier
}

// LatestOperationEvent returns the newest durable event cursor for one Operation.
func (repository *OperationRepository) LatestOperationEvent(ctx context.Context, id domain.OperationID) (domain.EventID, bool, error) {
	if _, err := domain.ParseOperationID(id.String()); err != nil {
		return 0, false, err
	}
	var value int64
	err := repository.database.QueryRowContext(ctx, `SELECT id FROM events WHERE operation_id=? ORDER BY id DESC LIMIT 1`, id.String()).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("read latest Operation event: %w", err)
	}
	eventID, err := domain.NewEventID(value)
	return eventID, err == nil, err
}

// NewOperationRepository constructs an Operation repository over a migrated database.
func NewOperationRepository(database *sql.DB) (*OperationRepository, error) {
	return NewOperationRepositoryWithNotifier(database, nil)
}

// NewOperationRepositoryWithNotifier publishes IDs only after event transactions commit.
func NewOperationRepositoryWithNotifier(database *sql.DB, notifier events.Notifier) (*OperationRepository, error) {
	if database == nil {
		return nil, fmt.Errorf("Operation repository database is required")
	}
	return &OperationRepository{database: database, notifier: notifier}, nil
}

// Create atomically handles idempotency, acquires the workspace lock, and inserts steps.
func (repository *OperationRepository) Create(ctx context.Context, command orchestrator.CreateCommand) (*orchestrator.CreateResult, error) {
	var created bool
	var operationID domain.OperationID
	var eventID domain.EventID
	err := executeImmediate(ctx, repository.database, func(connection *sql.Conn) error {
		var err error
		operationID, created, eventID, err = createOperationRecord(ctx, connection, command)
		return err
	})
	if err != nil {
		return nil, err
	}
	repository.notify(eventID)
	operation, err := repository.Get(ctx, operationID)
	if err != nil {
		return nil, err
	}
	return &orchestrator.CreateResult{Operation: *operation, Created: created}, nil
}

// Get returns one Operation with ordered steps.
func (repository *OperationRepository) Get(ctx context.Context, id domain.OperationID) (*orchestrator.Operation, error) {
	operation, err := scanOperation(repository.database.QueryRowContext(ctx, operationSelect+` WHERE id = ?`, id.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, orchestrator.ErrOperationNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get Operation: %w", err)
	}
	steps, err := repository.listSteps(ctx, id)
	if err != nil {
		return nil, err
	}
	operation.Steps = steps
	return &operation, nil
}

// List returns newest Operations and their ordered steps with a bounded scope.
func (repository *OperationRepository) List(ctx context.Context, workspaceID *domain.WorkspaceID, limit int) ([]orchestrator.Operation, error) {
	query, arguments := operationSelect+` ORDER BY created_at DESC, id DESC LIMIT ?`, []any{limit}
	if workspaceID != nil {
		query = operationSelect + ` WHERE workspace_id = ? ORDER BY created_at DESC, id DESC LIMIT ?`
		arguments = []any{workspaceID.String(), limit}
	}
	rows, err := repository.database.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, fmt.Errorf("list Operations: %w", err)
	}
	operations, err := scanOperations(rows)
	if err != nil {
		return nil, err
	}
	for index := range operations {
		operations[index].Steps, err = repository.listSteps(ctx, operations[index].ID)
		if err != nil {
			return nil, err
		}
	}
	return operations, nil
}

func scanOperations(rows *sql.Rows) ([]orchestrator.Operation, error) {
	defer rows.Close()
	result := make([]orchestrator.Operation, 0)
	for rows.Next() {
		operation, err := scanOperation(rows)
		if err != nil {
			return nil, fmt.Errorf("scan Operation: %w", err)
		}
		result = append(result, operation)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Operations: %w", err)
	}
	return result, nil
}

// Transition validates and persists one Operation state change.
func (repository *OperationRepository) Transition(ctx context.Context, id domain.OperationID, target domain.OperationState, errorCode string, now time.Time) (*orchestrator.Operation, error) {
	var eventID domain.EventID
	err := executeImmediate(ctx, repository.database, func(connection *sql.Conn) error {
		var err error
		eventID, err = transitionOperation(ctx, connection, id, target, errorCode, now)
		return err
	})
	if err != nil {
		return nil, err
	}
	repository.notify(eventID)
	return repository.Get(ctx, id)
}

// RequestCancel records cooperative cancellation using the central state machine.
func (repository *OperationRepository) RequestCancel(ctx context.Context, id domain.OperationID, now time.Time) (*orchestrator.Operation, error) {
	var eventID domain.EventID
	err := executeImmediate(ctx, repository.database, func(connection *sql.Conn) error {
		var err error
		eventID, err = cancelOperation(ctx, connection, id, now)
		return err
	})
	if err != nil {
		return nil, err
	}
	repository.notify(eventID)
	return repository.Get(ctx, id)
}

// TransitionStep validates and persists one step transition.
func (repository *OperationRepository) TransitionStep(ctx context.Context, id domain.OperationID, number int, target domain.OperationStepState, errorCode, detailRef string, now time.Time) (*orchestrator.Operation, error) {
	var eventID domain.EventID
	err := executeImmediate(ctx, repository.database, func(connection *sql.Conn) error {
		var err error
		eventID, err = transitionOperationStep(ctx, connection, id, number, target, errorCode, detailRef, now)
		return err
	})
	if err != nil {
		return nil, err
	}
	repository.notify(eventID)
	return repository.Get(ctx, id)
}

// RecoverInterrupted fails every non-terminal Operation and settles unfinished steps in one transaction per Operation.
func (repository *OperationRepository) RecoverInterrupted(ctx context.Context, now time.Time) ([]domain.OperationID, error) {
	ids, err := repository.listInterruptedIDs(ctx)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		eventIDs, recoverErr := repository.recoverInterrupted(ctx, id, now)
		if recoverErr != nil {
			return nil, recoverErr
		}
		for _, eventID := range eventIDs {
			repository.notify(eventID)
		}
	}
	return ids, nil
}

func (repository *OperationRepository) listInterruptedIDs(ctx context.Context) ([]domain.OperationID, error) {
	rows, err := repository.database.QueryContext(ctx, `SELECT id FROM operations
        WHERE state IN ('queued','running','cancelling') ORDER BY created_at,id`)
	if err != nil {
		return nil, fmt.Errorf("list interrupted Operations: %w", err)
	}
	defer rows.Close()
	var ids []domain.OperationID
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan interrupted Operation: %w", err)
		}
		ids = append(ids, domain.OperationID(id))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate interrupted Operations: %w", err)
	}
	return ids, nil
}

func (repository *OperationRepository) recoverInterrupted(ctx context.Context, id domain.OperationID, now time.Time) ([]domain.EventID, error) {
	var eventIDs []domain.EventID
	err := executeImmediate(ctx, repository.database, func(connection *sql.Conn) error {
		operation, err := getOperationOnConnection(ctx, connection, id)
		if err != nil || operation.State.Terminal() {
			return err
		}
		steps, err := listStepsOnConnection(ctx, connection, id)
		if err != nil {
			return err
		}
		for _, step := range steps {
			target, code := domain.OperationStepSkipped, ""
			if step.State == domain.OperationStepRunning {
				target, code = domain.OperationStepFailed, "CONTROL_PLANE_RESTARTED"
			} else if step.State != domain.OperationStepPending {
				continue
			}
			if err := updateStepState(ctx, connection, id, step, target, code, "", now); err != nil {
				return err
			}
			event, err := events.OperationStepChanged(operation, step, target, code, now)
			if err != nil {
				return err
			}
			eventID, err := insertEvent(ctx, connection, event)
			if err != nil {
				return err
			}
			eventIDs = append(eventIDs, eventID)
		}
		if err := updateOperationState(ctx, connection, operation, domain.OperationFailed, "CONTROL_PLANE_RESTARTED", now); err != nil {
			return err
		}
		event, err := events.OperationStateChanged(operation, domain.OperationFailed, "CONTROL_PLANE_RESTARTED", now)
		if err != nil {
			return err
		}
		eventID, err := insertEvent(ctx, connection, event)
		if err == nil {
			eventIDs = append(eventIDs, eventID)
		}
		return err
	})
	return eventIDs, err
}

func createOperationRecord(ctx context.Context, connection *sql.Conn, command orchestrator.CreateCommand) (domain.OperationID, bool, domain.EventID, error) {
	candidate := command.Operation
	if err := expireIdempotencyKey(ctx, connection, candidate); err != nil {
		return "", false, 0, err
	}
	existing, found, err := findIdempotentOperation(ctx, connection, candidate)
	if err != nil {
		return "", false, 0, err
	}
	if found {
		if existing.RequestDigest != candidate.RequestDigest {
			return "", false, 0, orchestrator.ErrIdempotencyKeyReused
		}
		return existing.ID, false, 0, nil
	}
	inserted, err := insertOperation(ctx, connection, candidate)
	if err != nil {
		return "", false, 0, err
	}
	if !inserted {
		return "", false, 0, classifyOperationConflict(ctx, connection, candidate)
	}
	if err := insertOperationSteps(ctx, connection, candidate.ID, command.StepKeys); err != nil {
		return "", false, 0, err
	}
	event, err := events.OperationCreated(candidate)
	if err != nil {
		return "", false, 0, err
	}
	eventID, err := insertEvent(ctx, connection, event)
	return candidate.ID, true, eventID, err
}

func transitionOperation(ctx context.Context, connection *sql.Conn, id domain.OperationID, target domain.OperationState, code string, now time.Time) (domain.EventID, error) {
	current, err := getOperationOnConnection(ctx, connection, id)
	if err != nil {
		return 0, err
	}
	if !current.State.CanTransitionTo(target) {
		return 0, orchestrator.ErrInvalidTransition
	}
	if err := updateOperationState(ctx, connection, current, target, code, now); err != nil {
		return 0, err
	}
	event, err := events.OperationStateChanged(current, target, code, now)
	if err != nil {
		return 0, err
	}
	return insertEvent(ctx, connection, event)
}

func cancelOperation(ctx context.Context, connection *sql.Conn, id domain.OperationID, now time.Time) (domain.EventID, error) {
	current, err := getOperationOnConnection(ctx, connection, id)
	if err != nil {
		return 0, err
	}
	if !current.Cancellable {
		return 0, orchestrator.ErrNotCancellable
	}
	if current.State == domain.OperationCancelling {
		return 0, nil
	}
	if current.State != domain.OperationQueued && current.State != domain.OperationRunning {
		return 0, orchestrator.ErrInvalidTransition
	}
	target := domain.OperationCancelling
	if current.State == domain.OperationQueued {
		target = domain.OperationCancelled
	}
	if err := updateCancellation(ctx, connection, current, target, now); err != nil {
		return 0, err
	}
	event, err := events.OperationStateChanged(current, target, "", now)
	if err != nil {
		return 0, err
	}
	return insertEvent(ctx, connection, event)
}

func transitionOperationStep(ctx context.Context, connection *sql.Conn, id domain.OperationID, number int, target domain.OperationStepState, code, detailRef string, now time.Time) (domain.EventID, error) {
	operation, err := getOperationOnConnection(ctx, connection, id)
	if err != nil {
		return 0, err
	}
	step, err := getStepOnConnection(ctx, connection, id, number)
	if err != nil {
		return 0, err
	}
	if !step.State.CanTransitionTo(target) {
		return 0, orchestrator.ErrInvalidTransition
	}
	if err := updateStepState(ctx, connection, id, step, target, code, detailRef, now); err != nil {
		return 0, err
	}
	event, err := events.OperationStepChanged(operation, step, target, code, now)
	if err != nil {
		return 0, err
	}
	return insertEvent(ctx, connection, event)
}

func (repository *OperationRepository) notify(id domain.EventID) {
	if id > 0 && repository.notifier != nil {
		repository.notifier.Notify(id)
	}
}

const operationSelect = `SELECT id, workspace_id, system_id, type, state, idempotency_subject,
    route_key, idempotency_key, request_digest, cancellable, cancel_requested_at, error_code,
    created_at, started_at, finished_at, duration_ms FROM operations`

func findIdempotentOperation(ctx context.Context, connection *sql.Conn, candidate orchestrator.Operation) (orchestrator.Operation, bool, error) {
	if candidate.IdempotencyKey == "" {
		return orchestrator.Operation{}, false, nil
	}
	row := connection.QueryRowContext(ctx, operationSelect+` WHERE idempotency_subject = ? AND route_key = ?
        AND workspace_id = ? AND idempotency_key = ?`, candidate.IdempotencySubject, candidate.RouteKey,
		candidate.WorkspaceID.String(), candidate.IdempotencyKey)
	operation, err := scanOperation(row)
	if errors.Is(err, sql.ErrNoRows) {
		return orchestrator.Operation{}, false, nil
	}
	if err != nil {
		return orchestrator.Operation{}, false, fmt.Errorf("find idempotent Operation: %w", err)
	}
	return operation, true, nil
}

func expireIdempotencyKey(ctx context.Context, connection *sql.Conn, candidate orchestrator.Operation) error {
	if candidate.IdempotencyKey == "" {
		return nil
	}
	_, err := connection.ExecContext(ctx, `UPDATE operations
        SET idempotency_key = NULL, idempotency_expires_at = NULL
        WHERE idempotency_subject = ? AND route_key = ? AND workspace_id = ?
          AND idempotency_key = ? AND idempotency_expires_at <= ?`,
		candidate.IdempotencySubject, candidate.RouteKey, candidate.WorkspaceID.String(),
		candidate.IdempotencyKey, formatDatabaseTime(candidate.CreatedAt))
	if err != nil {
		return fmt.Errorf("expire Operation idempotency key: %w", err)
	}
	return nil
}

func insertOperation(ctx context.Context, connection *sql.Conn, operation orchestrator.Operation) (bool, error) {
	result, err := connection.ExecContext(ctx, `INSERT INTO operations
        (id, workspace_id, system_id, type, state, idempotency_subject, route_key, idempotency_key,
         idempotency_expires_at, request_digest, cancellable, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT DO NOTHING`, operation.ID.String(), operation.WorkspaceID.String(), operation.SystemID.String(),
		string(operation.Type), string(operation.State), operation.IdempotencySubject, operation.RouteKey,
		nullableText(operation.IdempotencyKey), idempotencyExpiry(operation), operation.RequestDigest, boolInteger(operation.Cancellable),
		formatDatabaseTime(operation.CreatedAt))
	if err != nil {
		return false, fmt.Errorf("insert Operation: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("inspect Operation insert: %w", err)
	}
	return affected == 1, nil
}

func idempotencyExpiry(operation orchestrator.Operation) any {
	if operation.IdempotencyKey == "" {
		return nil
	}
	return formatDatabaseTime(operation.CreatedAt.Add(idempotencyRetention))
}

func classifyOperationConflict(ctx context.Context, connection *sql.Conn, candidate orchestrator.Operation) error {
	existing, found, err := findIdempotentOperation(ctx, connection, candidate)
	if err != nil {
		return err
	}
	if found {
		if existing.RequestDigest == candidate.RequestDigest {
			return fmt.Errorf("idempotent Operation appeared during locked transaction")
		}
		return orchestrator.ErrIdempotencyKeyReused
	}
	var activeID string
	err = connection.QueryRowContext(ctx, `SELECT id FROM operations WHERE workspace_id = ?
        AND state IN ('queued', 'running', 'cancelling')`, candidate.WorkspaceID.String()).Scan(&activeID)
	if err == nil {
		return orchestrator.ErrOperationAlreadyActive
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("classify active Operation conflict: %w", err)
	}
	return fmt.Errorf("Operation identifier conflict")
}

func insertOperationSteps(ctx context.Context, connection *sql.Conn, id domain.OperationID, keys []string) error {
	for index, key := range keys {
		_, err := connection.ExecContext(ctx, `INSERT INTO operation_steps
            (operation_id, step_no, step_key, state, attempt) VALUES (?, ?, ?, 'pending', 0)`,
			id.String(), index+1, key)
		if err != nil {
			return fmt.Errorf("insert Operation step: %w", err)
		}
	}
	return nil
}

func getOperationOnConnection(ctx context.Context, connection *sql.Conn, id domain.OperationID) (orchestrator.Operation, error) {
	operation, err := scanOperation(connection.QueryRowContext(ctx, operationSelect+` WHERE id = ?`, id.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return orchestrator.Operation{}, orchestrator.ErrOperationNotFound
	}
	if err != nil {
		return orchestrator.Operation{}, fmt.Errorf("read Operation for transition: %w", err)
	}
	return operation, nil
}

func updateOperationState(ctx context.Context, connection *sql.Conn, current orchestrator.Operation, target domain.OperationState, errorCode string, now time.Time) error {
	startedAt := current.StartedAt
	if target == domain.OperationRunning {
		startedAt = timePointer(now)
	}
	finishedAt, duration := current.FinishedAt, current.DurationMillis
	if target.Terminal() {
		finishedAt = timePointer(now)
		duration = durationMillis(current.CreatedAt, startedAt, now)
	}
	result, err := connection.ExecContext(ctx, `UPDATE operations SET state = ?, started_at = ?, finished_at = ?,
        duration_ms = ?, error_code = ? WHERE id = ? AND state = ?`, string(target), nullableTime(startedAt),
		nullableTime(finishedAt), nullableInteger(duration), nullableText(errorCode), current.ID.String(), string(current.State))
	if err != nil {
		return fmt.Errorf("transition Operation state: %w", err)
	}
	return requireSingleTransition(result)
}

func updateCancellation(ctx context.Context, connection *sql.Conn, current orchestrator.Operation, target domain.OperationState, now time.Time) error {
	finishedAt := current.FinishedAt
	duration := current.DurationMillis
	if target == domain.OperationCancelled {
		finishedAt = timePointer(now)
		duration = durationMillis(current.CreatedAt, current.StartedAt, now)
	}
	result, err := connection.ExecContext(ctx, `UPDATE operations SET state = ?, cancel_requested_at = ?,
        finished_at = ?, duration_ms = ? WHERE id = ? AND state = ?`, string(target), formatDatabaseTime(now),
		nullableTime(finishedAt), nullableInteger(duration), current.ID.String(), string(current.State))
	if err != nil {
		return fmt.Errorf("request Operation cancellation: %w", err)
	}
	return requireSingleTransition(result)
}

func getStepOnConnection(ctx context.Context, connection *sql.Conn, id domain.OperationID, number int) (orchestrator.Step, error) {
	row := connection.QueryRowContext(ctx, stepSelect+` WHERE operation_id = ? AND step_no = ?`, id.String(), number)
	step, err := scanStep(row)
	if errors.Is(err, sql.ErrNoRows) {
		return orchestrator.Step{}, orchestrator.ErrStepNotFound
	}
	if err != nil {
		return orchestrator.Step{}, fmt.Errorf("read Operation step: %w", err)
	}
	return step, nil
}

func updateStepState(ctx context.Context, connection *sql.Conn, id domain.OperationID, step orchestrator.Step, target domain.OperationStepState, errorCode, detailRef string, now time.Time) error {
	attempt := step.Attempt
	startedAt, finishedAt, duration := step.StartedAt, step.FinishedAt, step.DurationMillis
	if target == domain.OperationStepRunning {
		attempt++
		startedAt, finishedAt, duration = timePointer(now), nil, nil
		errorCode, detailRef = "", ""
	} else if target != domain.OperationStepPending {
		finishedAt = timePointer(now)
		duration = stepDurationMillis(startedAt, now)
	}
	result, err := connection.ExecContext(ctx, `UPDATE operation_steps SET state = ?, attempt = ?, started_at = ?,
        finished_at = ?, duration_ms = ?, error_code = ?, detail_ref = ?
        WHERE operation_id = ? AND step_no = ? AND state = ?`, string(target), attempt, nullableTime(startedAt),
		nullableTime(finishedAt), nullableInteger(duration), nullableText(errorCode), nullableText(detailRef),
		id.String(), step.Number, string(step.State))
	if err != nil {
		return fmt.Errorf("transition Operation step: %w", err)
	}
	return requireSingleTransition(result)
}

func requireSingleTransition(result sql.Result) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect state transition: %w", err)
	}
	if affected != 1 {
		return orchestrator.ErrInvalidTransition
	}
	return nil
}

const stepSelect = `SELECT step_no, step_key, state, attempt, started_at, finished_at,
    duration_ms, error_code, detail_ref FROM operation_steps`

func (repository *OperationRepository) listSteps(ctx context.Context, id domain.OperationID) ([]orchestrator.Step, error) {
	rows, err := repository.database.QueryContext(ctx, stepSelect+` WHERE operation_id = ? ORDER BY step_no`, id.String())
	if err != nil {
		return nil, fmt.Errorf("list Operation steps: %w", err)
	}
	defer rows.Close()
	steps := make([]orchestrator.Step, 0)
	for rows.Next() {
		step, err := scanStep(rows)
		if err != nil {
			return nil, fmt.Errorf("scan Operation step: %w", err)
		}
		steps = append(steps, step)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Operation steps: %w", err)
	}
	return steps, nil
}

func listStepsOnConnection(ctx context.Context, connection *sql.Conn, id domain.OperationID) ([]orchestrator.Step, error) {
	rows, err := connection.QueryContext(ctx, stepSelect+` WHERE operation_id = ? ORDER BY step_no`, id.String())
	if err != nil {
		return nil, fmt.Errorf("list Operation steps for recovery: %w", err)
	}
	defer rows.Close()
	steps := make([]orchestrator.Step, 0)
	for rows.Next() {
		step, err := scanStep(rows)
		if err != nil {
			return nil, fmt.Errorf("scan Operation step for recovery: %w", err)
		}
		steps = append(steps, step)
	}
	return steps, rows.Err()
}

func scanOperation(scanner rowScanner) (orchestrator.Operation, error) {
	var operation orchestrator.Operation
	var id, workspaceID, systemID, operationType, state, createdAt string
	var key, cancelAt, errorCode, startedAt, finishedAt sql.NullString
	var cancellable int
	var duration sql.NullInt64
	err := scanner.Scan(&id, &workspaceID, &systemID, &operationType, &state, &operation.IdempotencySubject,
		&operation.RouteKey, &key, &operation.RequestDigest, &cancellable, &cancelAt, &errorCode,
		&createdAt, &startedAt, &finishedAt, &duration)
	if err != nil {
		return orchestrator.Operation{}, err
	}
	operation.ID, operation.WorkspaceID, operation.SystemID = domain.OperationID(id), domain.WorkspaceID(workspaceID), domain.SystemID(systemID)
	operation.Type, operation.State = domain.OperationType(operationType), domain.OperationState(state)
	operation.IdempotencyKey, operation.ErrorCode, operation.Cancellable = key.String, errorCode.String, cancellable == 1
	if err := assignOperationTimes(&operation, createdAt, cancelAt, startedAt, finishedAt, duration); err != nil {
		return orchestrator.Operation{}, err
	}
	return operation, nil
}

func assignOperationTimes(operation *orchestrator.Operation, created string, cancel, started, finished sql.NullString, duration sql.NullInt64) error {
	parsed, err := parseDatabaseTime(created)
	if err != nil {
		return err
	}
	operation.CreatedAt = parsed
	if operation.CancelRequestedAt, err = parseNullableDatabaseTime(cancel); err != nil {
		return err
	}
	if operation.StartedAt, err = parseNullableDatabaseTime(started); err != nil {
		return err
	}
	if operation.FinishedAt, err = parseNullableDatabaseTime(finished); err != nil {
		return err
	}
	if duration.Valid {
		operation.DurationMillis = int64Pointer(duration.Int64)
	}
	return nil
}

func parseNullableDatabaseTime(value sql.NullString) (*time.Time, error) {
	if !value.Valid {
		return nil, nil
	}
	parsed, err := parseDatabaseTime(value.String)
	if err != nil {
		return nil, err
	}
	return timePointer(parsed), nil
}

func scanStep(scanner rowScanner) (orchestrator.Step, error) {
	var step orchestrator.Step
	var state string
	var started, finished, errorCode, detailRef sql.NullString
	var duration sql.NullInt64
	if err := scanner.Scan(&step.Number, &step.Key, &state, &step.Attempt, &started, &finished, &duration, &errorCode, &detailRef); err != nil {
		return orchestrator.Step{}, err
	}
	step.State, step.ErrorCode, step.DetailRef = domain.OperationStepState(state), errorCode.String, detailRef.String
	if started.Valid {
		value, err := parseDatabaseTime(started.String)
		if err != nil {
			return orchestrator.Step{}, err
		}
		step.StartedAt = timePointer(value)
	}
	if finished.Valid {
		value, err := parseDatabaseTime(finished.String)
		if err != nil {
			return orchestrator.Step{}, err
		}
		step.FinishedAt = timePointer(value)
	}
	if duration.Valid {
		step.DurationMillis = int64Pointer(duration.Int64)
	}
	return step, nil
}

func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return formatDatabaseTime(*value)
}

func nullableInteger(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func durationMillis(created time.Time, started *time.Time, finished time.Time) *int64 {
	begin := created
	if started != nil {
		begin = *started
	}
	value := finished.Sub(begin).Milliseconds()
	if value < 0 {
		value = 0
	}
	return int64Pointer(value)
}

func stepDurationMillis(started *time.Time, finished time.Time) *int64 {
	if started == nil {
		return int64Pointer(0)
	}
	return durationMillis(*started, started, finished)
}

func boolInteger(value bool) int {
	if value {
		return 1
	}
	return 0
}

func timePointer(value time.Time) *time.Time { copy := value.UTC(); return &copy }
func int64Pointer(value int64) *int64        { copy := value; return &copy }
