package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"stackpilot/internal/domain"
	"stackpilot/internal/workspace"
)

const importIdempotencyRetention = 24 * time.Hour

type WorkspaceImportRepository struct{ database *sql.DB }

func NewWorkspaceImportRepository(database *sql.DB) (*WorkspaceImportRepository, error) {
	if database == nil {
		return nil, fmt.Errorf("workspace import repository database is required")
	}
	return &WorkspaceImportRepository{database: database}, nil
}

func (repository *WorkspaceImportRepository) SaveDraft(ctx context.Context, draft workspace.DraftRecord) error {
	encoded, err := json.Marshal(draft.Draft)
	if err != nil {
		return fmt.Errorf("encode workspace draft: %w", err)
	}
	_, err = repository.database.ExecContext(ctx, `INSERT INTO workspace_drafts
        (id,kind,workspace_id,root_path,canonical_path,target_key,entry_script,source_digest,
         base_manifest_digest,state,draft_json,created_at,expires_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		draft.ID, draft.Kind, nullableWorkspaceID(draft.WorkspaceID), draft.RootPath, draft.CanonicalPath,
		draft.TargetKey, nullableText(draft.EntryScript), nullableText(draft.SourceDigest), nullableText(draft.BaseManifestDigest),
		draft.State, string(encoded), formatDatabaseTime(draft.CreatedAt), formatDatabaseTime(draft.ExpiresAt))
	if err != nil {
		return fmt.Errorf("save workspace draft: %w", err)
	}
	return nil
}

func (repository *WorkspaceImportRepository) GetDraft(ctx context.Context, id string) (*workspace.DraftRecord, error) {
	row := repository.database.QueryRowContext(ctx, `SELECT id,kind,workspace_id,root_path,canonical_path,target_key,
        entry_script,source_digest,base_manifest_digest,state,draft_json,created_at,expires_at,applied_at
        FROM workspace_drafts WHERE id=?`, id)
	draft, err := scanWorkspaceDraft(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, workspace.ErrDraftNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get workspace draft: %w", err)
	}
	return &draft, nil
}

func (repository *WorkspaceImportRepository) CreateImportOperation(ctx context.Context, operation workspace.ImportOperation, steps []string) (*workspace.ImportCreateResult, error) {
	var id domain.OperationID
	var created bool
	err := executeImmediate(ctx, repository.database, func(connection *sql.Conn) error {
		var err error
		id, created, err = createWorkspaceImport(ctx, connection, operation, steps)
		return err
	})
	if err != nil {
		return nil, err
	}
	loaded, err := repository.GetImportOperation(ctx, id)
	if err != nil {
		return nil, err
	}
	return &workspace.ImportCreateResult{Operation: *loaded, Created: created}, nil
}

func createWorkspaceImport(ctx context.Context, connection *sql.Conn, candidate workspace.ImportOperation, steps []string) (domain.OperationID, bool, error) {
	if candidate.IdempotencyKey != "" {
		_, _ = connection.ExecContext(ctx, `UPDATE workspace_import_operations SET idempotency_key=NULL,idempotency_expires_at=NULL
            WHERE idempotency_subject=? AND route_key=? AND target_key=? AND idempotency_key=? AND idempotency_expires_at<=?`,
			candidate.IdempotencySubject, candidate.RouteKey, candidate.TargetKey, candidate.IdempotencyKey, formatDatabaseTime(candidate.CreatedAt))
		if existing, found, err := findWorkspaceImportIdempotency(ctx, connection, candidate); err != nil || found {
			if err != nil {
				return "", false, err
			}
			if existing.RequestDigest != candidate.RequestDigest {
				return "", false, workspace.ErrManifestConflict
			}
			return existing.ID, false, nil
		}
	}
	result, err := connection.ExecContext(ctx, `INSERT INTO workspace_import_operations
        (id,draft_id,target_key,candidate_id,type,state,idempotency_subject,route_key,idempotency_key,
         idempotency_expires_at,request_digest,created_at) VALUES (?,?,?,?,?,'queued',?,?,?,?,?,?) ON CONFLICT DO NOTHING`,
		candidate.ID.String(), candidate.DraftID, candidate.TargetKey, candidate.CandidateID, candidate.Type,
		candidate.IdempotencySubject, candidate.RouteKey, nullableText(candidate.IdempotencyKey), importIdempotencyExpiry(candidate),
		candidate.RequestDigest, formatDatabaseTime(candidate.CreatedAt))
	if err != nil {
		return "", false, fmt.Errorf("insert workspace import Operation: %w", err)
	}
	affected, _ := result.RowsAffected()
	if affected != 1 {
		return "", false, classifyWorkspaceImportConflict(ctx, connection, candidate)
	}
	for index, key := range steps {
		if _, err := connection.ExecContext(ctx, `INSERT INTO workspace_import_operation_steps
            (operation_id,step_no,step_key,state) VALUES (?,?,?,'pending')`, candidate.ID.String(), index+1, key); err != nil {
			return "", false, fmt.Errorf("insert workspace import step: %w", err)
		}
	}
	return candidate.ID, true, nil
}

func findWorkspaceImportIdempotency(ctx context.Context, connection *sql.Conn, candidate workspace.ImportOperation) (workspace.ImportOperation, bool, error) {
	row := connection.QueryRowContext(ctx, importOperationSelect+` WHERE idempotency_subject=? AND route_key=? AND target_key=? AND idempotency_key=?`,
		candidate.IdempotencySubject, candidate.RouteKey, candidate.TargetKey, candidate.IdempotencyKey)
	operation, err := scanImportOperation(row)
	if errors.Is(err, sql.ErrNoRows) {
		return workspace.ImportOperation{}, false, nil
	}
	return operation, err == nil, err
}

func classifyWorkspaceImportConflict(ctx context.Context, connection *sql.Conn, candidate workspace.ImportOperation) error {
	if existing, found, err := findWorkspaceImportIdempotency(ctx, connection, candidate); err != nil {
		return err
	} else if found && existing.RequestDigest == candidate.RequestDigest {
		return fmt.Errorf("workspace import idempotency race")
	} else if found {
		return workspace.ErrManifestConflict
	}
	var id string
	err := connection.QueryRowContext(ctx, `SELECT id FROM workspace_import_operations WHERE target_key=? AND state IN ('queued','running')`, candidate.TargetKey).Scan(&id)
	if err == nil {
		return workspace.ErrImportAlreadyActive
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return fmt.Errorf("workspace import Operation identifier conflict")
}

func importIdempotencyExpiry(operation workspace.ImportOperation) any {
	if operation.IdempotencyKey == "" {
		return nil
	}
	return formatDatabaseTime(operation.CreatedAt.Add(importIdempotencyRetention))
}

func (repository *WorkspaceImportRepository) GetImportOperation(ctx context.Context, id domain.OperationID) (*workspace.ImportOperation, error) {
	operation, err := scanImportOperation(repository.database.QueryRowContext(ctx, importOperationSelect+` WHERE id=?`, id.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, workspace.ErrImportOperationNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get workspace import Operation: %w", err)
	}
	operation.Steps, err = repository.listImportSteps(ctx, id)
	return &operation, err
}

func (repository *WorkspaceImportRepository) ListRecoverableImports(ctx context.Context) ([]domain.OperationID, error) {
	rows, err := repository.database.QueryContext(ctx, `SELECT id FROM workspace_import_operations WHERE state IN ('queued','running') ORDER BY created_at,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]domain.OperationID, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result = append(result, domain.OperationID(id))
	}
	return result, rows.Err()
}

func (repository *WorkspaceImportRepository) TransitionImportOperation(ctx context.Context, id domain.OperationID, target domain.OperationState, code string, workspaceID *domain.WorkspaceID, now time.Time) error {
	current, err := repository.GetImportOperation(ctx, id)
	if err != nil {
		return err
	}
	if current.State == target {
		return nil
	}
	if !current.State.CanTransitionTo(target) {
		return fmt.Errorf("invalid workspace import Operation transition")
	}
	started, finished, duration := current.StartedAt, current.FinishedAt, current.DurationMillis
	if target == domain.OperationRunning {
		started = timePointer(now)
	}
	if target.Terminal() {
		finished = timePointer(now)
		duration = durationMillis(current.CreatedAt, started, now)
	}
	result, err := repository.database.ExecContext(ctx, `UPDATE workspace_import_operations SET state=?,workspace_id=COALESCE(?,workspace_id),
        error_code=?,started_at=?,finished_at=?,duration_ms=? WHERE id=? AND state=?`, string(target), nullableWorkspaceID(workspaceID),
		nullableText(code), nullableTime(started), nullableTime(finished), nullableInteger(duration), id.String(), string(current.State))
	if err != nil {
		return err
	}
	return requireSingleTransition(result)
}

func (repository *WorkspaceImportRepository) TransitionImportStep(ctx context.Context, id domain.OperationID, number int, target domain.OperationStepState, code string, now time.Time) error {
	var current string
	err := repository.database.QueryRowContext(ctx, `SELECT state FROM workspace_import_operation_steps WHERE operation_id=? AND step_no=?`, id.String(), number).Scan(&current)
	if err != nil {
		return err
	}
	state := domain.OperationStepState(current)
	if state == target {
		return nil
	}
	if !state.CanTransitionTo(target) {
		return fmt.Errorf("invalid workspace import step transition")
	}
	started, finished := any(nil), any(nil)
	if target == domain.OperationStepRunning {
		started = formatDatabaseTime(now)
	}
	if target != domain.OperationStepRunning && target != domain.OperationStepPending {
		finished = formatDatabaseTime(now)
	}
	result, err := repository.database.ExecContext(ctx, `UPDATE workspace_import_operation_steps SET state=?,started_at=COALESCE(?,started_at),finished_at=?,error_code=?
        WHERE operation_id=? AND step_no=? AND state=?`, string(target), started, finished, nullableText(code), id.String(), number, current)
	if err != nil {
		return err
	}
	return requireSingleTransition(result)
}

func (repository *WorkspaceImportRepository) MarkDraftApplied(ctx context.Context, id string, now time.Time) error {
	_, err := repository.database.ExecContext(ctx, `UPDATE workspace_drafts SET state='applied',applied_at=? WHERE id=? AND state='active'`, formatDatabaseTime(now), id)
	return err
}

func (repository *WorkspaceImportRepository) ExpireDrafts(ctx context.Context, now time.Time) error {
	_, err := repository.database.ExecContext(ctx, `UPDATE workspace_drafts SET state='expired' WHERE state='active' AND expires_at<=?`, formatDatabaseTime(now))
	return err
}

func (repository *WorkspaceImportRepository) SaveWorkspaceSource(ctx context.Context, source workspace.SourceRecord) error {
	_, err := repository.database.ExecContext(ctx, `INSERT INTO workspace_sources
        (workspace_id,source_type,entry_script,source_digest,analyzed_at,updated_at) VALUES (?,?,?,?,?,?)
        ON CONFLICT(workspace_id) DO UPDATE SET source_type=excluded.source_type,entry_script=excluded.entry_script,
        source_digest=excluded.source_digest,analyzed_at=excluded.analyzed_at,updated_at=excluded.updated_at`,
		source.WorkspaceID.String(), source.SourceType, nullableText(source.EntryScript), nullableText(source.SourceDigest), nullableTime(source.AnalyzedAt), formatDatabaseTime(source.UpdatedAt))
	return err
}

func (repository *WorkspaceImportRepository) GetWorkspaceSource(ctx context.Context, id domain.WorkspaceID) (*workspace.SourceRecord, error) {
	var source workspace.SourceRecord
	var entry, digest, analyzed sql.NullString
	var updated string
	err := repository.database.QueryRowContext(ctx, `SELECT source_type,entry_script,source_digest,analyzed_at,updated_at FROM workspace_sources WHERE workspace_id=?`, id.String()).Scan(&source.SourceType, &entry, &digest, &analyzed, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, workspace.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	source.WorkspaceID, source.EntryScript, source.SourceDigest = id, entry.String, digest.String
	if analyzed.Valid {
		value, err := parseDatabaseTime(analyzed.String)
		if err != nil {
			return nil, err
		}
		source.AnalyzedAt = &value
	}
	source.UpdatedAt, err = parseDatabaseTime(updated)
	return &source, err
}

const importOperationSelect = `SELECT id,draft_id,workspace_id,target_key,candidate_id,type,state,idempotency_subject,route_key,
    idempotency_key,request_digest,error_code,created_at,started_at,finished_at,duration_ms FROM workspace_import_operations`

func scanImportOperation(scanner rowScanner) (workspace.ImportOperation, error) {
	var value workspace.ImportOperation
	var id, state, created string
	var workspaceID, key, code, started, finished sql.NullString
	var duration sql.NullInt64
	err := scanner.Scan(&id, &value.DraftID, &workspaceID, &value.TargetKey, &value.CandidateID, &value.Type, &state,
		&value.IdempotencySubject, &value.RouteKey, &key, &value.RequestDigest, &code, &created, &started, &finished, &duration)
	if err != nil {
		return value, err
	}
	value.ID, value.State, value.IdempotencyKey, value.ErrorCode = domain.OperationID(id), domain.OperationState(state), key.String, code.String
	if workspaceID.Valid {
		parsed := domain.WorkspaceID(workspaceID.String)
		value.WorkspaceID = &parsed
	}
	value.CreatedAt, err = parseDatabaseTime(created)
	if err != nil {
		return value, err
	}
	if started.Valid {
		parsed, err := parseDatabaseTime(started.String)
		if err != nil {
			return value, err
		}
		value.StartedAt = &parsed
	}
	if finished.Valid {
		parsed, err := parseDatabaseTime(finished.String)
		if err != nil {
			return value, err
		}
		value.FinishedAt = &parsed
	}
	if duration.Valid {
		value.DurationMillis = &duration.Int64
	}
	return value, nil
}

func (repository *WorkspaceImportRepository) listImportSteps(ctx context.Context, id domain.OperationID) ([]workspace.ImportStep, error) {
	rows, err := repository.database.QueryContext(ctx, `SELECT step_no,step_key,state,started_at,finished_at,error_code
        FROM workspace_import_operation_steps WHERE operation_id=? ORDER BY step_no`, id.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]workspace.ImportStep, 0)
	for rows.Next() {
		var step workspace.ImportStep
		var state string
		var started, finished, code sql.NullString
		if err := rows.Scan(&step.Number, &step.Key, &state, &started, &finished, &code); err != nil {
			return nil, err
		}
		step.State, step.ErrorCode = domain.OperationStepState(state), code.String
		if started.Valid {
			value, _ := parseDatabaseTime(started.String)
			step.StartedAt = &value
		}
		if finished.Valid {
			value, _ := parseDatabaseTime(finished.String)
			step.FinishedAt = &value
		}
		result = append(result, step)
	}
	return result, rows.Err()
}

func scanWorkspaceDraft(scanner rowScanner) (workspace.DraftRecord, error) {
	var value workspace.DraftRecord
	var workspaceID, entry, source, base, applied sql.NullString
	var encoded, created, expires string
	err := scanner.Scan(&value.ID, &value.Kind, &workspaceID, &value.RootPath, &value.CanonicalPath, &value.TargetKey, &entry, &source, &base,
		&value.State, &encoded, &created, &expires, &applied)
	if err != nil {
		return value, err
	}
	if workspaceID.Valid {
		id := domain.WorkspaceID(workspaceID.String)
		value.WorkspaceID = &id
	}
	value.EntryScript, value.SourceDigest, value.BaseManifestDigest = entry.String, source.String, base.String
	if err := json.Unmarshal([]byte(encoded), &value.Draft); err != nil {
		return value, err
	}
	value.CreatedAt, err = parseDatabaseTime(created)
	if err != nil {
		return value, err
	}
	value.ExpiresAt, err = parseDatabaseTime(expires)
	if err != nil {
		return value, err
	}
	if applied.Valid {
		parsed, err := parseDatabaseTime(applied.String)
		if err != nil {
			return value, err
		}
		value.AppliedAt = &parsed
	}
	return value, nil
}

func nullableWorkspaceID(value *domain.WorkspaceID) any {
	if value == nil {
		return nil
	}
	return value.String()
}
