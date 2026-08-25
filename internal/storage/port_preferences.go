package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"stackpilot/internal/domain"
	"stackpilot/internal/ports"
)

// LoadPreferences returns stable override and sticky inputs for planning.
func (repository *PortLeaseRepository) LoadPreferences(ctx context.Context, workspaceID domain.WorkspaceID) (ports.Preferences, error) {
	workspaceValues, err := loadPortValues(ctx, repository.database, `SELECT logical_name,port FROM workspace_port_overrides
        WHERE workspace_id = ? ORDER BY logical_name`, workspaceID.String())
	if err != nil {
		return ports.Preferences{}, fmt.Errorf("load workspace port overrides: %w", err)
	}
	stickyValues, err := loadPortValues(ctx, repository.database, `SELECT logical_name,port FROM sticky_port_history
        WHERE workspace_id = ? ORDER BY logical_name`, workspaceID.String())
	if err != nil {
		return ports.Preferences{}, fmt.Errorf("load sticky port history: %w", err)
	}
	return ports.Preferences{Workspace: workspaceValues, Sticky: stickyValues}, nil
}

// SetWorkspaceOverride stores one validated loopback TCP preference.
func (repository *PortLeaseRepository) SetWorkspaceOverride(ctx context.Context, workspaceID domain.WorkspaceID, logicalName string, port int, now time.Time) error {
	if _, err := domain.ParseWorkspaceID(workspaceID.String()); err != nil || !validLogicalPort(logicalName, port) {
		return fmt.Errorf("workspace port override is invalid")
	}
	_, err := repository.database.ExecContext(ctx, `INSERT INTO workspace_port_overrides
        (workspace_id,logical_name,protocol,host,port,updated_at) VALUES (?,?,'tcp','127.0.0.1',?,?)
        ON CONFLICT(workspace_id,logical_name,protocol) DO UPDATE SET port=excluded.port,host=excluded.host,updated_at=excluded.updated_at`,
		workspaceID.String(), logicalName, port, formatDatabaseTime(now))
	if err != nil {
		return fmt.Errorf("set workspace port override: %w", err)
	}
	return nil
}

// RecordSuccessfulPlan updates sticky history from every bound lease in a successful plan.
func (repository *PortLeaseRepository) RecordSuccessfulPlan(ctx context.Context, planID domain.PortPlanID, now time.Time) error {
	return executeImmediate(ctx, repository.database, func(connection *sql.Conn) error {
		result, err := connection.ExecContext(ctx, `INSERT INTO sticky_port_history
            (workspace_id,logical_name,protocol,host,port,manifest_digest,succeeded_at)
            SELECT workspace_id,logical_name,protocol,host,port,manifest_digest,? FROM port_leases
            WHERE plan_id = ? AND state = 'bound'
            ON CONFLICT(workspace_id,logical_name,protocol) DO UPDATE SET host=excluded.host,port=excluded.port,
            manifest_digest=excluded.manifest_digest,succeeded_at=excluded.succeeded_at`, formatDatabaseTime(now), planID.String())
		if err != nil {
			return fmt.Errorf("record successful port plan: %w", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("read sticky port update result: %w", err)
		}
		if affected == 0 {
			return fmt.Errorf("successful port plan has no bound leases")
		}
		return nil
	})
}

func loadPortValues(ctx context.Context, database *sql.DB, query string, workspaceID string) (map[string]int, error) {
	rows, err := database.QueryContext(ctx, query, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]int)
	for rows.Next() {
		var logicalName string
		var port int
		if err := rows.Scan(&logicalName, &port); err != nil {
			return nil, err
		}
		result[logicalName] = port
	}
	return result, rows.Err()
}

func validLogicalPort(logicalName string, port int) bool {
	_, err := domain.ParseServiceID(logicalName)
	return err == nil && port >= 1024 && port <= 65535
}
