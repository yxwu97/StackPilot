package security

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"time"

	"stackpilot/internal/domain"
)

const (
	// SecretProviderDPAPIFile identifies the Phase 2A Windows provider.
	SecretProviderDPAPIFile = "dpapi-file"
	// MaximumSecretValueSize bounds plaintext accepted by every provider boundary.
	MaximumSecretValueSize = 64 * 1024
)

var (
	// ErrSecretNotFound indicates that no protected value exists for a key.
	ErrSecretNotFound = errors.New("secret not found")
	// ErrSecretInvalid indicates malformed secret input or protected content.
	ErrSecretInvalid = errors.New("secret is invalid")
	// ErrSecretVersionConflict indicates metadata newer than protected storage.
	ErrSecretVersionConflict = errors.New("secret version conflict")
	secretNamePattern        = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
)

// SecretKey identifies one system-scoped secret without containing its value.
type SecretKey struct {
	SystemID domain.SystemID
	Name     string
}

// SecretMetadata is the only Secret representation permitted in SQLite and DTOs.
type SecretMetadata struct {
	Key       SecretKey
	Provider  string
	Version   int64
	UpdatedAt time.Time
}

// ResolvedSecret owns a temporary plaintext buffer used immediately before launch.
type ResolvedSecret struct {
	Metadata SecretMetadata
	Value    []byte
}

// ServiceSecretVersion is the non-sensitive projection of one launch-time resolution.
type ServiceSecretVersion struct {
	ServiceInstanceID domain.ServiceInstanceID
	EnvironmentName   string
	Key               SecretKey
	Provider          string
	Version           int64
	ResolvedAt        time.Time
}

// Clear overwrites the resolved plaintext buffer.
func (secret *ResolvedSecret) Clear() {
	zeroBytes(secret.Value)
	secret.Value = nil
}

// SecretMetadataStore persists only non-sensitive metadata.
type SecretMetadataStore interface {
	GetSecretMetadata(context.Context, SecretKey) (SecretMetadata, bool, error)
	PutSecretMetadata(context.Context, SecretMetadata) error
	DeleteSecretMetadata(context.Context, SecretKey) error
}

// SecretProvider exposes the complete Phase 2 Secret value boundary.
type SecretProvider interface {
	Set(context.Context, SecretKey, []byte) (SecretMetadata, error)
	Resolve(context.Context, SecretKey) (ResolvedSecret, error)
	Metadata(context.Context, SecretKey) (SecretMetadata, bool, error)
	Delete(context.Context, SecretKey) error
}

type protectedSecretStore interface {
	Load(context.Context, SecretKey) (protectedSecret, bool, error)
	Save(context.Context, protectedSecret) error
	Delete(context.Context, SecretKey) error
}

type protectedSecret struct {
	SchemaVersion int       `json:"schemaVersion"`
	SystemID      string    `json:"systemId"`
	Name          string    `json:"name"`
	Version       int64     `json:"version"`
	UpdatedAt     time.Time `json:"updatedAt"`
	Value         []byte    `json:"value"`
}

type secretProvider struct {
	store      protectedSecretStore
	metadata   SecretMetadataStore
	clock      func() time.Time
	operations sync.Mutex
}

func newSecretProvider(store protectedSecretStore, metadata SecretMetadataStore, clock func() time.Time) (SecretProvider, error) {
	if store == nil || metadata == nil || clock == nil {
		return nil, fmt.Errorf("secret provider dependencies are required")
	}
	return &secretProvider{store: store, metadata: metadata, clock: clock}, nil
}

func (provider *secretProvider) Set(ctx context.Context, key SecretKey, value []byte) (SecretMetadata, error) {
	if err := validateSecretKey(key); err != nil {
		return SecretMetadata{}, err
	}
	if len(value) == 0 || len(value) > MaximumSecretValueSize {
		return SecretMetadata{}, fmt.Errorf("%w: value size", ErrSecretInvalid)
	}
	provider.operations.Lock()
	defer provider.operations.Unlock()
	version, err := provider.nextVersion(ctx, key)
	if err != nil {
		return SecretMetadata{}, err
	}
	record := protectedSecret{
		SchemaVersion: 1, SystemID: key.SystemID.String(), Name: key.Name,
		Version: version, UpdatedAt: provider.clock().UTC(), Value: append([]byte(nil), value...),
	}
	defer zeroBytes(record.Value)
	if err := provider.store.Save(ctx, record); err != nil {
		return SecretMetadata{}, err
	}
	metadata := record.metadata()
	if err := provider.metadata.PutSecretMetadata(ctx, metadata); err != nil {
		return SecretMetadata{}, fmt.Errorf("project secret metadata: %w", err)
	}
	return metadata, nil
}

func (provider *secretProvider) Resolve(ctx context.Context, key SecretKey) (ResolvedSecret, error) {
	provider.operations.Lock()
	defer provider.operations.Unlock()
	record, found, err := provider.load(ctx, key)
	if err != nil {
		return ResolvedSecret{}, err
	}
	if !found {
		return ResolvedSecret{}, ErrSecretNotFound
	}
	metadata := record.metadata()
	if err := provider.metadata.PutSecretMetadata(ctx, metadata); err != nil {
		zeroBytes(record.Value)
		return ResolvedSecret{}, fmt.Errorf("reconcile secret metadata: %w", err)
	}
	return ResolvedSecret{Metadata: metadata, Value: record.Value}, nil
}

func (provider *secretProvider) Metadata(ctx context.Context, key SecretKey) (SecretMetadata, bool, error) {
	provider.operations.Lock()
	defer provider.operations.Unlock()
	record, found, err := provider.load(ctx, key)
	if err != nil {
		return SecretMetadata{}, false, err
	}
	if !found {
		return SecretMetadata{}, false, provider.metadata.DeleteSecretMetadata(ctx, key)
	}
	defer zeroBytes(record.Value)
	metadata := record.metadata()
	if err := provider.metadata.PutSecretMetadata(ctx, metadata); err != nil {
		return SecretMetadata{}, false, fmt.Errorf("reconcile secret metadata: %w", err)
	}
	return metadata, true, nil
}

func (provider *secretProvider) Delete(ctx context.Context, key SecretKey) error {
	if err := validateSecretKey(key); err != nil {
		return err
	}
	provider.operations.Lock()
	defer provider.operations.Unlock()
	if err := provider.store.Delete(ctx, key); err != nil {
		return err
	}
	return provider.metadata.DeleteSecretMetadata(ctx, key)
}

func (provider *secretProvider) nextVersion(ctx context.Context, key SecretKey) (int64, error) {
	var current int64
	record, found, err := provider.load(ctx, key)
	if err != nil {
		return 0, err
	}
	if found {
		current = record.Version
		zeroBytes(record.Value)
	}
	metadata, found, err := provider.metadata.GetSecretMetadata(ctx, key)
	if err != nil {
		return 0, err
	}
	if found && metadata.Version > current {
		current = metadata.Version
	}
	if current == int64(^uint64(0)>>1) {
		return 0, fmt.Errorf("%w: version exhausted", ErrSecretVersionConflict)
	}
	return current + 1, nil
}

func (provider *secretProvider) load(ctx context.Context, key SecretKey) (protectedSecret, bool, error) {
	if err := validateSecretKey(key); err != nil {
		return protectedSecret{}, false, err
	}
	if err := ctx.Err(); err != nil {
		return protectedSecret{}, false, err
	}
	return provider.store.Load(ctx, key)
}

func (record protectedSecret) metadata() SecretMetadata {
	return SecretMetadata{
		Key:      SecretKey{SystemID: domain.SystemID(record.SystemID), Name: record.Name},
		Provider: SecretProviderDPAPIFile, Version: record.Version, UpdatedAt: record.UpdatedAt,
	}
}

func validateSecretKey(key SecretKey) error {
	if _, err := domain.ParseSystemID(key.SystemID.String()); err != nil {
		return fmt.Errorf("%w: system ID", ErrSecretInvalid)
	}
	if !secretNamePattern.MatchString(key.Name) {
		return fmt.Errorf("%w: name", ErrSecretInvalid)
	}
	return nil
}

// ValidateSecretKey enforces the system/name identifier grammar.
func ValidateSecretKey(key SecretKey) error {
	return validateSecretKey(key)
}

// ValidateSecretMetadata enforces the non-sensitive persistent contract.
func ValidateSecretMetadata(metadata SecretMetadata) error {
	if err := validateSecretKey(metadata.Key); err != nil {
		return err
	}
	_, offset := metadata.UpdatedAt.Zone()
	if metadata.Provider != SecretProviderDPAPIFile || metadata.Version < 1 || metadata.UpdatedAt.IsZero() || offset != 0 {
		return fmt.Errorf("%w: metadata", ErrSecretInvalid)
	}
	return nil
}

func validateProtectedSecret(record protectedSecret, key SecretKey) error {
	if record.SchemaVersion != 1 || record.SystemID != key.SystemID.String() || record.Name != key.Name {
		return fmt.Errorf("%w: protected identity", ErrSecretInvalid)
	}
	if len(record.Value) == 0 || len(record.Value) > MaximumSecretValueSize {
		return fmt.Errorf("%w: protected value size", ErrSecretInvalid)
	}
	return ValidateSecretMetadata(record.metadata())
}
