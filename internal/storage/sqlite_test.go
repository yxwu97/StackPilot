package storage

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenConfiguresSQLiteAndMigratesEmptyDatabase(t *testing.T) {
	database := openTestDatabase(t)
	checks := []struct {
		pragma string
		want   string
	}{
		{pragma: "journal_mode", want: "wal"},
		{pragma: "foreign_keys", want: "1"},
		{pragma: "busy_timeout", want: "5000"},
		{pragma: "synchronous", want: "1"},
	}
	for _, check := range checks {
		var value string
		if err := database.QueryRowContext(context.Background(), "PRAGMA "+check.pragma).Scan(&value); err != nil {
			t.Fatalf("read PRAGMA %s: %v", check.pragma, err)
		}
		if value != check.want {
			t.Errorf("PRAGMA %s = %q, want %q", check.pragma, value, check.want)
		}
	}
	if database.Stats().MaxOpenConnections != maxOpenConnections {
		t.Errorf("max open connections = %d, want %d", database.Stats().MaxOpenConnections, maxOpenConnections)
	}
	assertTableExists(t, database, "schema_migrations")
	assertTableExists(t, database, "systems")
	assertTableExists(t, database, "manifest_snapshots")
	assertTableExists(t, database, "workspaces")
	assertTableExists(t, database, "services")
	assertTableExists(t, database, "operations")
	assertTableExists(t, database, "operation_steps")
	assertTableExists(t, database, "system_instances")
	assertTableExists(t, database, "service_instances")
	assertTableExists(t, database, "log_segments")
	assertTableExists(t, database, "health_results")
	assertTableExists(t, database, "events")
	assertTableExists(t, database, "resolved_system_specs")
	assertTableExists(t, database, "workspace_port_overrides")
	assertTableExists(t, database, "sticky_port_history")
	assertTableExists(t, database, "port_leases")
	assertTableExists(t, database, "secret_metadata")
	assertTableExists(t, database, "service_instance_secret_versions")
	if err := CheckWritable(context.Background(), database); err != nil {
		t.Fatalf("CheckWritable() error = %v", err)
	}
}

func TestOpenRejectsRelativeDatabasePath(t *testing.T) {
	database, err := Open(context.Background(), "relative.db")
	if database != nil || err == nil {
		t.Fatalf("Open(relative path) = (%v, %v), want error", database, err)
	}
}

func TestOpenDataDirCreatesDatabase(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "nested", "data")
	database, err := OpenDataDir(context.Background(), dataDir)
	if err != nil {
		t.Fatalf("OpenDataDir() error = %v", err)
	}
	defer closeDatabase(t, database)
	if _, err := os.Stat(filepath.Join(dataDir, databaseFilename)); err != nil {
		t.Fatalf("stat created database: %v", err)
	}
}

func TestPragmasApplyToEveryPooledConnection(t *testing.T) {
	database := openTestDatabase(t)
	connections := make([]*sql.Conn, 0, maxOpenConnections)
	for index := 0; index < maxOpenConnections; index++ {
		connection, err := database.Conn(context.Background())
		if err != nil {
			t.Fatalf("acquire connection %d: %v", index, err)
		}
		connections = append(connections, connection)
		var foreignKeys int
		if err := connection.QueryRowContext(context.Background(), `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
			t.Fatalf("query connection %d foreign_keys: %v", index, err)
		}
		if foreignKeys != 1 {
			t.Errorf("connection %d foreign_keys = %d, want 1", index, foreignKeys)
		}
	}
	for _, connection := range connections {
		if err := connection.Close(); err != nil {
			t.Errorf("close pooled connection: %v", err)
		}
	}
}

func TestMigrationsAreIdempotentAcrossRepeatedOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "stackpilot.db")
	first, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("first Open() error = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first database: %v", err)
	}
	second, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("second Open() error = %v", err)
	}
	defer closeDatabase(t, second)
	var count int
	if err := second.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if count != 16 {
		t.Fatalf("migration count = %d, want 16", count)
	}
}

func TestForeignKeysAreEnforced(t *testing.T) {
	database := openTestDatabase(t)
	_, err := database.Exec(`INSERT INTO manifest_snapshots
        (digest, system_id, api_version, normalized_yaml, parsed_json, created_at)
        VALUES ('digest', 'missing', 'stackpilot.io/v1alpha1', '{}', '{}', '2026-08-17T00:00:00Z')`)
	if err == nil {
		t.Fatal("insert with missing system unexpectedly succeeded")
	}
}

func TestMigrationChecksumMismatchRefusesStartup(t *testing.T) {
	database := openTestDatabase(t)
	if _, err := database.Exec(`UPDATE schema_migrations SET checksum = 'edited' WHERE version = 1`); err != nil {
		t.Fatalf("corrupt migration checksum: %v", err)
	}
	err := applyMigrations(context.Background(), database)
	if !errors.Is(err, ErrMigrationChecksumMismatch) {
		t.Fatalf("applyMigrations() error = %v, want checksum mismatch", err)
	}
}

func openTestDatabase(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stackpilot.db")
	database, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { closeDatabase(t, database) })
	return database
}

func closeDatabase(t *testing.T, database *sql.DB) {
	t.Helper()
	if err := database.Close(); err != nil {
		t.Errorf("close database: %v", err)
	}
}

func assertTableExists(t *testing.T, database *sql.DB, name string) {
	t.Helper()
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type = 'table' AND name = ?`, name).Scan(&count); err != nil {
		t.Fatalf("query table %s: %v", name, err)
	}
	if count != 1 {
		t.Errorf("table %s count = %d, want 1", name, count)
	}
}
