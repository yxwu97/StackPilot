//go:build windows

package security

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestOSTokenStoreEncryptsAndRoundTripsWithDPAPI(t *testing.T) {
	dataDir := t.TempDir()
	storeValue, err := NewOSTokenStore(dataDir)
	if err != nil {
		t.Fatalf("NewOSTokenStore() error = %v", err)
	}
	token := []byte("local-token-plaintext-value-1234567890")
	if err := storeValue.Save(token); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	path := filepath.Join(dataDir, "auth", "token.dpapi")
	protected, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read DPAPI file: %v", err)
	}
	if bytes.Contains(protected, token) {
		t.Fatal("DPAPI file contains plaintext token")
	}
	loaded, found, err := storeValue.Load()
	if err != nil || !found || !bytes.Equal(loaded, token) {
		t.Fatalf("Load() = (%q, %t, %v)", loaded, found, err)
	}
	zeroBytes(loaded)
}
