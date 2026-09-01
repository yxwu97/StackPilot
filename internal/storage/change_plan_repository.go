package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"stackpilot/internal/changeplan"
	"stackpilot/internal/domain"
	"stackpilot/internal/revision"
)

// ChangePlanRepository persists immutable deterministic plan results.
type ChangePlanRepository struct {
	database *sql.DB
}

// NewChangePlanRepository constructs a repository over migration 17 or later.
func NewChangePlanRepository(database *sql.DB) (*ChangePlanRepository, error) {
	if database == nil {
		return nil, fmt.Errorf("change plan repository database is required")
	}
	return &ChangePlanRepository{database: database}, nil
}

// SaveOrGet inserts one plan or returns the identical immutable result.
func (repository *ChangePlanRepository) SaveOrGet(ctx context.Context, record changeplan.Record) (*changeplan.Record, error) {
	if err := validateChangePlanRecord(record); err != nil {
		return nil, err
	}
	result, err := repository.database.ExecContext(ctx, `INSERT OR IGNORE INTO change_plans
        (id,created_by_operation_id,workspace_id,system_id,from_snapshot_id,to_snapshot_id,rule_version,state,risk,
         item_count,blocked_count,result_schema_version,result_digest,result_json,created_at)
        VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, record.ID.String(), record.CreatedByOperationID.String(), record.WorkspaceID.String(),
		record.SystemID.String(), record.FromSnapshotID.String(), record.ToSnapshotID.String(), record.RuleVersion, string(record.State),
		string(record.Risk), record.ItemCount, record.BlockedCount, record.ResultSchemaVersion, record.ResultDigest,
		string(record.ResultJSON), formatDatabaseTime(record.CreatedAt))
	if err != nil {
		return nil, fmt.Errorf("save change plan: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("read change plan insert result: %w", err)
	}
	if affected == 1 {
		copy := record
		return &copy, nil
	}
	return repository.getIdentical(ctx, record)
}

// Get returns one validated immutable plan record.
func (repository *ChangePlanRepository) Get(ctx context.Context, id domain.ChangePlanID) (*changeplan.Record, error) {
	if _, err := domain.ParseChangePlanID(id.String()); err != nil {
		return nil, changeplan.ErrInvalidInput
	}
	return scanChangePlanRecord(repository.database.QueryRowContext(ctx, changePlanSelect+` WHERE id=?`, id.String()))
}

func (repository *ChangePlanRepository) getIdentical(ctx context.Context, record changeplan.Record) (*changeplan.Record, error) {
	row := repository.database.QueryRowContext(ctx, changePlanSelect+` WHERE workspace_id=? AND from_snapshot_id=? AND
        to_snapshot_id=? AND rule_version=? AND result_digest=?`, record.WorkspaceID.String(), record.FromSnapshotID.String(),
		record.ToSnapshotID.String(), record.RuleVersion, record.ResultDigest)
	existing, err := scanChangePlanRecord(row)
	if err != nil {
		return nil, err
	}
	if existing.State != record.State || existing.Risk != record.Risk || string(existing.ResultJSON) != string(record.ResultJSON) {
		return nil, changeplan.ErrInvalidInput
	}
	return existing, nil
}

const changePlanSelect = `SELECT id,created_by_operation_id,workspace_id,system_id,from_snapshot_id,to_snapshot_id,
    rule_version,state,risk,item_count,blocked_count,result_schema_version,result_digest,result_json,created_at FROM change_plans`

func scanChangePlanRecord(scanner rowScanner) (*changeplan.Record, error) {
	var record changeplan.Record
	var id, operationID, workspaceID, systemID, fromID, toID, state, risk, resultJSON, createdAt string
	err := scanner.Scan(&id, &operationID, &workspaceID, &systemID, &fromID, &toID, &record.RuleVersion, &state, &risk,
		&record.ItemCount, &record.BlockedCount, &record.ResultSchemaVersion, &record.ResultDigest, &resultJSON, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, changeplan.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan change plan: %w", err)
	}
	record.ID, record.CreatedByOperationID = domain.ChangePlanID(id), domain.OperationID(operationID)
	record.WorkspaceID, record.SystemID = domain.WorkspaceID(workspaceID), domain.SystemID(systemID)
	record.FromSnapshotID, record.ToSnapshotID = domain.RevisionID(fromID), domain.RevisionID(toID)
	record.State, record.Risk, record.ResultJSON = domain.ChangePlanState(state), domain.ChangeRisk(risk), []byte(resultJSON)
	record.CreatedAt, err = parseDatabaseTime(createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse change plan creation time: %w", err)
	}
	if err := validateChangePlanRecord(record); err != nil {
		return nil, fmt.Errorf("validate persisted change plan: %w", err)
	}
	return &record, nil
}

func validateChangePlanRecord(record changeplan.Record) error {
	if _, err := domain.ParseChangePlanID(record.ID.String()); err != nil {
		return changeplan.ErrInvalidInput
	}
	if _, err := domain.ParseOperationID(record.CreatedByOperationID.String()); err != nil {
		return changeplan.ErrInvalidInput
	}
	if _, err := domain.ParseWorkspaceID(record.WorkspaceID.String()); err != nil {
		return changeplan.ErrInvalidInput
	}
	if _, err := domain.ParseSystemID(record.SystemID.String()); err != nil {
		return changeplan.ErrInvalidInput
	}
	if _, err := domain.ParseRevisionID(record.FromSnapshotID.String()); err != nil {
		return changeplan.ErrInvalidInput
	}
	if _, err := domain.ParseRevisionID(record.ToSnapshotID.String()); err != nil || record.FromSnapshotID == record.ToSnapshotID {
		return changeplan.ErrInvalidInput
	}
	if record.RuleVersion != changeplan.RuleVersion || record.ResultSchemaVersion != changeplan.ResultSchemaVersion ||
		record.State.Validate() != nil || record.Risk.Validate() != nil || record.ItemCount < 0 || record.ItemCount > changeplan.MaximumItems ||
		record.BlockedCount < 0 || record.BlockedCount > record.ItemCount || record.CreatedAt.IsZero() || record.CreatedAt.Location() != time.UTC {
		return changeplan.ErrInvalidInput
	}
	if record.State == domain.ChangePlanBlocked && (record.Risk != domain.ChangeRiskBlocked || record.BlockedCount == 0) ||
		record.State == domain.ChangePlanReady && (record.Risk == domain.ChangeRiskBlocked || record.BlockedCount != 0) {
		return changeplan.ErrInvalidInput
	}
	if len(record.ResultJSON) > revision.MaxSnapshotBytes || !json.Valid(record.ResultJSON) {
		return changeplan.ErrInvalidInput
	}
	digest := sha256.Sum256(record.ResultJSON)
	if hex.EncodeToString(digest[:]) != record.ResultDigest {
		return changeplan.ErrInvalidInput
	}
	var result changeplan.Result
	if json.Unmarshal(record.ResultJSON, &result) != nil || result.SchemaVersion != record.ResultSchemaVersion ||
		result.RuleVersion != record.RuleVersion || result.State != record.State || result.Risk != record.Risk ||
		result.BlockedCount != record.BlockedCount || len(result.Items) != record.ItemCount {
		return changeplan.ErrInvalidInput
	}
	return nil
}
