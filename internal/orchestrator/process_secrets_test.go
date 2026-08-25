package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"stackpilot/internal/domain"
	"stackpilot/internal/driver"
	"stackpilot/internal/security"
)

func TestPrepareProcessLaunchInjectsAndRecordsWithoutMutatingResolvedSpec(t *testing.T) {
	provider := &launchSecretProvider{value: []byte("launch-only-value"), version: 3}
	versions := &launchSecretVersions{}
	service := &SingleService{config: SingleServiceConfig{Secrets: provider, SecretVersions: versions}}
	spec := driver.ResolvedServiceSpec{
		Environment:      map[string]string{"DATABASE_PASSWORD": "${secret.database-password}", "STATIC": "safe"},
		SecretReferences: map[string]string{"DATABASE_PASSWORD": "database-password"},
	}
	serviceID := domain.ServiceInstanceID("svi_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	launch, err := service.prepareProcessLaunch(context.Background(), "btc", serviceID, spec)
	if err != nil {
		t.Fatal(err)
	}
	if launch.spec.Environment["DATABASE_PASSWORD"] != "launch-only-value" || spec.Environment["DATABASE_PASSWORD"] != "${secret.database-password}" {
		t.Fatalf("launch/resolved environments = %#v / %#v", launch.spec.Environment, spec.Environment)
	}
	if len(versions.recorded) != 1 || versions.recorded[0].Version != 3 || versions.recorded[0].EnvironmentName != "DATABASE_PASSWORD" {
		t.Fatalf("recorded versions = %#v", versions.recorded)
	}
	owned := launch.redactionValues[0]
	launch.clear()
	if !bytes.Equal(owned, make([]byte, len(owned))) || launch.spec.Environment["DATABASE_PASSWORD"] != "" {
		t.Fatalf("launch plaintext buffers were not cleared")
	}
}

func TestPrepareProcessLaunchRejectsUnsafeSecretValuesAndMapsErrors(t *testing.T) {
	serviceID := domain.ServiceInstanceID("svi_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	spec := driver.ResolvedServiceSpec{
		Environment:      map[string]string{"TOKEN": "${secret.api-key}"},
		SecretReferences: map[string]string{"TOKEN": "api-key"},
	}
	for _, value := range [][]byte{{0xff}, {'a', 0, 'b'}, []byte("line-one\nline-two"), bytes.Repeat([]byte{'x'}, maximumProcessSecretSize+1)} {
		service := &SingleService{config: SingleServiceConfig{
			Secrets: &launchSecretProvider{value: value, version: 1}, SecretVersions: &launchSecretVersions{},
		}}
		if _, err := service.prepareProcessLaunch(context.Background(), "btc", serviceID, spec); !errors.Is(err, security.ErrSecretInvalid) {
			t.Fatalf("unsafe value error = %v", err)
		}
	}
	if secretErrorCode(security.ErrSecretNotFound) != "SECRET_NOT_FOUND" || secretErrorCode(security.ErrSecretVersionConflict) != "SECRET_VERSION_CONFLICT" {
		t.Fatal("Secret errors were not mapped to stable codes")
	}
}

type launchSecretProvider struct {
	value   []byte
	version int64
}

func (provider *launchSecretProvider) Resolve(_ context.Context, key security.SecretKey) (security.ResolvedSecret, error) {
	if provider.value == nil {
		return security.ResolvedSecret{}, security.ErrSecretNotFound
	}
	return security.ResolvedSecret{
		Metadata: security.SecretMetadata{Key: key, Provider: security.SecretProviderDPAPIFile, Version: provider.version, UpdatedAt: time.Now().UTC()},
		Value:    append([]byte(nil), provider.value...),
	}, nil
}

func (*launchSecretProvider) Set(context.Context, security.SecretKey, []byte) (security.SecretMetadata, error) {
	return security.SecretMetadata{}, errors.New("not implemented")
}

func (*launchSecretProvider) Metadata(context.Context, security.SecretKey) (security.SecretMetadata, bool, error) {
	return security.SecretMetadata{}, false, errors.New("not implemented")
}

func (*launchSecretProvider) Delete(context.Context, security.SecretKey) error {
	return errors.New("not implemented")
}

type launchSecretVersions struct {
	recorded []security.ServiceSecretVersion
}

func (versions *launchSecretVersions) RecordServiceSecretVersions(_ context.Context, _ domain.ServiceInstanceID, values []security.ServiceSecretVersion) error {
	versions.recorded = append([]security.ServiceSecretVersion(nil), values...)
	return nil
}

func (versions *launchSecretVersions) ListServiceSecretVersions(context.Context, domain.ServiceInstanceID) ([]security.ServiceSecretVersion, error) {
	return append([]security.ServiceSecretVersion(nil), versions.recorded...), nil
}
