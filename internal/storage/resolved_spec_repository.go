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
	"time"

	"stackpilot/internal/domain"
)

// ResolvedSpecRecord is one immutable persisted system-runtime snapshot.
type ResolvedSpecRecord struct {
	Digest         string
	WorkspaceID    domain.WorkspaceID
	ManifestDigest string
	JSON           []byte
	CreatedAt      time.Time
}

// ResolvedSpecRepository persists safe immutable resolved system specifications.
type ResolvedSpecRepository struct {
	database *sql.DB
}

// SaveResolvedSpec persists raw immutable values through the orchestrator-owned interface.
func (repository *ResolvedSpecRepository) SaveResolvedSpec(ctx context.Context, digest string, workspaceID domain.WorkspaceID, manifestDigest string, value []byte, createdAt time.Time) error {
	return repository.Save(ctx, ResolvedSpecRecord{
		Digest: digest, WorkspaceID: workspaceID, ManifestDigest: manifestDigest,
		JSON: append([]byte(nil), value...), CreatedAt: createdAt,
	})
}

// LoadResolvedSpec returns a defensive JSON copy for runtime stop/recovery semantics.
func (repository *ResolvedSpecRepository) LoadResolvedSpec(ctx context.Context, digest string) ([]byte, error) {
	record, err := repository.Get(ctx, digest)
	if err != nil {
		return nil, err
	}
	return append([]byte(nil), record.JSON...), nil
}

// NewResolvedSpecRepository constructs a resolved-spec repository.
func NewResolvedSpecRepository(database *sql.DB) (*ResolvedSpecRepository, error) {
	if database == nil {
		return nil, fmt.Errorf("resolved spec repository database is required")
	}
	return &ResolvedSpecRepository{database: database}, nil
}

// Save inserts an immutable snapshot or validates an identical replay.
func (repository *ResolvedSpecRepository) Save(ctx context.Context, record ResolvedSpecRecord) error {
	if err := validateResolvedSpecRecord(record); err != nil {
		return err
	}
	result, err := repository.database.ExecContext(ctx, `INSERT OR IGNORE INTO resolved_system_specs
        (digest,workspace_id,manifest_digest,spec_json,created_at) VALUES (?,?,?,?,?)`,
		record.Digest, record.WorkspaceID.String(), record.ManifestDigest, string(record.JSON), formatDatabaseTime(record.CreatedAt))
	if err != nil {
		return fmt.Errorf("save resolved system spec: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read resolved spec insert result: %w", err)
	}
	if affected == 1 {
		return nil
	}
	existing, err := repository.Get(ctx, record.Digest)
	if err != nil {
		return err
	}
	if existing.WorkspaceID != record.WorkspaceID || existing.ManifestDigest != record.ManifestDigest || !bytes.Equal(existing.JSON, record.JSON) {
		return fmt.Errorf("resolved system spec digest collision")
	}
	return nil
}

// Get returns one immutable snapshot by digest.
func (repository *ResolvedSpecRepository) Get(ctx context.Context, digest string) (*ResolvedSpecRecord, error) {
	var record ResolvedSpecRecord
	var workspaceID, specJSON, createdAt string
	err := repository.database.QueryRowContext(ctx, `SELECT digest,workspace_id,manifest_digest,spec_json,created_at
        FROM resolved_system_specs WHERE digest = ?`, digest).Scan(
		&record.Digest, &workspaceID, &record.ManifestDigest, &specJSON, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, sql.ErrNoRows
	}
	if err != nil {
		return nil, fmt.Errorf("get resolved system spec: %w", err)
	}
	record.WorkspaceID, record.JSON = domain.WorkspaceID(workspaceID), []byte(specJSON)
	record.CreatedAt, err = parseDatabaseTime(createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse resolved spec creation time: %w", err)
	}
	return &record, nil
}

func validateResolvedSpecRecord(record ResolvedSpecRecord) error {
	if _, err := domain.ParseWorkspaceID(record.WorkspaceID.String()); err != nil || len(record.ManifestDigest) != 64 || !record.CreatedAt.Equal(record.CreatedAt.UTC()) {
		return fmt.Errorf("resolved system spec metadata is invalid")
	}
	if !json.Valid(record.JSON) || len(record.JSON) == 0 {
		return fmt.Errorf("resolved system spec JSON is invalid")
	}
	digest := sha256.Sum256(record.JSON)
	if hex.EncodeToString(digest[:]) != record.Digest {
		return fmt.Errorf("resolved system spec digest is invalid")
	}
	return nil
}
