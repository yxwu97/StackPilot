package security

import (
	"context"
	"errors"
	"testing"
	"time"

	"stackpilot/internal/domain"
)

func TestSecretProviderVersionsReconcilesAndDeletes(t *testing.T) {
	store := &memoryProtectedSecrets{records: make(map[SecretKey]protectedSecret)}
	metadata := &memorySecretMetadata{records: make(map[SecretKey]SecretMetadata)}
	fixed := time.Date(2026, 8, 18, 3, 10, 0, 0, time.UTC)
	providerValue, err := newSecretProvider(store, metadata, func() time.Time { return fixed })
	if err != nil {
		t.Fatal(err)
	}
	key := SecretKey{SystemID: domain.SystemID("aiws"), Name: "database-password"}
	first, err := providerValue.Set(context.Background(), key, []byte("first-value"))
	if err != nil || first.Version != 1 {
		t.Fatalf("Set(first) = (%+v, %v)", first, err)
	}
	resolved, err := providerValue.Resolve(context.Background(), key)
	if err != nil || string(resolved.Value) != "first-value" || resolved.Metadata != first {
		t.Fatalf("Resolve() = (%+v, %v)", resolved, err)
	}
	resolved.Clear()
	delete(metadata.records, key)
	got, found, err := providerValue.Metadata(context.Background(), key)
	if err != nil || !found || got != first || metadata.records[key] != first {
		t.Fatalf("Metadata() = (%+v, %t, %v)", got, found, err)
	}
	second, err := providerValue.Set(context.Background(), key, []byte("second-value"))
	if err != nil || second.Version != 2 {
		t.Fatalf("Set(second) = (%+v, %v)", second, err)
	}
	if err := providerValue.Delete(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	if _, found, err := providerValue.Metadata(context.Background(), key); err != nil || found {
		t.Fatalf("Metadata(after delete) = (found=%t, %v)", found, err)
	}
}

func TestSecretProviderRejectsInvalidInput(t *testing.T) {
	store := &memoryProtectedSecrets{records: make(map[SecretKey]protectedSecret)}
	metadata := &memorySecretMetadata{records: make(map[SecretKey]SecretMetadata)}
	providerValue, err := newSecretProvider(store, metadata, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	invalid := []SecretKey{
		{SystemID: domain.SystemID("AIWS"), Name: "key"},
		{SystemID: domain.SystemID("aiws"), Name: "../key"},
	}
	for _, key := range invalid {
		if _, err := providerValue.Set(context.Background(), key, []byte("never-persist")); !errors.Is(err, ErrSecretInvalid) {
			t.Fatalf("Set(%+v) error = %v", key, err)
		}
	}
	if _, err := providerValue.Set(context.Background(), SecretKey{SystemID: domain.SystemID("aiws"), Name: "key"}, nil); !errors.Is(err, ErrSecretInvalid) {
		t.Fatalf("Set(empty) error = %v", err)
	}
}

type memoryProtectedSecrets struct {
	records map[SecretKey]protectedSecret
}

func (store *memoryProtectedSecrets) Load(_ context.Context, key SecretKey) (protectedSecret, bool, error) {
	record, found := store.records[key]
	record.Value = append([]byte(nil), record.Value...)
	return record, found, nil
}

func (store *memoryProtectedSecrets) Save(_ context.Context, record protectedSecret) error {
	key := SecretKey{SystemID: domain.SystemID(record.SystemID), Name: record.Name}
	record.Value = append([]byte(nil), record.Value...)
	store.records[key] = record
	return nil
}

func (store *memoryProtectedSecrets) Delete(_ context.Context, key SecretKey) error {
	record := store.records[key]
	zeroBytes(record.Value)
	delete(store.records, key)
	return nil
}

type memorySecretMetadata struct {
	records map[SecretKey]SecretMetadata
}

func (store *memorySecretMetadata) GetSecretMetadata(_ context.Context, key SecretKey) (SecretMetadata, bool, error) {
	metadata, found := store.records[key]
	return metadata, found, nil
}

func (store *memorySecretMetadata) PutSecretMetadata(_ context.Context, metadata SecretMetadata) error {
	if current, found := store.records[metadata.Key]; found && current.Version > metadata.Version {
		return ErrSecretVersionConflict
	}
	store.records[metadata.Key] = metadata
	return nil
}

func (store *memorySecretMetadata) DeleteSecretMetadata(_ context.Context, key SecretKey) error {
	delete(store.records, key)
	return nil
}
