// Package storage owns StackPilot SQLite connections, migrations, and repositories.
package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

const (
	maxOpenConnections = 4
	busyTimeoutMillis  = 5000
	databaseFilename   = "stackpilot.db"
)

// OpenDataDir creates and canonicalizes a data directory before opening its database.
func OpenDataDir(ctx context.Context, dataDir string) (*sql.DB, error) {
	if dataDir == "" || !filepath.IsAbs(dataDir) {
		return nil, fmt.Errorf("data directory must be absolute")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	canonicalDir, err := filepath.EvalSymlinks(dataDir)
	if err != nil {
		return nil, fmt.Errorf("canonicalize data directory: %w", err)
	}
	return Open(ctx, filepath.Join(canonicalDir, databaseFilename))
}

// Open creates a configured SQLite connection pool and applies pending migrations.
// The path must already have passed the caller's data-directory trust checks.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	if path == "" || !filepath.IsAbs(path) {
		return nil, fmt.Errorf("database path must be absolute")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	database, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		return nil, fmt.Errorf("open SQLite database: %w", err)
	}
	configurePool(database)
	if err := initialize(ctx, database); err != nil {
		if closeErr := database.Close(); closeErr != nil {
			return nil, errors.Join(err, fmt.Errorf("close SQLite database: %w", closeErr))
		}
		return nil, err
	}
	return database, nil
}

func initialize(ctx context.Context, database *sql.DB) error {
	if err := database.PingContext(ctx); err != nil {
		return fmt.Errorf("connect to SQLite database: %w", err)
	}
	if err := applyMigrations(ctx, database); err != nil {
		return fmt.Errorf("apply SQLite migrations: %w", err)
	}
	return nil
}

func configurePool(database *sql.DB) {
	database.SetMaxOpenConns(maxOpenConnections)
	database.SetMaxIdleConns(maxOpenConnections)
}

func sqliteDSN(path string) string {
	location := filepath.ToSlash(path)
	if filepath.VolumeName(path) != "" {
		location = "/" + location
	}
	endpoint := url.URL{Scheme: "file", Path: location}
	query := url.Values{}
	query.Add("_pragma", "journal_mode(WAL)")
	query.Add("_pragma", "foreign_keys(ON)")
	query.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", busyTimeoutMillis))
	query.Add("_pragma", "synchronous(NORMAL)")
	endpoint.RawQuery = query.Encode()
	return endpoint.String()
}

// CheckWritable acquires and releases a SQLite write reservation without changing data.
func CheckWritable(ctx context.Context, database *sql.DB) (err error) {
	connection, err := database.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire SQLite connection: %w", err)
	}
	defer func() {
		if closeErr := connection.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close SQLite connection: %w", closeErr))
		}
	}()
	if _, err := connection.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("begin SQLite write probe: %w", err)
	}
	if _, err := connection.ExecContext(context.WithoutCancel(ctx), "ROLLBACK"); err != nil {
		return fmt.Errorf("rollback SQLite write probe: %w", err)
	}
	return nil
}
