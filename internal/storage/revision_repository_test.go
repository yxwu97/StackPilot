package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"stackpilot/internal/domain"
	"stackpilot/internal/revision"
)

func TestRevisionRepositoryPersistsCanonicalSnapshotIdempotently(t *testing.T) {
	database := openTestDatabase(t)
	manager := newWorkspaceManager(t, database)
	workspaceRoot := createWorkspaceFixture(t, "sample", "Sample", "backend")
	workspaceRecord, err := manager.Register(context.Background(), workspaceRoot)
	if err != nil {
		t.Fatalf("register workspace: %v", err)
	}
	snapshot := revision.Snapshot{
		SchemaVersion: revision.SchemaVersion, WorkspaceID: workspaceRecord.ID, SystemID: workspaceRecord.SystemID,
		Kind: domain.RevisionWorkspace, ManifestDigest: workspaceRecord.LastValidDigest,
		Git: revision.GitFact{Status: revision.SourceNotRepo, Reason: "GIT_NOT_REPOSITORY"},
	}
	encoded, digest, err := revision.Canonicalize(snapshot)
	if err != nil {
		t.Fatalf("Canonicalize() error = %v", err)
	}
	repository, err := NewRevisionRepository(database)
	if err != nil {
		t.Fatalf("NewRevisionRepository() error = %v", err)
	}
	record := revision.Record{
		ID: "rev_01ARZ3NDEKTSV4RRFFQ69G5FAV", WorkspaceID: workspaceRecord.ID, SystemID: workspaceRecord.SystemID,
		Kind: domain.RevisionWorkspace, SchemaVersion: revision.SchemaVersion, Digest: digest, JSON: encoded,
		CreatedAt: time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC),
	}
	if err := repository.Save(context.Background(), record); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	replay := record
	replay.ID = "rev_01ARZ3NDEKTSV4RRFFQ69G5FAW"
	replay.CreatedAt = replay.CreatedAt.Add(time.Second)
	if err := repository.Save(context.Background(), replay); err != nil {
		t.Fatalf("Save(replay) error = %v", err)
	}
	list, err := repository.ListLatest(context.Background(), workspaceRecord.ID, domain.RevisionWorkspace, 10)
	if err != nil {
		t.Fatalf("ListLatest() error = %v", err)
	}
	if len(list) != 1 || list[0].ID != record.ID || list[0].Digest != digest {
		t.Fatalf("revision list = %#v", list)
	}
	loaded, err := repository.Get(context.Background(), record.ID)
	if err != nil || string(loaded.JSON) != string(encoded) {
		t.Fatalf("Get() = %#v, %v", loaded, err)
	}
	if _, err := repository.Get(context.Background(), replay.ID); !errors.Is(err, revision.ErrNotFound) {
		t.Fatalf("Get(replay ID) error = %v", err)
	}
}

func TestRevisionRepositoryRejectsNonCanonicalOrMismatchedRecord(t *testing.T) {
	database := openTestDatabase(t)
	repository, err := NewRevisionRepository(database)
	if err != nil {
		t.Fatalf("NewRevisionRepository() error = %v", err)
	}
	value := []byte(`{"schemaVersion":"revision/v1"}`)
	sum := sha256.Sum256(value)
	record := revision.Record{
		ID: "rev_01ARZ3NDEKTSV4RRFFQ69G5FAV", WorkspaceID: "ws_01ARZ3NDEKTSV4RRFFQ69G5FAV", SystemID: "sample",
		Kind: domain.RevisionWorkspace, SchemaVersion: revision.SchemaVersion, Digest: hex.EncodeToString(sum[:]), JSON: value,
		CreatedAt: time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC),
	}
	if err := repository.Save(context.Background(), record); !errors.Is(err, revision.ErrInvalidInput) {
		t.Fatalf("Save(invalid) error = %v", err)
	}
	if _, err := repository.ListLatest(context.Background(), record.WorkspaceID, record.Kind, 101); !errors.Is(err, revision.ErrInvalidInput) {
		t.Fatalf("ListLatest(over limit) error = %v", err)
	}
}
