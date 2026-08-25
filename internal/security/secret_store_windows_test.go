//go:build windows

package security

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"stackpilot/internal/domain"
)

func TestOSSecretProviderEncryptsAndRoundTripsWithDPAPI(t *testing.T) {
	dataDir := t.TempDir()
	metadata := &memorySecretMetadata{records: make(map[SecretKey]SecretMetadata)}
	providerValue, err := NewOSSecretProvider(dataDir, metadata, func() time.Time {
		return time.Date(2026, 8, 18, 3, 20, 0, 0, time.UTC)
	})
	if err != nil {
		t.Fatalf("NewOSSecretProvider() error = %v", err)
	}
	key := SecretKey{SystemID: domain.SystemID("aiws"), Name: "database-password"}
	plaintext := []byte("secret-value-that-must-not-be-written")
	created, err := providerValue.Set(context.Background(), key, plaintext)
	if err != nil || created.Version != 1 {
		t.Fatalf("Set() = (%+v, %v)", created, err)
	}
	entries, err := os.ReadDir(filepath.Join(dataDir, "secrets"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("read Secret directory = (%d, %v)", len(entries), err)
	}
	protected, err := os.ReadFile(filepath.Join(dataDir, "secrets", entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(protected, plaintext) || bytes.Contains(protected, []byte(key.Name)) {
		t.Fatal("DPAPI file contains plaintext Secret material")
	}
	resolved, err := providerValue.Resolve(context.Background(), key)
	if err != nil || !bytes.Equal(resolved.Value, plaintext) || resolved.Metadata != created {
		t.Fatalf("Resolve() = (%+v, %v)", resolved, err)
	}
	resolved.Clear()
	if err := providerValue.Delete(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "secrets", entries[0].Name())); !os.IsNotExist(err) {
		t.Fatalf("protected Secret file remains: %v", err)
	}
}

func TestOSSecretProviderRejectsTamperedCiphertext(t *testing.T) {
	dataDir := t.TempDir()
	metadata := &memorySecretMetadata{records: make(map[SecretKey]SecretMetadata)}
	providerValue, err := NewOSSecretProvider(dataDir, metadata, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	key := SecretKey{SystemID: domain.SystemID("aiws"), Name: "tamper-check"}
	plaintext := []byte("plaintext-must-not-appear-in-errors")
	if _, err := providerValue.Set(context.Background(), key, plaintext); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(dataDir, "secrets"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("read Secret directory = (%d, %v)", len(entries), err)
	}
	path := filepath.Join(dataDir, "secrets", entries[0].Name())
	protected, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	protected[len(protected)/2] ^= 0xff
	if err := os.WriteFile(path, protected, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = providerValue.Resolve(context.Background(), key)
	if err == nil || errors.Is(err, ErrSecretNotFound) || bytes.Contains([]byte(err.Error()), plaintext) {
		t.Fatalf("Resolve(tampered) error = %v", err)
	}
}
