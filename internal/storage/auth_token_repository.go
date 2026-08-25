package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"stackpilot/internal/security"
)

// AuthTokenRepository persists local-token hashes without plaintext token material.
type AuthTokenRepository struct {
	database *sql.DB
}

// NewAuthTokenRepository constructs the local authentication repository.
func NewAuthTokenRepository(database *sql.DB) (*AuthTokenRepository, error) {
	if database == nil {
		return nil, fmt.Errorf("auth token repository database is required")
	}
	return &AuthTokenRepository{database: database}, nil
}

// Active returns the single non-revoked token, if initialized.
func (repository *AuthTokenRepository) Active(ctx context.Context) (security.TokenRecord, bool, error) {
	var record security.TokenRecord
	var createdAt string
	var lastUsedAt, revokedAt sql.NullString
	err := repository.database.QueryRowContext(ctx, `SELECT id, token_hash, created_at, last_used_at, revoked_at
        FROM auth_tokens WHERE revoked_at IS NULL`).Scan(&record.ID, &record.Hash, &createdAt, &lastUsedAt, &revokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return record, false, nil
	}
	if err != nil {
		return record, false, fmt.Errorf("read active auth token: %w", err)
	}
	return parseAuthTokenRecord(record, createdAt, lastUsedAt, revokedAt)
}

// Create inserts the first active token hash.
func (repository *AuthTokenRepository) Create(ctx context.Context, record security.TokenRecord) error {
	if record.ID == "" || record.Hash == "" || record.CreatedAt.IsZero() || record.CreatedAt.Location() != time.UTC {
		return fmt.Errorf("invalid auth token record")
	}
	_, err := repository.database.ExecContext(ctx, `INSERT INTO auth_tokens(id, token_hash, created_at) VALUES (?, ?, ?)`,
		record.ID, record.Hash, formatDatabaseTime(record.CreatedAt))
	if err != nil {
		return fmt.Errorf("create auth token record: %w", err)
	}
	return nil
}

// MarkUsed updates safe usage metadata at most once per minute.
func (repository *AuthTokenRepository) MarkUsed(ctx context.Context, id string, now time.Time) error {
	if id == "" || now.IsZero() || now.Location() != time.UTC {
		return fmt.Errorf("invalid auth token usage")
	}
	cutoff := now.Add(-time.Minute)
	_, err := repository.database.ExecContext(ctx, `UPDATE auth_tokens SET last_used_at = ?
        WHERE id = ? AND revoked_at IS NULL AND (last_used_at IS NULL OR last_used_at < ?)`,
		formatDatabaseTime(now), id, formatDatabaseTime(cutoff))
	if err != nil {
		return fmt.Errorf("mark auth token used: %w", err)
	}
	return nil
}

// PendingRotation returns the crash-recovery journal entry, if one exists.
func (repository *AuthTokenRepository) PendingRotation(ctx context.Context) (security.TokenRecord, bool, error) {
	var record security.TokenRecord
	var createdAt string
	err := repository.database.QueryRowContext(ctx, `SELECT token_id, token_hash, created_at
        FROM auth_token_rotation WHERE id = 'pending'`).Scan(&record.ID, &record.Hash, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return record, false, nil
	}
	if err != nil {
		return record, false, fmt.Errorf("read pending auth token rotation: %w", err)
	}
	record.CreatedAt, err = parseDatabaseTime(createdAt)
	return record, err == nil, err
}

// PrepareRotation durably records the next hash before replacing secure storage.
func (repository *AuthTokenRepository) PrepareRotation(ctx context.Context, record security.TokenRecord) error {
	if err := validateAuthTokenRecord(record); err != nil {
		return err
	}
	_, err := repository.database.ExecContext(ctx, `INSERT INTO auth_token_rotation
        (id, token_id, token_hash, created_at) VALUES ('pending', ?, ?, ?)`,
		record.ID, record.Hash, formatDatabaseTime(record.CreatedAt))
	if err != nil {
		return fmt.Errorf("prepare auth token rotation: %w", err)
	}
	return nil
}

// CommitRotation revokes the prior token, activates the pending hash, and clears the journal atomically.
func (repository *AuthTokenRepository) CommitRotation(ctx context.Context, currentID string, record security.TokenRecord, revokedAt time.Time) error {
	if currentID == "" || revokedAt.IsZero() || revokedAt.Location() != time.UTC {
		return fmt.Errorf("invalid current auth token rotation state")
	}
	if err := validateAuthTokenRecord(record); err != nil {
		return err
	}
	return executeImmediate(ctx, repository.database, func(connection *sql.Conn) error {
		result, err := connection.ExecContext(ctx, `UPDATE auth_tokens SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`,
			formatDatabaseTime(revokedAt), currentID)
		if err != nil {
			return fmt.Errorf("revoke prior auth token: %w", err)
		}
		if count, err := result.RowsAffected(); err != nil || count != 1 {
			return fmt.Errorf("prior active auth token changed during rotation")
		}
		if _, err := connection.ExecContext(ctx, `INSERT INTO auth_tokens(id, token_hash, created_at) VALUES (?, ?, ?)`,
			record.ID, record.Hash, formatDatabaseTime(record.CreatedAt)); err != nil {
			return fmt.Errorf("activate rotated auth token: %w", err)
		}
		cleared, err := connection.ExecContext(ctx, `DELETE FROM auth_token_rotation WHERE id = 'pending' AND token_id = ?`, record.ID)
		if err != nil {
			return fmt.Errorf("clear committed auth token rotation: %w", err)
		}
		if count, err := cleared.RowsAffected(); err != nil || count != 1 {
			return fmt.Errorf("pending auth token changed during rotation")
		}
		return nil
	})
}

// ClearRotation removes a preparation that never reached secure storage.
func (repository *AuthTokenRepository) ClearRotation(ctx context.Context) error {
	if _, err := repository.database.ExecContext(ctx, `DELETE FROM auth_token_rotation WHERE id = 'pending'`); err != nil {
		return fmt.Errorf("clear auth token rotation: %w", err)
	}
	return nil
}

func validateAuthTokenRecord(record security.TokenRecord) error {
	if record.ID == "" || record.Hash == "" || record.CreatedAt.IsZero() || record.CreatedAt.Location() != time.UTC {
		return fmt.Errorf("invalid auth token record")
	}
	return nil
}

func parseAuthTokenRecord(record security.TokenRecord, createdAt string, lastUsedAt, revokedAt sql.NullString) (security.TokenRecord, bool, error) {
	var err error
	record.CreatedAt, err = parseDatabaseTime(createdAt)
	if err != nil {
		return record, false, err
	}
	if _, err = parseNullableDatabaseTime(lastUsedAt); err != nil {
		return record, false, err
	}
	if _, err = parseNullableDatabaseTime(revokedAt); err != nil {
		return record, false, err
	}
	return record, true, nil
}
