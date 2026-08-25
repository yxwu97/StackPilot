package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"stackpilot/internal/domain"
	"stackpilot/internal/security"
)

func TestSecretMetadataRepositoryPersistsMonotonicProjection(t *testing.T) {
	database := openTestDatabase(t)
	repository, err := NewSecretMetadataRepository(database)
	if err != nil {
		t.Fatal(err)
	}
	key := security.SecretKey{SystemID: domain.SystemID("aiws"), Name: "database-password"}
	first := security.SecretMetadata{
		Key: key, Provider: security.SecretProviderDPAPIFile, Version: 1,
		UpdatedAt: time.Date(2026, 8, 18, 3, 30, 0, 0, time.UTC),
	}
	if err := repository.PutSecretMetadata(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if err := repository.PutSecretMetadata(context.Background(), first); err != nil {
		t.Fatalf("idempotent PutSecretMetadata() error = %v", err)
	}
	second := first
	second.Version, second.UpdatedAt = 2, first.UpdatedAt.Add(time.Second)
	if err := repository.PutSecretMetadata(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if err := repository.PutSecretMetadata(context.Background(), first); !errors.Is(err, security.ErrSecretVersionConflict) {
		t.Fatalf("version rollback error = %v", err)
	}
	got, found, err := repository.GetSecretMetadata(context.Background(), key)
	if err != nil || !found || got != second {
		t.Fatalf("GetSecretMetadata() = (%+v, %t, %v)", got, found, err)
	}
	if err := repository.DeleteSecretMetadata(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	if _, found, err := repository.GetSecretMetadata(context.Background(), key); err != nil || found {
		t.Fatalf("GetSecretMetadata(after delete) = (found=%t, %v)", found, err)
	}
}

func TestSecretMetadataSchemaContainsNoValueColumn(t *testing.T) {
	database := openTestDatabase(t)
	rows, err := database.Query(`PRAGMA table_info(secret_metadata)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var index, notNull, primaryKey int
		var name, kind string
		var defaultValue any
		if err := rows.Scan(&index, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		if name == "value" || name == "secret" || name == "plaintext" {
			t.Fatalf("secret_metadata contains forbidden column %q", name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}
