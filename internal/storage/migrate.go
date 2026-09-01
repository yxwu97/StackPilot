package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const foreignKeysOffDirective = "-- stackpilot:foreign-keys-off"

var (
	// ErrMigrationChecksumMismatch indicates that an applied migration was edited.
	ErrMigrationChecksumMismatch = errors.New("migration checksum mismatch")
	// ErrMigrationHistoryInvalid indicates that applied versions are not a known prefix.
	ErrMigrationHistoryInvalid = errors.New("migration history is invalid")
)

var migrationFilenamePattern = regexp.MustCompile(`^([0-9]{6})_([a-z0-9_]+)\.sql$`)

//go:embed migrations/*.sql
var productionMigrations embed.FS

type migration struct {
	version  int64
	name     string
	contents string
	checksum string
}

type appliedMigration struct {
	version  int64
	name     string
	checksum string
}

type migrator struct {
	migrations []migration
	now        func() time.Time
}

func applyMigrations(ctx context.Context, database *sql.DB) error {
	migrator, err := newMigrator(productionMigrations, time.Now)
	if err != nil {
		return err
	}
	return migrator.apply(ctx, database)
}

func newMigrator(source fs.FS, now func() time.Time) (*migrator, error) {
	migrations, err := loadMigrations(source)
	if err != nil {
		return nil, err
	}
	if now == nil {
		return nil, fmt.Errorf("migration clock is required")
	}
	return &migrator{migrations: migrations, now: now}, nil
}

func loadMigrations(source fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(source, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}
	result := make([]migration, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		loaded, err := loadMigration(source, entry.Name())
		if err != nil {
			return nil, err
		}
		result = append(result, loaded)
	}
	sort.Slice(result, func(left, right int) bool { return result[left].version < result[right].version })
	if err := validateMigrationVersions(result); err != nil {
		return nil, err
	}
	return result, nil
}

func loadMigration(source fs.FS, filename string) (migration, error) {
	matches := migrationFilenamePattern.FindStringSubmatch(filename)
	if matches == nil {
		return migration{}, fmt.Errorf("invalid migration filename %q", filename)
	}
	version, err := strconv.ParseInt(matches[1], 10, 64)
	if err != nil || version == 0 {
		return migration{}, fmt.Errorf("invalid migration version in %q", filename)
	}
	contents, err := fs.ReadFile(source, "migrations/"+filename)
	if err != nil {
		return migration{}, fmt.Errorf("read migration %q: %w", filename, err)
	}
	digest := sha256.Sum256(contents)
	return migration{version: version, name: matches[2], contents: string(contents), checksum: fmt.Sprintf("%x", digest)}, nil
}

func validateMigrationVersions(migrations []migration) error {
	for index := 1; index < len(migrations); index++ {
		if migrations[index-1].version >= migrations[index].version {
			return fmt.Errorf("%w: duplicate or unordered version %d", ErrMigrationHistoryInvalid, migrations[index].version)
		}
	}
	return nil
}

func (m *migrator) apply(ctx context.Context, database *sql.DB) error {
	if err := ensureMigrationTable(ctx, database); err != nil {
		return err
	}
	applied, err := readAppliedMigrations(ctx, database)
	if err != nil {
		return err
	}
	if err := m.validateApplied(applied); err != nil {
		return err
	}
	for _, pending := range m.migrations[len(applied):] {
		if err := m.applyOne(ctx, database, pending); err != nil {
			return err
		}
	}
	return nil
}

func ensureMigrationTable(ctx context.Context, database *sql.DB) error {
	const statement = `CREATE TABLE IF NOT EXISTS schema_migrations (
        version INTEGER PRIMARY KEY,
        name TEXT NOT NULL,
        checksum TEXT NOT NULL,
        applied_at TEXT NOT NULL
    ) STRICT`
	if _, err := database.ExecContext(ctx, statement); err != nil {
		return fmt.Errorf("create migration table: %w", err)
	}
	return nil
}

func readAppliedMigrations(ctx context.Context, database *sql.DB) ([]appliedMigration, error) {
	rows, err := database.QueryContext(ctx, `SELECT version, name, checksum FROM schema_migrations ORDER BY version`)
	if err != nil {
		return nil, fmt.Errorf("query migration history: %w", err)
	}
	defer rows.Close()
	var result []appliedMigration
	for rows.Next() {
		var item appliedMigration
		if err := rows.Scan(&item.version, &item.name, &item.checksum); err != nil {
			return nil, fmt.Errorf("scan migration history: %w", err)
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate migration history: %w", err)
	}
	return result, nil
}

func (m *migrator) validateApplied(applied []appliedMigration) error {
	if len(applied) > len(m.migrations) {
		return fmt.Errorf("%w: database version %d is newer than supported", ErrMigrationHistoryInvalid, applied[len(applied)-1].version)
	}
	for index, current := range applied {
		expected := m.migrations[index]
		if current.version != expected.version || current.name != expected.name {
			return fmt.Errorf("%w: unexpected migration %d_%s", ErrMigrationHistoryInvalid, current.version, current.name)
		}
		if current.checksum != expected.checksum {
			return fmt.Errorf("%w: version %d", ErrMigrationChecksumMismatch, current.version)
		}
	}
	return nil
}

func (m *migrator) applyOne(ctx context.Context, database *sql.DB, item migration) (err error) {
	if strings.HasPrefix(item.contents, foreignKeysOffDirective+"\n") {
		return m.applyOneWithForeignKeysDisabled(ctx, database, item)
	}
	return m.applyOneTransaction(ctx, database, item, false)
}

type migrationBeginner interface {
	BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
}

func (m *migrator) applyOneWithForeignKeysDisabled(ctx context.Context, database *sql.DB, item migration) (err error) {
	connection, err := database.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration %d connection: %w", item.version, err)
	}
	defer func() {
		if closeErr := connection.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close migration %d connection: %w", item.version, closeErr))
		}
	}()
	if _, err = connection.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		return fmt.Errorf("disable foreign keys for migration %d: %w", item.version, err)
	}
	defer func() {
		if _, enableErr := connection.ExecContext(context.WithoutCancel(ctx), `PRAGMA foreign_keys=ON`); enableErr != nil {
			err = errors.Join(err, fmt.Errorf("restore foreign keys after migration %d: %w", item.version, enableErr))
		}
	}()
	return m.applyOneTransaction(ctx, connection, item, true)
}

func (m *migrator) applyOneTransaction(ctx context.Context, beginner migrationBeginner, item migration, checkForeignKeys bool) (err error) {
	transaction, err := beginner.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %d: %w", item.version, err)
	}
	defer func() {
		if err != nil {
			if rollbackErr := transaction.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
				err = errors.Join(err, fmt.Errorf("rollback migration %d: %w", item.version, rollbackErr))
			}
		}
	}()
	if _, err = transaction.ExecContext(ctx, item.contents); err != nil {
		return fmt.Errorf("execute migration %d_%s: %w", item.version, item.name, err)
	}
	if checkForeignKeys {
		if err = checkMigrationForeignKeys(ctx, transaction); err != nil {
			return fmt.Errorf("validate migration %d foreign keys: %w", item.version, err)
		}
	}
	appliedAt := m.now().UTC().Format(time.RFC3339Nano)
	if _, err = transaction.ExecContext(ctx, `INSERT INTO schema_migrations(version, name, checksum, applied_at) VALUES (?, ?, ?, ?)`, item.version, item.name, item.checksum, appliedAt); err != nil {
		return fmt.Errorf("record migration %d: %w", item.version, err)
	}
	if err = transaction.Commit(); err != nil {
		return fmt.Errorf("commit migration %d: %w", item.version, err)
	}
	return nil
}

func checkMigrationForeignKeys(ctx context.Context, transaction *sql.Tx) error {
	rows, err := transaction.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		var table string
		var rowID sql.NullInt64
		var parent string
		var foreignKeyID int
		if err := rows.Scan(&table, &rowID, &parent, &foreignKeyID); err != nil {
			return err
		}
		return fmt.Errorf("table %s row %v references %s foreign key %d", table, rowID, parent, foreignKeyID)
	}
	return rows.Err()
}
