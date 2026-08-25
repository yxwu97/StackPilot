//go:build windows

package storage

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"stackpilot/internal/domain"
	"stackpilot/internal/security"
)

func TestSecretProviderNeverPersistsPlaintextInSQLiteOrDPAPIFile(t *testing.T) {
	dataDir := t.TempDir()
	database, err := OpenDataDir(context.Background(), dataDir)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := NewSecretMetadataRepository(database)
	if err != nil {
		t.Fatal(err)
	}
	providerValue, err := security.NewOSSecretProvider(dataDir, repository, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	key := security.SecretKey{SystemID: domain.SystemID("aiws"), Name: "database-password"}
	plaintext := []byte("unique-p2a01-plaintext-never-persist")
	if _, err := providerValue.Set(context.Background(), key, plaintext); err != nil {
		t.Fatal(err)
	}
	resolved, err := providerValue.Resolve(context.Background(), key)
	if err != nil || !bytes.Equal(resolved.Value, plaintext) {
		t.Fatalf("Resolve() = (%q, %v)", resolved.Value, err)
	}
	resolved.Clear()
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	assertFilesExcludePlaintext(t, dataDir, plaintext)
}

func assertFilesExcludePlaintext(t *testing.T, root string, plaintext []byte) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		payload, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(payload, plaintext) {
			t.Errorf("plaintext Secret found in %s", filepath.Base(path))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
