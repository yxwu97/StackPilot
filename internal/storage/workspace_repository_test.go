package storage

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"stackpilot/internal/domain"
	"stackpilot/internal/manifest"
	workspaceapp "stackpilot/internal/workspace"
)

func TestWorkspaceRegistrationPersistsValidSnapshot(t *testing.T) {
	database := openTestDatabase(t)
	manager := newWorkspaceManager(t, database)
	root := createWorkspaceFixture(t, "sample", "Sample", "backend")

	record, err := manager.Register(context.Background(), root)
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if _, err := domain.ParseWorkspaceID(record.ID.String()); err != nil {
		t.Fatalf("workspace ID = %q: %v", record.ID, err)
	}
	if record.SystemID != "sample" || record.ManifestStatus != workspaceapp.ManifestValid || record.ServiceCount != 1 {
		t.Fatalf("registered workspace = %#v", record)
	}
	if len(record.LastValidDigest) != 64 || record.LastErrorCode != "" {
		t.Fatalf("manifest outcome = (%q, %q)", record.LastValidDigest, record.LastErrorCode)
	}
	assertCatalogCounts(t, database, 1, 1, 1, 1)
}

func TestWorkspaceRegistrationRejectsDuplicateCanonicalPathAtomically(t *testing.T) {
	database := openTestDatabase(t)
	manager := newWorkspaceManager(t, database)
	root := createWorkspaceFixture(t, "sample", "Sample", "backend")
	if _, err := manager.Register(context.Background(), root); err != nil {
		t.Fatalf("first Register() error = %v", err)
	}
	_, err := manager.Register(context.Background(), filepath.Join(root, "."))
	if !errors.Is(err, workspaceapp.ErrAlreadyRegistered) {
		t.Fatalf("second Register() error = %v, want ErrAlreadyRegistered", err)
	}
	assertCatalogCounts(t, database, 1, 1, 1, 1)
}

func TestWorkspaceRefreshReplacesSnapshotAndServiceSummaries(t *testing.T) {
	database := openTestDatabase(t)
	manager := newWorkspaceManager(t, database)
	root := createWorkspaceFixture(t, "sample", "Sample", "backend")
	registered, err := manager.Register(context.Background(), root)
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "web"), 0o700); err != nil {
		t.Fatalf("create refreshed service directory: %v", err)
	}
	writeManifestFixture(t, root, validWorkspaceManifest("sample", "Renamed", "web"))

	refreshed, err := manager.Refresh(context.Background(), registered.ID)
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if refreshed.LastValidDigest == registered.LastValidDigest || refreshed.SystemName != "Renamed" {
		t.Fatalf("refreshed workspace = %#v, original digest %q", refreshed, registered.LastValidDigest)
	}
	if refreshed.ManifestStatus != workspaceapp.ManifestValid || refreshed.ServiceCount != 1 {
		t.Fatalf("refreshed outcome = %#v", refreshed)
	}
	assertCatalogCounts(t, database, 1, 2, 1, 1)
	assertOnlyService(t, database, registered.ID.String(), "web")
}

func TestInvalidRefreshRetainsLastValidSnapshot(t *testing.T) {
	database := openTestDatabase(t)
	manager := newWorkspaceManager(t, database)
	root := createWorkspaceFixture(t, "sample", "Sample", "backend")
	registered, err := manager.Register(context.Background(), root)
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	invalid := strings.Replace(validWorkspaceManifest("sample", "Sample", "backend"),
		"      driver: process", "      driver: process\n      driver: process", 1)
	writeManifestFixture(t, root, invalid)

	_, err = manager.Refresh(context.Background(), registered.ID)
	if !errors.Is(err, manifest.ErrDuplicateKey) {
		t.Fatalf("Refresh(invalid) error = %v, want ErrDuplicateKey", err)
	}
	current, err := manager.Get(context.Background(), registered.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if current.ManifestStatus != workspaceapp.ManifestInvalid || current.LastErrorCode != manifest.CodeDuplicateKey {
		t.Fatalf("invalid refresh outcome = %#v", current)
	}
	if current.LastValidDigest != registered.LastValidDigest || current.ServiceCount != 1 {
		t.Fatalf("last valid snapshot was not retained: %#v", current)
	}
	assertCatalogCounts(t, database, 1, 1, 1, 1)
}

func TestRefreshMarksSystemIdentityChangeInvalid(t *testing.T) {
	database := openTestDatabase(t)
	manager := newWorkspaceManager(t, database)
	root := createWorkspaceFixture(t, "sample", "Sample", "backend")
	registered, err := manager.Register(context.Background(), root)
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	writeManifestFixture(t, root, validWorkspaceManifest("changed", "Changed", "backend"))
	_, err = manager.Refresh(context.Background(), registered.ID)
	if !errors.Is(err, workspaceapp.ErrSystemChanged) {
		t.Fatalf("Refresh(system changed) error = %v, want ErrSystemChanged", err)
	}
	current, err := manager.Get(context.Background(), registered.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if current.LastErrorCode != "WORKSPACE_SYSTEM_ID_CHANGED" || current.LastValidDigest != registered.LastValidDigest {
		t.Fatalf("system change outcome = %#v", current)
	}
}

func TestRefreshMissingManifestRetainsSnapshot(t *testing.T) {
	database := openTestDatabase(t)
	manager := newWorkspaceManager(t, database)
	root := createWorkspaceFixture(t, "sample", "Sample", "backend")
	registered, err := manager.Register(context.Background(), root)
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if err := os.Remove(filepath.Join(root, ".stackpilot", "system.yaml")); err != nil {
		t.Fatalf("remove manifest fixture: %v", err)
	}
	_, err = manager.Refresh(context.Background(), registered.ID)
	if !errors.Is(err, workspaceapp.ErrManifestUnavailable) {
		t.Fatalf("Refresh(missing) error = %v, want ErrManifestUnavailable", err)
	}
	current, err := manager.Get(context.Background(), registered.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if current.LastErrorCode != workspaceapp.CodeManifestUnavailable || current.LastValidDigest != registered.LastValidDigest {
		t.Fatalf("missing manifest outcome = %#v", current)
	}
}

func TestWorkspaceUnregisterOnlyDeletesCatalogData(t *testing.T) {
	database := openTestDatabase(t)
	manager := newWorkspaceManager(t, database)
	root := createWorkspaceFixture(t, "sample", "Sample", "backend")
	registered, err := manager.Register(context.Background(), root)
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if err := manager.Unregister(context.Background(), registered.ID); err != nil {
		t.Fatalf("Unregister() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".stackpilot", "system.yaml")); err != nil {
		t.Fatalf("workspace manifest was touched: %v", err)
	}
	if _, err := manager.Get(context.Background(), registered.ID); !errors.Is(err, workspaceapp.ErrNotFound) {
		t.Fatalf("Get(deleted) error = %v, want ErrNotFound", err)
	}
	assertCatalogCounts(t, database, 0, 0, 0, 0)
}

func TestWorkspaceUnregisterRejectsActiveRuntimeState(t *testing.T) {
	database := openTestDatabase(t)
	manager := newWorkspaceManager(t, database)
	root := createWorkspaceFixture(t, "sample", "Sample", "backend")
	registered, err := manager.Register(context.Background(), root)
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	insertWorkspaceOperation(t, database, registered, "running")

	err = manager.Unregister(context.Background(), registered.ID)
	if !errors.Is(err, workspaceapp.ErrUnregisterRuntimeActive) {
		t.Fatalf("Unregister(active) error = %v, want ErrUnregisterRuntimeActive", err)
	}
	if _, err := manager.Get(context.Background(), registered.ID); err != nil {
		t.Fatalf("active workspace was changed: %v", err)
	}
}

func TestWorkspaceUnregisterDeletesStoppedRuntimeHistory(t *testing.T) {
	database := openTestDatabase(t)
	manager := newWorkspaceManager(t, database)
	root := createWorkspaceFixture(t, "sample", "Sample", "backend")
	registered, err := manager.Register(context.Background(), root)
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	insertWorkspaceOperation(t, database, registered, "succeeded")
	insertStoppedSystemInstance(t, database, registered)
	insertCompletedWorkspaceEdit(t, database, registered)

	if err := manager.Unregister(context.Background(), registered.ID); err != nil {
		t.Fatalf("Unregister(stopped history) error = %v", err)
	}
	for _, table := range []string{"operations", "system_instances", "workspace_import_operations", "workspace_drafts", "workspaces"} {
		var count int
		if err := database.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("%s count = %d, want 0", table, count)
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".stackpilot", "system.yaml")); err != nil {
		t.Fatalf("workspace manifest was touched: %v", err)
	}
}

func insertCompletedWorkspaceEdit(t *testing.T, database *sql.DB, record *workspaceapp.Record) {
	t.Helper()
	draftID := "draft_00000000000000000000000000000000"
	targetKey := strings.Repeat("2", 64)
	_, err := database.Exec(`INSERT INTO workspace_drafts (
        id, kind, workspace_id, root_path, canonical_path, target_key, base_manifest_digest,
        state, draft_json, created_at, expires_at, applied_at
    ) VALUES (?, 'edit', ?, ?, ?, ?, ?, 'applied', '{}', ?, ?, ?)`,
		draftID, record.ID.String(), record.RootPath, record.CanonicalPath, targetKey, record.LastValidDigest,
		"2026-08-23T00:00:00Z", "2026-08-23T01:00:00Z", "2026-08-23T00:00:01Z")
	if err != nil {
		t.Fatalf("insert workspace edit draft: %v", err)
	}
	_, err = database.Exec(`INSERT INTO workspace_import_operations (
        id, draft_id, workspace_id, target_key, candidate_id, type, state,
        idempotency_subject, route_key, request_digest, created_at, finished_at, duration_ms
    ) VALUES (?, ?, ?, ?, 'edit', 'workspace-edit-apply', 'succeeded', 'test', 'test', ?, ?, ?, 1)`,
		"op_01ARZ3NDEKTSV4RRFFQ69G5FAV", draftID, record.ID.String(), targetKey,
		strings.Repeat("3", 64), "2026-08-23T00:00:00Z", "2026-08-23T00:00:01Z")
	if err != nil {
		t.Fatalf("insert workspace edit Operation: %v", err)
	}
}

func insertWorkspaceOperation(t *testing.T, database *sql.DB, record *workspaceapp.Record, state string) {
	t.Helper()
	finishedAt := any(nil)
	if state == "succeeded" {
		finishedAt = "2026-08-23T00:00:01Z"
	}
	_, err := database.Exec(`INSERT INTO operations (
        id, workspace_id, system_id, type, state, idempotency_subject, route_key,
        request_digest, cancellable, created_at, finished_at
    ) VALUES (?, ?, ?, 'start', ?, 'test', 'test', ?, 0, ?, ?)`,
		"op_01ARZ3NDEKTSV4RRFFQ69G5FAV", record.ID.String(), record.SystemID.String(), state,
		strings.Repeat("0", 64), "2026-08-23T00:00:00Z", finishedAt)
	if err != nil {
		t.Fatalf("insert workspace Operation: %v", err)
	}
}

func insertStoppedSystemInstance(t *testing.T, database *sql.DB, record *workspaceapp.Record) {
	t.Helper()
	_, err := database.Exec(`INSERT INTO system_instances (
        id, workspace_id, manifest_digest, resolved_spec_digest, state, started_at, stopped_at
    ) VALUES (?, ?, ?, ?, 'stopped', ?, ?)`,
		"si_01ARZ3NDEKTSV4RRFFQ69G5FAV", record.ID.String(), record.LastValidDigest,
		strings.Repeat("1", 64), "2026-08-23T00:00:00Z", "2026-08-23T00:00:01Z")
	if err != nil {
		t.Fatalf("insert stopped system instance: %v", err)
	}
}

func newWorkspaceManager(t *testing.T, database *sql.DB) *workspaceapp.Manager {
	t.Helper()
	loader, err := manifest.NewLoader()
	if err != nil {
		t.Fatalf("NewLoader() error = %v", err)
	}
	repository, err := NewWorkspaceRepository(database)
	if err != nil {
		t.Fatalf("NewWorkspaceRepository() error = %v", err)
	}
	manager, err := workspaceapp.NewManager(repository, loader, manifest.NewValidator())
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	return manager
}

func createWorkspaceFixture(t *testing.T, systemID, systemName, serviceID string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".stackpilot"), 0o700); err != nil {
		t.Fatalf("create manifest directory: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, serviceID), 0o700); err != nil {
		t.Fatalf("create service directory: %v", err)
	}
	writeManifestFixture(t, root, validWorkspaceManifest(systemID, systemName, serviceID))
	return root
}

func writeManifestFixture(t *testing.T, root, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, ".stackpilot", "system.yaml"), []byte(contents), 0o600); err != nil {
		t.Fatalf("write manifest fixture: %v", err)
	}
}

func validWorkspaceManifest(systemID, systemName, serviceID string) string {
	return "apiVersion: stackpilot.io/v1alpha1\nkind: System\nmetadata:\n  id: " + systemID + "\n  name: " + systemName +
		"\nspec:\n  services:\n    " + serviceID + ":\n      driver: process\n      runner: java\n" +
		"      workingDirectory: ./" + serviceID + "\n      arguments: []\n      readiness:\n        type: process\n"
}

func assertCatalogCounts(t *testing.T, database *sql.DB, systems, snapshots, workspaces, services int) {
	t.Helper()
	tables := []struct {
		name string
		want int
	}{{"systems", systems}, {"manifest_snapshots", snapshots}, {"workspaces", workspaces}, {"services", services}}
	for _, table := range tables {
		var count int
		if err := database.QueryRow(`SELECT COUNT(*) FROM ` + table.name).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table.name, err)
		}
		if count != table.want {
			t.Errorf("%s count = %d, want %d", table.name, count, table.want)
		}
	}
}

func assertOnlyService(t *testing.T, database *sql.DB, workspaceID, serviceID string) {
	t.Helper()
	var actual string
	if err := database.QueryRow(`SELECT service_id FROM services WHERE workspace_id = ?`, workspaceID).Scan(&actual); err != nil {
		t.Fatalf("query service summary: %v", err)
	}
	if actual != serviceID {
		t.Fatalf("service ID = %q, want %q", actual, serviceID)
	}
}
