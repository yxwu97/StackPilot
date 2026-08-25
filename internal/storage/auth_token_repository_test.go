package storage

import (
	"context"
	"strings"
	"testing"
	"time"

	"stackpilot/internal/security"
)

func TestAuthTokenRepositoryPersistsHashAndUsageMetadata(t *testing.T) {
	database := openTestDatabase(t)
	repository, err := NewAuthTokenRepository(database)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	record := security.TokenRecord{ID: "local", Hash: strings.Repeat("h", 96), CreatedAt: now}
	if err := repository.Create(context.Background(), record); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	got, found, err := repository.Active(context.Background())
	if err != nil || !found || got != record {
		t.Fatalf("Active() = (%#v, %t, %v)", got, found, err)
	}
	usedAt := now.Add(time.Hour)
	if err := repository.MarkUsed(context.Background(), record.ID, usedAt); err != nil {
		t.Fatalf("MarkUsed() error = %v", err)
	}
	var persisted string
	if err := database.QueryRow(`SELECT last_used_at FROM auth_tokens WHERE id = ?`, record.ID).Scan(&persisted); err != nil || persisted != usedAt.Format(time.RFC3339Nano) {
		t.Fatalf("last_used_at = (%q, %v)", persisted, err)
	}
	second := security.TokenRecord{ID: "second", Hash: strings.Repeat("x", 96), CreatedAt: now}
	if err := repository.Create(context.Background(), second); err == nil {
		t.Fatal("second active token unexpectedly inserted")
	}
}

func TestAuthTokenRepositoryCommitsPreparedRotationAtomically(t *testing.T) {
	database := openTestDatabase(t)
	repository, err := NewAuthTokenRepository(database)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	active := security.TokenRecord{ID: "local", Hash: strings.Repeat("a", 96), CreatedAt: now}
	pending := security.TokenRecord{ID: "local-next", Hash: strings.Repeat("b", 96), CreatedAt: now.Add(time.Minute)}
	if err := repository.Create(context.Background(), active); err != nil {
		t.Fatal(err)
	}
	if err := repository.PrepareRotation(context.Background(), pending); err != nil {
		t.Fatal(err)
	}
	prepared, found, err := repository.PendingRotation(context.Background())
	if err != nil || !found || prepared != pending {
		t.Fatalf("PendingRotation() = (%+v, %t, %v)", prepared, found, err)
	}
	if err := repository.CommitRotation(context.Background(), active.ID, pending, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	current, found, err := repository.Active(context.Background())
	if err != nil || !found || current != pending {
		t.Fatalf("Active() after rotation = (%+v, %t, %v)", current, found, err)
	}
	if _, found, err := repository.PendingRotation(context.Background()); err != nil || found {
		t.Fatalf("pending rotation after commit = (%t, %v)", found, err)
	}
	var revokedAt string
	if err := database.QueryRow(`SELECT revoked_at FROM auth_tokens WHERE id = ?`, active.ID).Scan(&revokedAt); err != nil || revokedAt == "" {
		t.Fatalf("prior token revoked_at = (%q, %v)", revokedAt, err)
	}
}
