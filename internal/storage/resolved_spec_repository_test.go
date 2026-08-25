package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"
)

func TestResolvedSpecRepositoryPersistsImmutableSnapshot(t *testing.T) {
	database := openTestDatabase(t)
	seedRuntimeCatalog(t, database)
	repository, err := NewResolvedSpecRepository(database)
	if err != nil {
		t.Fatal(err)
	}
	jsonValue := []byte(`{"schemaVersion":"stackpilot.resolved/v1alpha1"}`)
	digestValue := sha256.Sum256(jsonValue)
	record := ResolvedSpecRecord{
		Digest: hex.EncodeToString(digestValue[:]), WorkspaceID: "ws_01ARZ3NDEKTSV4RRFFQ69G5FAV",
		ManifestDigest: testRuntimeDigest, JSON: jsonValue,
		CreatedAt: time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC),
	}
	if err := repository.Save(context.Background(), record); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if err := repository.Save(context.Background(), record); err != nil {
		t.Fatalf("Save(replay) error = %v", err)
	}
	got, err := repository.Get(context.Background(), record.Digest)
	if err != nil || string(got.JSON) != string(record.JSON) || got.CreatedAt != record.CreatedAt {
		t.Fatalf("Get() = %#v, %v", got, err)
	}
}

func TestResolvedSpecRepositoryRejectsDigestMismatch(t *testing.T) {
	database := openTestDatabase(t)
	seedRuntimeCatalog(t, database)
	repository, _ := NewResolvedSpecRepository(database)
	err := repository.Save(context.Background(), ResolvedSpecRecord{
		Digest: testRuntimeDigest, WorkspaceID: "ws_01ARZ3NDEKTSV4RRFFQ69G5FAV", ManifestDigest: testRuntimeDigest,
		JSON: []byte(`{"different":true}`), CreatedAt: time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC),
	})
	if err == nil {
		t.Fatal("Save(digest mismatch) unexpectedly succeeded")
	}
	if _, err := repository.Get(context.Background(), testRuntimeDigest); err == nil {
		t.Fatal("invalid record was persisted")
	}
}
