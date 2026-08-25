package storage

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"

	"stackpilot/internal/domain"
	"stackpilot/internal/security"
)

var secretEnvironmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ServiceSecretVersionRepository records the Secret metadata used by process launches.
type ServiceSecretVersionRepository struct {
	database *sql.DB
}

// NewServiceSecretVersionRepository constructs a repository over the migrated database.
func NewServiceSecretVersionRepository(database *sql.DB) (*ServiceSecretVersionRepository, error) {
	if database == nil {
		return nil, fmt.Errorf("service Secret version repository database is required")
	}
	return &ServiceSecretVersionRepository{database: database}, nil
}

// RecordServiceSecretVersions atomically inserts or advances the versions used by one service instance.
func (repository *ServiceSecretVersionRepository) RecordServiceSecretVersions(ctx context.Context, serviceID domain.ServiceInstanceID, values []security.ServiceSecretVersion) error {
	if _, err := domain.ParseServiceInstanceID(serviceID.String()); err != nil {
		return security.ErrSecretInvalid
	}
	transaction, err := repository.database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin service Secret version transaction: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	for _, value := range values {
		if err := recordServiceSecretVersion(ctx, transaction, serviceID, value); err != nil {
			return err
		}
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit service Secret versions: %w", err)
	}
	return nil
}

func recordServiceSecretVersion(ctx context.Context, transaction *sql.Tx, serviceID domain.ServiceInstanceID, value security.ServiceSecretVersion) error {
	if value.ServiceInstanceID != serviceID || !secretEnvironmentName.MatchString(value.EnvironmentName) || len(value.EnvironmentName) > 32767 {
		return security.ErrSecretInvalid
	}
	if err := security.ValidateSecretKey(value.Key); err != nil || value.Provider != security.SecretProviderDPAPIFile || value.Version < 1 || value.ResolvedAt.IsZero() {
		return security.ErrSecretInvalid
	}
	result, err := transaction.ExecContext(ctx, `INSERT INTO service_instance_secret_versions
        (service_instance_id, environment_name, system_id, name, provider, version, resolved_at)
        VALUES (?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(service_instance_id, environment_name) DO UPDATE SET
            system_id = excluded.system_id, name = excluded.name, provider = excluded.provider,
            version = excluded.version, resolved_at = excluded.resolved_at
        WHERE excluded.version >= service_instance_secret_versions.version`,
		serviceID.String(), value.EnvironmentName, value.Key.SystemID.String(), value.Key.Name,
		value.Provider, value.Version, formatDatabaseTime(value.ResolvedAt.UTC()))
	if err != nil {
		return fmt.Errorf("record service Secret version: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read service Secret version result: %w", err)
	}
	if affected != 1 {
		return security.ErrSecretVersionConflict
	}
	return nil
}

// ListServiceSecretVersions returns the launch metadata ordered by environment name.
func (repository *ServiceSecretVersionRepository) ListServiceSecretVersions(ctx context.Context, serviceID domain.ServiceInstanceID) ([]security.ServiceSecretVersion, error) {
	if _, err := domain.ParseServiceInstanceID(serviceID.String()); err != nil {
		return nil, security.ErrSecretInvalid
	}
	rows, err := repository.database.QueryContext(ctx, `SELECT environment_name, system_id, name, provider, version, resolved_at
        FROM service_instance_secret_versions WHERE service_instance_id = ? ORDER BY environment_name`, serviceID.String())
	if err != nil {
		return nil, fmt.Errorf("list service Secret versions: %w", err)
	}
	defer rows.Close()
	return scanServiceSecretVersions(rows, serviceID)
}

func scanServiceSecretVersions(rows *sql.Rows, serviceID domain.ServiceInstanceID) ([]security.ServiceSecretVersion, error) {
	result := make([]security.ServiceSecretVersion, 0)
	for rows.Next() {
		var environment, systemID, name, provider, resolvedAt string
		var version int64
		if err := rows.Scan(&environment, &systemID, &name, &provider, &version, &resolvedAt); err != nil {
			return nil, fmt.Errorf("scan service Secret version: %w", err)
		}
		timestamp, err := parseDatabaseTime(resolvedAt)
		if err != nil {
			return nil, fmt.Errorf("parse service Secret resolution time: %w", err)
		}
		result = append(result, security.ServiceSecretVersion{
			ServiceInstanceID: serviceID, EnvironmentName: environment, ResolvedAt: timestamp,
			Key: security.SecretKey{SystemID: domain.SystemID(systemID), Name: name}, Provider: provider, Version: version,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate service Secret versions: %w", err)
	}
	return result, nil
}
