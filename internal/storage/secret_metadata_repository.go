package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"stackpilot/internal/security"
)

// SecretMetadataRepository persists the non-sensitive Secret projection.
type SecretMetadataRepository struct {
	database *sql.DB
}

// NewSecretMetadataRepository constructs a repository over the migrated database.
func NewSecretMetadataRepository(database *sql.DB) (*SecretMetadataRepository, error) {
	if database == nil {
		return nil, fmt.Errorf("secret metadata repository database is required")
	}
	return &SecretMetadataRepository{database: database}, nil
}

// GetSecretMetadata returns one system-scoped metadata record.
func (repository *SecretMetadataRepository) GetSecretMetadata(ctx context.Context, key security.SecretKey) (security.SecretMetadata, bool, error) {
	var provider, updatedAt string
	var version int64
	err := repository.database.QueryRowContext(ctx, `SELECT provider, version, updated_at
        FROM secret_metadata WHERE system_id = ? AND name = ?`, key.SystemID.String(), key.Name).Scan(&provider, &version, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return security.SecretMetadata{}, false, nil
	}
	if err != nil {
		return security.SecretMetadata{}, false, fmt.Errorf("get secret metadata: %w", err)
	}
	timestamp, err := parseDatabaseTime(updatedAt)
	if err != nil {
		return security.SecretMetadata{}, false, fmt.Errorf("parse secret metadata timestamp: %w", err)
	}
	metadata := security.SecretMetadata{Key: key, Provider: provider, Version: version, UpdatedAt: timestamp}
	if err := security.ValidateSecretMetadata(metadata); err != nil {
		return security.SecretMetadata{}, false, fmt.Errorf("invalid stored secret metadata: %w", err)
	}
	return metadata, true, nil
}

// PutSecretMetadata inserts or advances one metadata version without rollback.
func (repository *SecretMetadataRepository) PutSecretMetadata(ctx context.Context, metadata security.SecretMetadata) error {
	if err := security.ValidateSecretMetadata(metadata); err != nil {
		return err
	}
	result, err := repository.database.ExecContext(ctx, `INSERT INTO secret_metadata
        (system_id, name, provider, version, updated_at) VALUES (?, ?, ?, ?, ?)
        ON CONFLICT(system_id, name) DO UPDATE SET provider = excluded.provider,
            version = excluded.version, updated_at = excluded.updated_at
        WHERE excluded.version > secret_metadata.version OR
            (excluded.version = secret_metadata.version AND excluded.provider = secret_metadata.provider
             AND excluded.updated_at = secret_metadata.updated_at)`,
		metadata.Key.SystemID.String(), metadata.Key.Name, metadata.Provider,
		metadata.Version, formatDatabaseTime(metadata.UpdatedAt))
	if err != nil {
		return fmt.Errorf("put secret metadata: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read secret metadata result: %w", err)
	}
	if affected != 1 {
		return security.ErrSecretVersionConflict
	}
	return nil
}

// DeleteSecretMetadata removes only the non-sensitive projection.
func (repository *SecretMetadataRepository) DeleteSecretMetadata(ctx context.Context, key security.SecretKey) error {
	if err := security.ValidateSecretKey(key); err != nil {
		return err
	}
	_, err := repository.database.ExecContext(ctx,
		`DELETE FROM secret_metadata WHERE system_id = ? AND name = ?`, key.SystemID.String(), key.Name)
	if err != nil {
		return fmt.Errorf("delete secret metadata: %w", err)
	}
	return nil
}

var _ security.SecretMetadataStore = (*SecretMetadataRepository)(nil)
