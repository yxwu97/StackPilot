package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"stackpilot/internal/domain"
	"stackpilot/internal/workspace"
)

// WorkspaceRepository persists the Phase 1A workspace catalog in SQLite.
type WorkspaceRepository struct {
	database *sql.DB
}

// NewWorkspaceRepository constructs a catalog repository over a migrated database.
func NewWorkspaceRepository(database *sql.DB) (*WorkspaceRepository, error) {
	if database == nil {
		return nil, fmt.Errorf("workspace repository database is required")
	}
	return &WorkspaceRepository{database: database}, nil
}

// Register atomically persists the initial valid workspace snapshot.
func (repository *WorkspaceRepository) Register(ctx context.Context, registration workspace.Registration) (*workspace.Record, error) {
	err := executeTransaction(ctx, repository.database, func(transaction *sql.Tx) error {
		if err := upsertSystem(ctx, transaction, registration.Snapshot); err != nil {
			return err
		}
		if err := insertSnapshot(ctx, transaction, registration.Snapshot); err != nil {
			return err
		}
		if err := insertWorkspace(ctx, transaction, registration); err != nil {
			return err
		}
		return replaceServices(ctx, transaction, registration.ID, registration.Snapshot.Services)
	})
	if err != nil {
		return nil, err
	}
	return repository.Get(ctx, registration.ID)
}

// Refresh atomically installs a new valid snapshot and service summary set.
func (repository *WorkspaceRepository) Refresh(ctx context.Context, id domain.WorkspaceID, snapshot workspace.Snapshot) (*workspace.Record, error) {
	err := executeTransaction(ctx, repository.database, func(transaction *sql.Tx) error {
		if err := requireWorkspaceSystem(ctx, transaction, id, snapshot.SystemID); err != nil {
			return err
		}
		if err := upsertSystem(ctx, transaction, snapshot); err != nil {
			return err
		}
		if err := insertSnapshot(ctx, transaction, snapshot); err != nil {
			return err
		}
		if err := updateWorkspaceSnapshot(ctx, transaction, id, snapshot); err != nil {
			return err
		}
		return replaceServices(ctx, transaction, id, snapshot.Services)
	})
	if err != nil {
		return nil, err
	}
	return repository.Get(ctx, id)
}

// MarkInvalid records a refresh failure without replacing the last valid snapshot.
func (repository *WorkspaceRepository) MarkInvalid(ctx context.Context, id domain.WorkspaceID, code string, updatedAt time.Time) error {
	result, err := repository.database.ExecContext(ctx, `UPDATE workspaces
        SET manifest_status = 'invalid', last_error_code = ?, updated_at = ? WHERE id = ?`,
		code, formatDatabaseTime(updatedAt), id.String())
	if err != nil {
		return fmt.Errorf("mark workspace manifest invalid: %w", err)
	}
	return requireAffectedWorkspace(result)
}

// Get returns one workspace registration and its system summary.
func (repository *WorkspaceRepository) Get(ctx context.Context, id domain.WorkspaceID) (*workspace.Record, error) {
	row := repository.database.QueryRowContext(ctx, workspaceSelect+` WHERE w.id = ?`, id.String())
	record, err := scanWorkspace(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, workspace.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get workspace: %w", err)
	}
	return &record, nil
}

// List returns workspace registrations ordered deterministically.
func (repository *WorkspaceRepository) List(ctx context.Context) ([]workspace.Record, error) {
	rows, err := repository.database.QueryContext(ctx, workspaceSelect+` ORDER BY w.created_at, w.id`)
	if err != nil {
		return nil, fmt.Errorf("list workspaces: %w", err)
	}
	defer rows.Close()
	result := make([]workspace.Record, 0)
	for rows.Next() {
		record, err := scanWorkspace(rows)
		if err != nil {
			return nil, fmt.Errorf("scan workspace: %w", err)
		}
		result = append(result, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate workspaces: %w", err)
	}
	return result, nil
}

// Definition loads the last valid snapshot and persisted service summaries.
func (repository *WorkspaceRepository) Definition(ctx context.Context, id domain.WorkspaceID) (*workspace.Definition, error) {
	record, err := repository.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if record.LastValidDigest == "" {
		return nil, fmt.Errorf("workspace has no valid manifest snapshot")
	}
	manifestView, err := repository.getManifestView(ctx, record.LastValidDigest)
	if err != nil {
		return nil, err
	}
	services, err := repository.listServiceDefinitions(ctx, id)
	if err != nil {
		return nil, err
	}
	return &workspace.Definition{Workspace: *record, Manifest: manifestView, Services: services}, nil
}

// Delete removes catalog data without accessing the registered workspace path.
func (repository *WorkspaceRepository) Delete(ctx context.Context, id domain.WorkspaceID) error {
	return executeImmediate(ctx, repository.database, func(connection *sql.Conn) error {
		var systemID string
		if err := connection.QueryRowContext(ctx, `SELECT system_id FROM workspaces WHERE id = ?`, id.String()).Scan(&systemID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return workspace.ErrNotFound
			}
			return fmt.Errorf("find workspace for deletion: %w", err)
		}
		if err := requireInactiveWorkspace(ctx, connection, id); err != nil {
			return err
		}
		if err := deleteWorkspaceHistory(ctx, connection, id); err != nil {
			return err
		}
		if _, err := connection.ExecContext(ctx, `DELETE FROM workspaces WHERE id = ?`, id.String()); err != nil {
			return fmt.Errorf("delete workspace: %w", err)
		}
		return cleanupSystemCatalog(ctx, connection, systemID)
	})
}

func requireInactiveWorkspace(ctx context.Context, connection *sql.Conn, id domain.WorkspaceID) error {
	var active int
	err := connection.QueryRowContext(ctx, `SELECT EXISTS (
        SELECT 1 FROM operations WHERE workspace_id = ? AND state IN ('queued', 'running', 'cancelling')
        UNION ALL
        SELECT 1 FROM system_instances WHERE workspace_id = ? AND state <> 'stopped'
        UNION ALL
        SELECT 1 FROM workspace_import_operations WHERE workspace_id = ? AND state IN ('queued', 'running')
    )`, id.String(), id.String(), id.String()).Scan(&active)
	if err != nil {
		return fmt.Errorf("check workspace runtime before deletion: %w", err)
	}
	if active != 0 {
		return workspace.ErrUnregisterRuntimeActive
	}
	return nil
}

func deleteWorkspaceHistory(ctx context.Context, connection *sql.Conn, id domain.WorkspaceID) error {
	statements := []string{
		`DELETE FROM workspace_import_operations WHERE workspace_id = ?
            OR draft_id IN (SELECT id FROM workspace_drafts WHERE workspace_id = ?)`,
		`DELETE FROM workspace_drafts WHERE workspace_id = ?`,
		`DELETE FROM operations WHERE workspace_id = ?`,
		`DELETE FROM system_instances WHERE workspace_id = ?`,
	}
	for _, statement := range statements {
		arguments := []any{id.String()}
		if strings.Count(statement, "?") == 2 {
			arguments = append(arguments, id.String())
		}
		if _, err := connection.ExecContext(ctx, statement, arguments...); err != nil {
			return fmt.Errorf("delete workspace history: %w", err)
		}
	}
	return nil
}

// Relink atomically installs the target snapshot and changes only the catalog path.
func (repository *WorkspaceRepository) Relink(ctx context.Context, relink workspace.Relink) (*workspace.Record, error) {
	err := executeTransaction(ctx, repository.database, func(transaction *sql.Tx) error {
		if err := requireWorkspaceSystem(ctx, transaction, relink.ID, relink.Snapshot.SystemID); err != nil {
			return err
		}
		if err := upsertSystem(ctx, transaction, relink.Snapshot); err != nil {
			return err
		}
		if err := insertSnapshot(ctx, transaction, relink.Snapshot); err != nil {
			return err
		}
		result, err := transaction.ExecContext(ctx, `UPDATE workspaces SET root_path=?, canonical_path=?,
            manifest_status='valid', last_valid_digest=?, last_error_code=NULL, updated_at=? WHERE id=?`,
			relink.RootPath, relink.CanonicalPath, relink.Snapshot.Digest,
			formatDatabaseTime(relink.Snapshot.CreatedAt), relink.ID.String())
		if err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "unique") {
				return workspace.ErrAlreadyRegistered
			}
			return fmt.Errorf("relink workspace: %w", err)
		}
		if err := requireAffectedWorkspace(result); err != nil {
			return err
		}
		return replaceServices(ctx, transaction, relink.ID, relink.Snapshot.Services)
	})
	if err != nil {
		return nil, err
	}
	return repository.Get(ctx, relink.ID)
}

const workspaceSelect = `SELECT w.id, w.system_id, s.name, w.root_path, w.canonical_path,
    w.manifest_status, w.last_valid_digest, w.last_error_code, w.created_at, w.updated_at,
    (SELECT COUNT(*) FROM services service WHERE service.workspace_id = w.id)
    FROM workspaces w JOIN systems s ON s.id = w.system_id`

type rowScanner interface {
	Scan(...any) error
}

func scanWorkspace(scanner rowScanner) (workspace.Record, error) {
	var record workspace.Record
	var id, systemID, createdAt, updatedAt string
	var digest, errorCode sql.NullString
	err := scanner.Scan(&id, &systemID, &record.SystemName, &record.RootPath, &record.CanonicalPath,
		&record.ManifestStatus, &digest, &errorCode, &createdAt, &updatedAt, &record.ServiceCount)
	if err != nil {
		return workspace.Record{}, err
	}
	record.ID = domain.WorkspaceID(id)
	record.SystemID = domain.SystemID(systemID)
	record.LastValidDigest, record.LastErrorCode = digest.String, errorCode.String
	record.CreatedAt, err = parseDatabaseTime(createdAt)
	if err != nil {
		return workspace.Record{}, err
	}
	record.UpdatedAt, err = parseDatabaseTime(updatedAt)
	return record, err
}

func (repository *WorkspaceRepository) getManifestView(ctx context.Context, digest string) (workspace.ManifestView, error) {
	var view workspace.ManifestView
	var createdAt string
	err := repository.database.QueryRowContext(ctx, `SELECT digest, api_version, normalized_yaml, parsed_json, created_at
        FROM manifest_snapshots WHERE digest = ?`, digest).Scan(
		&view.Digest, &view.APIVersion, &view.NormalizedYAML, &view.ParsedJSON, &createdAt)
	if err != nil {
		return workspace.ManifestView{}, fmt.Errorf("read manifest snapshot: %w", err)
	}
	view.CreatedAt, err = parseDatabaseTime(createdAt)
	return view, err
}

// ManifestByDigest returns one immutable manifest snapshot by its content digest.
func (repository *WorkspaceRepository) ManifestByDigest(ctx context.Context, digest string) (workspace.ManifestView, error) {
	if len(digest) != 64 {
		return workspace.ManifestView{}, workspace.ErrNotFound
	}
	view, err := repository.getManifestView(ctx, digest)
	if errors.Is(err, sql.ErrNoRows) {
		return workspace.ManifestView{}, workspace.ErrNotFound
	}
	return view, err
}

func (repository *WorkspaceRepository) listServiceDefinitions(ctx context.Context, id domain.WorkspaceID) ([]workspace.ServiceDefinition, error) {
	rows, err := repository.database.QueryContext(ctx, `SELECT service_id, driver, mode, required, definition_digest
        FROM services WHERE workspace_id = ? ORDER BY service_id`, id.String())
	if err != nil {
		return nil, fmt.Errorf("list service definitions: %w", err)
	}
	defer rows.Close()
	result := make([]workspace.ServiceDefinition, 0)
	for rows.Next() {
		var service workspace.ServiceDefinition
		var serviceID, driver, mode string
		var required int
		if err := rows.Scan(&serviceID, &driver, &mode, &required, &service.DefinitionDigest); err != nil {
			return nil, fmt.Errorf("scan service definition: %w", err)
		}
		service.ID, service.Driver, service.Mode = domain.ServiceID(serviceID), domain.DriverKind(driver), domain.ProcessMode(mode)
		service.Required = required == 1
		result = append(result, service)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate service definitions: %w", err)
	}
	return result, nil
}

func executeTransaction(ctx context.Context, database *sql.DB, action func(*sql.Tx) error) (err error) {
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin catalog transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			if rollbackErr := transaction.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
				err = errors.Join(err, fmt.Errorf("rollback catalog transaction: %w", rollbackErr))
			}
		}
	}()
	if err := action(transaction); err != nil {
		return err
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit catalog transaction: %w", err)
	}
	committed = true
	return nil
}

func upsertSystem(ctx context.Context, transaction *sql.Tx, snapshot workspace.Snapshot) error {
	_, err := transaction.ExecContext(ctx, `INSERT INTO systems (id, name, current_digest, created_at, updated_at)
        VALUES (?, ?, ?, ?, ?)
        ON CONFLICT(id) DO UPDATE SET name = excluded.name, current_digest = excluded.current_digest,
            updated_at = excluded.updated_at`, snapshot.SystemID.String(), snapshot.SystemName, snapshot.Digest,
		formatDatabaseTime(snapshot.CreatedAt), formatDatabaseTime(snapshot.CreatedAt))
	if err != nil {
		return fmt.Errorf("upsert system: %w", err)
	}
	return nil
}

func insertSnapshot(ctx context.Context, transaction *sql.Tx, snapshot workspace.Snapshot) error {
	_, err := transaction.ExecContext(ctx, `INSERT INTO manifest_snapshots
        (digest, system_id, api_version, normalized_yaml, parsed_json, created_at)
        VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT(digest) DO NOTHING`, snapshot.Digest,
		snapshot.SystemID.String(), snapshot.APIVersion, snapshot.NormalizedYAML, snapshot.ParsedJSON,
		formatDatabaseTime(snapshot.CreatedAt))
	if err != nil {
		return fmt.Errorf("insert manifest snapshot: %w", err)
	}
	var systemID string
	if err := transaction.QueryRowContext(ctx, `SELECT system_id FROM manifest_snapshots WHERE digest = ?`, snapshot.Digest).Scan(&systemID); err != nil {
		return fmt.Errorf("verify manifest snapshot: %w", err)
	}
	if systemID != snapshot.SystemID.String() {
		return fmt.Errorf("manifest digest belongs to another system")
	}
	return nil
}

func insertWorkspace(ctx context.Context, transaction *sql.Tx, registration workspace.Registration) error {
	result, err := transaction.ExecContext(ctx, `INSERT INTO workspaces
        (id, system_id, root_path, canonical_path, manifest_status, last_valid_digest,
         last_error_code, created_at, updated_at) VALUES (?, ?, ?, ?, 'valid', ?, NULL, ?, ?)
        ON CONFLICT(canonical_path) DO NOTHING`, registration.ID.String(), registration.Snapshot.SystemID.String(),
		registration.RootPath, registration.CanonicalPath, registration.Snapshot.Digest,
		formatDatabaseTime(registration.Snapshot.CreatedAt), formatDatabaseTime(registration.Snapshot.CreatedAt))
	if err != nil {
		return fmt.Errorf("insert workspace: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect workspace insert: %w", err)
	}
	if affected == 0 {
		return workspace.ErrAlreadyRegistered
	}
	return nil
}

func replaceServices(ctx context.Context, transaction *sql.Tx, id domain.WorkspaceID, services []workspace.ServiceDefinition) error {
	if _, err := transaction.ExecContext(ctx, `DELETE FROM services WHERE workspace_id = ?`, id.String()); err != nil {
		return fmt.Errorf("delete service summaries: %w", err)
	}
	for _, service := range services {
		required := 0
		if service.Required {
			required = 1
		}
		_, err := transaction.ExecContext(ctx, `INSERT INTO services
            (workspace_id, service_id, driver, mode, required, definition_digest) VALUES (?, ?, ?, ?, ?, ?)`,
			id.String(), service.ID.String(), string(service.Driver), string(service.Mode), required, service.DefinitionDigest)
		if err != nil {
			return fmt.Errorf("insert service summary: %w", err)
		}
	}
	return nil
}

func requireWorkspaceSystem(ctx context.Context, transaction *sql.Tx, id domain.WorkspaceID, expected domain.SystemID) error {
	var actual string
	if err := transaction.QueryRowContext(ctx, `SELECT system_id FROM workspaces WHERE id = ?`, id.String()).Scan(&actual); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return workspace.ErrNotFound
		}
		return fmt.Errorf("read workspace system: %w", err)
	}
	if actual != expected.String() {
		return workspace.ErrSystemChanged
	}
	return nil
}

func updateWorkspaceSnapshot(ctx context.Context, transaction *sql.Tx, id domain.WorkspaceID, snapshot workspace.Snapshot) error {
	result, err := transaction.ExecContext(ctx, `UPDATE workspaces SET manifest_status = 'valid',
        last_valid_digest = ?, last_error_code = NULL, updated_at = ? WHERE id = ?`,
		snapshot.Digest, formatDatabaseTime(snapshot.CreatedAt), id.String())
	if err != nil {
		return fmt.Errorf("update workspace snapshot: %w", err)
	}
	return requireAffectedWorkspace(result)
}

func requireAffectedWorkspace(result sql.Result) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect workspace update: %w", err)
	}
	if affected == 0 {
		return workspace.ErrNotFound
	}
	return nil
}

type catalogExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func cleanupSystemCatalog(ctx context.Context, executor catalogExecutor, systemID string) error {
	if _, err := executor.ExecContext(ctx, `DELETE FROM manifest_snapshots WHERE system_id = ?
		AND NOT EXISTS (SELECT 1 FROM workspaces WHERE last_valid_digest = manifest_snapshots.digest)
		AND NOT EXISTS (SELECT 1 FROM system_instances WHERE manifest_digest = manifest_snapshots.digest)
		AND NOT EXISTS (SELECT 1 FROM resolved_system_specs WHERE manifest_digest = manifest_snapshots.digest)
		AND NOT EXISTS (SELECT 1 FROM sticky_port_history WHERE manifest_digest = manifest_snapshots.digest)
		AND NOT EXISTS (SELECT 1 FROM port_leases WHERE manifest_digest = manifest_snapshots.digest)`, systemID); err != nil {
		return fmt.Errorf("delete unreferenced manifest snapshots: %w", err)
	}
	var digest string
	err := executor.QueryRowContext(ctx, `SELECT last_valid_digest FROM workspaces
        WHERE system_id = ? ORDER BY updated_at DESC, id DESC LIMIT 1`, systemID).Scan(&digest)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := executor.ExecContext(ctx, `DELETE FROM systems WHERE id = ?`, systemID); err != nil {
			return fmt.Errorf("delete unreferenced system: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("select current system snapshot: %w", err)
	}
	if _, err := executor.ExecContext(ctx, `UPDATE systems SET current_digest = ? WHERE id = ?`, digest, systemID); err != nil {
		return fmt.Errorf("update current system snapshot: %w", err)
	}
	return nil
}

func formatDatabaseTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func parseDatabaseTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse database timestamp: %w", err)
	}
	return parsed.UTC(), nil
}
