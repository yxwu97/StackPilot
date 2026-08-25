package storage

import (
	"context"
	"errors"
	"testing"

	"stackpilot/internal/domain"
	"stackpilot/internal/logs"
)

func TestRuntimeLogScopeRepositoryResolvesPersistedServiceInstance(t *testing.T) {
	database := openTestDatabase(t)
	serviceInstanceID := seedRuntimeInstance(t, database)
	repository, err := NewRuntimeLogScopeRepository(database)
	if err != nil {
		t.Fatalf("NewRuntimeLogScopeRepository() error = %v", err)
	}
	resolved, err := repository.Resolve(context.Background(),
		domain.SystemInstanceID("si_01ARZ3NDEKTSV4RRFFQ69G5FAV"), domain.ServiceID("backend"))
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolved.Scope.ServiceInstanceID != serviceInstanceID || resolved.Scope.SystemID != "btc" || resolved.WorkspaceID != "ws_01ARZ3NDEKTSV4RRFFQ69G5FAV" {
		t.Fatalf("Resolve() = %#v", resolved)
	}
	if _, err := repository.Resolve(context.Background(), resolved.Scope.InstanceID, domain.ServiceID("missing")); !errors.Is(err, logs.ErrScopeNotFound) {
		t.Fatalf("Resolve(missing) error = %v", err)
	}
}
