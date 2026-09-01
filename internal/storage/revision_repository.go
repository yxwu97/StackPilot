package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"stackpilot/internal/domain"
	"stackpilot/internal/revision"
)

// RevisionRepository persists immutable canonical revision snapshots.
type RevisionRepository struct {
	database *sql.DB
}

// NewRevisionRepository constructs a repository over a migrated database.
func NewRevisionRepository(database *sql.DB) (*RevisionRepository, error) {
	if database == nil {
		return nil, fmt.Errorf("revision repository database is required")
	}
	return &RevisionRepository{database: database}, nil
}

// Save inserts one revision or verifies an idempotent digest match.
func (repository *RevisionRepository) Save(ctx context.Context, record revision.Record) error {
	if err := validateRevisionRecord(record); err != nil {
		return err
	}
	result, err := repository.database.ExecContext(ctx, `INSERT OR IGNORE INTO system_revision_snapshots
        (id,workspace_id,system_id,system_instance_id,kind,schema_version,digest,snapshot_json,created_at)
        VALUES (?,?,?,?,?,?,?,?,?)`, record.ID.String(), record.WorkspaceID.String(), record.SystemID.String(),
		nullableSystemInstanceID(record.SystemInstanceID), string(record.Kind), record.SchemaVersion, record.Digest,
		string(record.JSON), formatDatabaseTime(record.CreatedAt))
	if err != nil {
		return fmt.Errorf("save revision snapshot: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read revision insert result: %w", err)
	}
	if affected == 1 {
		return nil
	}
	existing, err := repository.getByDigest(ctx, record.Digest)
	if err != nil {
		return err
	}
	if !sameRevisionRecord(*existing, record) {
		return revision.ErrDigestCollision
	}
	return nil
}

// Get returns one validated revision by ID.
func (repository *RevisionRepository) Get(ctx context.Context, id domain.RevisionID) (*revision.Record, error) {
	if _, err := domain.ParseRevisionID(id.String()); err != nil {
		return nil, revision.ErrInvalidInput
	}
	return scanRevisionRecord(repository.database.QueryRowContext(ctx, revisionSelect+` WHERE id = ?`, id.String()))
}

// GetByDigest returns one validated revision by canonical digest.
func (repository *RevisionRepository) GetByDigest(ctx context.Context, digest string) (*revision.Record, error) {
	if !validRevisionDigest(digest) {
		return nil, revision.ErrInvalidInput
	}
	return repository.getByDigest(ctx, digest)
}

// ListLatest returns a bounded newest-first list for one workspace and kind.
func (repository *RevisionRepository) ListLatest(ctx context.Context, workspaceID domain.WorkspaceID, kind domain.RevisionKind, limit int) ([]revision.Record, error) {
	if _, err := domain.ParseWorkspaceID(workspaceID.String()); err != nil || kind.Validate() != nil || limit < 1 || limit > 100 {
		return nil, revision.ErrInvalidInput
	}
	rows, err := repository.database.QueryContext(ctx, revisionSelect+` WHERE workspace_id = ? AND kind = ? ORDER BY created_at DESC, id DESC LIMIT ?`, workspaceID.String(), string(kind), limit)
	if err != nil {
		return nil, fmt.Errorf("list revision snapshots: %w", err)
	}
	defer rows.Close()
	result := make([]revision.Record, 0, limit)
	for rows.Next() {
		record, err := scanRevisionRecord(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, *record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate revision snapshots: %w", err)
	}
	return result, nil
}

func (repository *RevisionRepository) getByDigest(ctx context.Context, digest string) (*revision.Record, error) {
	return scanRevisionRecord(repository.database.QueryRowContext(ctx, revisionSelect+` WHERE digest = ?`, digest))
}

const revisionSelect = `SELECT id,workspace_id,system_id,system_instance_id,kind,schema_version,digest,snapshot_json,created_at
    FROM system_revision_snapshots`

func scanRevisionRecord(scanner rowScanner) (*revision.Record, error) {
	var record revision.Record
	var id, workspaceID, systemID, kind, snapshotJSON, createdAt string
	var instanceID sql.NullString
	err := scanner.Scan(&id, &workspaceID, &systemID, &instanceID, &kind, &record.SchemaVersion, &record.Digest, &snapshotJSON, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, revision.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan revision snapshot: %w", err)
	}
	record.ID, record.WorkspaceID, record.SystemID = domain.RevisionID(id), domain.WorkspaceID(workspaceID), domain.SystemID(systemID)
	record.Kind, record.JSON = domain.RevisionKind(kind), []byte(snapshotJSON)
	if instanceID.Valid {
		value := domain.SystemInstanceID(instanceID.String)
		record.SystemInstanceID = &value
	}
	record.CreatedAt, err = parseDatabaseTime(createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse revision creation time: %w", err)
	}
	if err := validateRevisionRecord(record); err != nil {
		return nil, fmt.Errorf("validate persisted revision: %w", err)
	}
	return &record, nil
}

func validateRevisionRecord(record revision.Record) error {
	if _, err := domain.ParseRevisionID(record.ID.String()); err != nil {
		return revision.ErrInvalidInput
	}
	if !record.CreatedAt.Equal(record.CreatedAt.UTC()) || record.CreatedAt.IsZero() || len(record.JSON) > revision.MaxSnapshotBytes || !json.Valid(record.JSON) {
		return revision.ErrInvalidInput
	}
	digest := sha256.Sum256(record.JSON)
	if hex.EncodeToString(digest[:]) != record.Digest {
		return revision.ErrInvalidInput
	}
	var snapshot revision.Snapshot
	if err := json.Unmarshal(record.JSON, &snapshot); err != nil {
		return revision.ErrInvalidInput
	}
	canonical, canonicalDigest, err := revision.Canonicalize(snapshot)
	if err != nil || canonicalDigest != record.Digest || !bytes.Equal(canonical, record.JSON) {
		return revision.ErrInvalidInput
	}
	if snapshot.WorkspaceID != record.WorkspaceID || snapshot.SystemID != record.SystemID || snapshot.Kind != record.Kind || snapshot.SchemaVersion != record.SchemaVersion {
		return revision.ErrInvalidInput
	}
	if !sameOptionalInstance(snapshot.SystemInstanceID, record.SystemInstanceID) {
		return revision.ErrInvalidInput
	}
	return nil
}

func sameRevisionRecord(left, right revision.Record) bool {
	return left.WorkspaceID == right.WorkspaceID && left.SystemID == right.SystemID && left.Kind == right.Kind &&
		left.SchemaVersion == right.SchemaVersion && left.Digest == right.Digest && bytes.Equal(left.JSON, right.JSON) &&
		sameOptionalInstance(left.SystemInstanceID, right.SystemInstanceID)
}

func sameOptionalInstance(left, right *domain.SystemInstanceID) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func nullableSystemInstanceID(value *domain.SystemInstanceID) any {
	if value == nil {
		return nil
	}
	return value.String()
}

func validRevisionDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
