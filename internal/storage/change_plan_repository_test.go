package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"stackpilot/internal/changeplan"
	"stackpilot/internal/domain"
	"stackpilot/internal/revision"
)

func TestChangePlanRepositorySavesAndReusesImmutableResult(t *testing.T) {
	database, record := prepareChangePlanRecord(t)
	repository, err := NewChangePlanRepository(database)
	if err != nil {
		t.Fatalf("NewChangePlanRepository() error = %v", err)
	}

	created, err := repository.SaveOrGet(context.Background(), record)
	if err != nil {
		t.Fatalf("SaveOrGet() error = %v", err)
	}
	replay := record
	replay.ID = "plan_01ARZ3NDEKTSV4RRFFQ69G5FAW"
	reused, err := repository.SaveOrGet(context.Background(), replay)
	if err != nil {
		t.Fatalf("SaveOrGet(replay) error = %v", err)
	}
	if created.ID != record.ID || reused.ID != record.ID {
		t.Fatalf("created/reused IDs = %q/%q, want %q", created.ID, reused.ID, record.ID)
	}
	loaded, err := repository.Get(context.Background(), record.ID)
	if err != nil || string(loaded.ResultJSON) != string(record.ResultJSON) {
		t.Fatalf("Get() = %#v, %v", loaded, err)
	}
}

func TestChangePlanRepositoryRejectsInvalidAndCorruptedRecords(t *testing.T) {
	database, record := prepareChangePlanRecord(t)
	repository, _ := NewChangePlanRepository(database)

	invalidJSON := record
	invalidJSON.ResultJSON = []byte("{")
	if _, err := repository.SaveOrGet(context.Background(), invalidJSON); !errors.Is(err, changeplan.ErrInvalidInput) {
		t.Fatalf("SaveOrGet(invalid JSON) error = %v", err)
	}
	invalidDigest := record
	invalidDigest.ResultDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := repository.SaveOrGet(context.Background(), invalidDigest); !errors.Is(err, changeplan.ErrInvalidInput) {
		t.Fatalf("SaveOrGet(invalid digest) error = %v", err)
	}

	if _, err := database.ExecContext(context.Background(), `UPDATE system_revision_snapshots SET snapshot_json='{}' WHERE id=?`, record.FromSnapshotID.String()); err != nil {
		t.Fatalf("corrupt revision fixture: %v", err)
	}
	if _, err := repository.SaveOrGet(context.Background(), record); err != nil {
		t.Fatalf("valid plan save unexpectedly depends on revision JSON decoding: %v", err)
	}
	if _, err := database.ExecContext(context.Background(), `UPDATE change_plans SET result_json='{}' WHERE id=?`, record.ID.String()); err != nil {
		t.Fatalf("corrupt plan fixture: %v", err)
	}
	if _, err := repository.Get(context.Background(), record.ID); !errors.Is(err, changeplan.ErrInvalidInput) {
		t.Fatalf("Get(corrupted result) error = %v", err)
	}
}

func TestChangePlanRepositoryEnforcesForeignKeys(t *testing.T) {
	database, record := prepareChangePlanRecord(t)
	repository, _ := NewChangePlanRepository(database)
	record.CreatedByOperationID = "op_01ARZ3NDEKTSV4RRFFQ69G5FAA"
	if _, err := repository.SaveOrGet(context.Background(), record); err == nil {
		t.Fatal("SaveOrGet() succeeded with a missing operation foreign key")
	}
}

func prepareChangePlanRecord(t *testing.T) (*sql.DB, changeplan.Record) {
	t.Helper()
	database := openTestDatabase(t)
	manager := newWorkspaceManager(t, database)
	workspaceRecord, err := manager.Register(context.Background(), createWorkspaceFixture(t, "sample", "Sample", "backend"))
	if err != nil {
		t.Fatalf("register workspace: %v", err)
	}
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	operationID := domain.OperationID("op_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	if _, err := database.ExecContext(context.Background(), `INSERT INTO operations
        (id,workspace_id,system_id,type,state,idempotency_subject,route_key,request_digest,cancellable,created_at,finished_at)
        VALUES (?,?,?,?,?,?,?,?,?,?,?)`, operationID.String(), workspaceRecord.ID.String(), workspaceRecord.SystemID.String(),
		domain.OperationChangePlan, domain.OperationSucceeded, "test", "change-plan:create",
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 0, formatDatabaseTime(now), formatDatabaseTime(now)); err != nil {
		t.Fatalf("insert operation: %v", err)
	}
	revisionRepository, _ := NewRevisionRepository(database)
	from := persistPlanRevision(t, revisionRepository, workspaceRecord.ID, workspaceRecord.SystemID,
		"rev_01ARZ3NDEKTSV4RRFFQ69G5FAV", domain.RevisionRunning, "running", workspaceRecord.LastValidDigest, now)
	to := persistPlanRevision(t, revisionRepository, workspaceRecord.ID, workspaceRecord.SystemID,
		"rev_01ARZ3NDEKTSV4RRFFQ69G5FAW", domain.RevisionWorkspace, "workspace", workspaceRecord.LastValidDigest, now.Add(time.Second))
	result := changeplan.Result{
		SchemaVersion: changeplan.ResultSchemaVersion, FromDigest: from.Digest, ToDigest: to.Digest,
		RuleVersion: changeplan.RuleVersion, State: domain.ChangePlanReady, Risk: domain.ChangeRiskInfo, Items: []changeplan.Item{},
	}
	encoded, _ := json.Marshal(result)
	digest := sha256.Sum256(encoded)
	return database, changeplan.Record{
		ID: "plan_01ARZ3NDEKTSV4RRFFQ69G5FAV", CreatedByOperationID: operationID, WorkspaceID: workspaceRecord.ID,
		SystemID: workspaceRecord.SystemID, FromSnapshotID: from.ID, ToSnapshotID: to.ID,
		RuleVersion: changeplan.RuleVersion, State: result.State, Risk: result.Risk,
		ResultSchemaVersion: changeplan.ResultSchemaVersion, ResultDigest: hex.EncodeToString(digest[:]), ResultJSON: encoded, CreatedAt: now,
	}
}

func persistPlanRevision(t *testing.T, repository *RevisionRepository, workspaceID domain.WorkspaceID, systemID domain.SystemID,
	id domain.RevisionID, kind domain.RevisionKind, marker, manifestDigest string, createdAt time.Time) revision.Record {
	t.Helper()
	snapshot := revision.Snapshot{
		SchemaVersion: revision.SchemaVersion, WorkspaceID: workspaceID, SystemID: systemID, Kind: kind,
		ManifestDigest: manifestDigest, Git: revision.GitFact{Status: revision.SourceAvailable, Revision: marker},
	}
	if kind == domain.RevisionRunning {
		instanceID := insertPlanSystemInstance(t, repository.database, workspaceID, manifestDigest, createdAt)
		snapshot.SystemInstanceID = &instanceID
		snapshot.ResolvedSpecDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	}
	encoded, digest, err := revision.Canonicalize(snapshot)
	if err != nil {
		t.Fatalf("canonicalize revision: %v", err)
	}
	record := revision.Record{ID: id, WorkspaceID: workspaceID, SystemID: systemID, SystemInstanceID: snapshot.SystemInstanceID,
		Kind: kind, SchemaVersion: revision.SchemaVersion, Digest: digest, JSON: encoded, CreatedAt: createdAt}
	if err := repository.Save(context.Background(), record); err != nil {
		t.Fatalf("save revision: %v", err)
	}
	return record
}

func insertPlanSystemInstance(t *testing.T, database *sql.DB, workspaceID domain.WorkspaceID,
	manifestDigest string, createdAt time.Time) domain.SystemInstanceID {
	t.Helper()
	id := domain.SystemInstanceID("si_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	_, err := database.ExecContext(context.Background(), `INSERT INTO system_instances
		(id,workspace_id,manifest_digest,resolved_spec_digest,state,started_at)
		VALUES (?,?,?,?,?,?)`, id.String(), workspaceID.String(), manifestDigest,
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", domain.SystemRunning, formatDatabaseTime(createdAt))
	if err != nil {
		t.Fatalf("insert system instance: %v", err)
	}
	return id
}
