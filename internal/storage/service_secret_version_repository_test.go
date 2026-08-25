package storage

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"stackpilot/internal/domain"
	"stackpilot/internal/security"
)

func TestServiceSecretVersionRepositoryRecordsMonotonicLaunchProjection(t *testing.T) {
	database := openTestDatabase(t)
	serviceID := seedRuntimeInstance(t, database)
	repository, err := NewServiceSecretVersionRepository(database)
	if err != nil {
		t.Fatal(err)
	}
	first := serviceSecretVersion(serviceID, "DATABASE_PASSWORD", 1, time.Date(2026, 8, 18, 5, 0, 0, 0, time.UTC))
	if err := repository.RecordServiceSecretVersions(context.Background(), serviceID, []security.ServiceSecretVersion{first}); err != nil {
		t.Fatal(err)
	}
	second := first
	second.Version, second.ResolvedAt = 2, first.ResolvedAt.Add(time.Second)
	if err := repository.RecordServiceSecretVersions(context.Background(), serviceID, []security.ServiceSecretVersion{second}); err != nil {
		t.Fatal(err)
	}
	if err := repository.RecordServiceSecretVersions(context.Background(), serviceID, []security.ServiceSecretVersion{first}); !errors.Is(err, security.ErrSecretVersionConflict) {
		t.Fatalf("version rollback error = %v", err)
	}
	got, err := repository.ListServiceSecretVersions(context.Background(), serviceID)
	if err != nil || !reflect.DeepEqual(got, []security.ServiceSecretVersion{second}) {
		t.Fatalf("ListServiceSecretVersions() = (%#v, %v)", got, err)
	}
}

func TestServiceSecretVersionSchemaContainsNoSensitiveColumns(t *testing.T) {
	database := openTestDatabase(t)
	rows, err := database.Query(`PRAGMA table_info(service_instance_secret_versions)`)
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
		for _, forbidden := range []string{"value", "secret", "plaintext", "ciphertext", "digest"} {
			if name == forbidden {
				t.Fatalf("service Secret version table contains forbidden column %q", name)
			}
		}
	}
}

func serviceSecretVersion(serviceID domain.ServiceInstanceID, environment string, version int64, resolvedAt time.Time) security.ServiceSecretVersion {
	return security.ServiceSecretVersion{
		ServiceInstanceID: serviceID, EnvironmentName: environment,
		Key:      security.SecretKey{SystemID: "btc", Name: "database-password"},
		Provider: security.SecretProviderDPAPIFile, Version: version, ResolvedAt: resolvedAt,
	}
}
