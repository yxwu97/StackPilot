//go:build windows

package security_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"stackpilot/internal/security"
	"stackpilot/internal/storage"
)

func TestRealDPAPIAndSQLiteTokenRotationRoundTrip(t *testing.T) {
	dataDir := t.TempDir()
	database, err := storage.Open(context.Background(), filepath.Join(dataDir, "stackpilot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	repository, err := storage.NewAuthTokenRepository(database)
	if err != nil {
		t.Fatal(err)
	}
	store, err := security.NewOSTokenStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := security.NewAuthManager(security.AuthConfig{
		Repository: repository, Store: store, ArgonMemory: 8 * 1024, ArgonTime: 1, ArgonThreads: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Initialize(context.Background()); err != nil {
		t.Fatalf("initialize real authentication = %v", err)
	}
	prior, found, err := store.Load()
	if err != nil || !found {
		t.Fatalf("load prior DPAPI token = (%t, %v)", found, err)
	}
	if _, err := manager.Rotate(context.Background()); err != nil {
		t.Fatalf("Rotate() error = %v", err)
	}
	current, found, err := store.Load()
	if err != nil || !found || string(current) == string(prior) {
		t.Fatalf("load rotated DPAPI token = (found=%t, changed=%t, error=%v)", found, string(current) != string(prior), err)
	}
	if err := manager.AuthenticateBearer(context.Background(), string(prior)); !errors.Is(err, security.ErrAuthenticationFailed) {
		t.Fatalf("prior token error = %v", err)
	}
	if err := manager.AuthenticateBearer(context.Background(), string(current)); err != nil {
		t.Fatalf("current token error = %v", err)
	}
	restarted, err := security.NewAuthManager(security.AuthConfig{
		Repository: repository, Store: store, ArgonMemory: 8 * 1024, ArgonTime: 1, ArgonThreads: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.Initialize(context.Background()); err != nil {
		t.Fatalf("restarted Initialize() error = %v", err)
	}
	zero(prior)
	zero(current)
}

func zero(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
