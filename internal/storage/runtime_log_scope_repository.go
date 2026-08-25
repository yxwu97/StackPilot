package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"stackpilot/internal/domain"
	"stackpilot/internal/logs"
)

// RuntimeLogScopeRepository resolves API log scopes from persisted runtime identity.
type RuntimeLogScopeRepository struct {
	database *sql.DB
}

// NewRuntimeLogScopeRepository constructs a resolver over a migrated database.
func NewRuntimeLogScopeRepository(database *sql.DB) (*RuntimeLogScopeRepository, error) {
	if database == nil {
		return nil, fmt.Errorf("runtime log scope repository database is required")
	}
	return &RuntimeLogScopeRepository{database: database}, nil
}

// Resolve verifies a system instance/service pair and returns its internal log scope.
func (repository *RuntimeLogScopeRepository) Resolve(ctx context.Context, instanceID domain.SystemInstanceID, serviceID domain.ServiceID) (logs.ResolvedScope, error) {
	if _, err := domain.ParseSystemInstanceID(instanceID.String()); err != nil {
		return logs.ResolvedScope{}, logs.ErrScopeNotFound
	}
	if _, err := domain.ParseServiceID(serviceID.String()); err != nil {
		return logs.ResolvedScope{}, logs.ErrScopeNotFound
	}
	var workspaceID, systemID, serviceInstanceID string
	err := repository.database.QueryRowContext(ctx, `SELECT w.id, w.system_id, svi.id
        FROM service_instances svi
        JOIN system_instances si ON si.id = svi.system_instance_id
        JOIN workspaces w ON w.id = si.workspace_id
        WHERE si.id = ? AND svi.service_id = ?`, instanceID.String(), serviceID.String()).Scan(
		&workspaceID, &systemID, &serviceInstanceID)
	if errors.Is(err, sql.ErrNoRows) {
		return logs.ResolvedScope{}, logs.ErrScopeNotFound
	}
	if err != nil {
		return logs.ResolvedScope{}, fmt.Errorf("resolve runtime log scope: %w", err)
	}
	return logs.ResolvedScope{
		WorkspaceID: domain.WorkspaceID(workspaceID),
		Scope: logs.Scope{
			SystemID: domain.SystemID(systemID), InstanceID: instanceID, ServiceID: serviceID,
			ServiceInstanceID: domain.ServiceInstanceID(serviceInstanceID),
		},
	}, nil
}

var _ logs.ScopeResolver = (*RuntimeLogScopeRepository)(nil)
