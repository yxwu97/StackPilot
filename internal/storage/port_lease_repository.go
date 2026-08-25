package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"modernc.org/sqlite"
	"stackpilot/internal/domain"
	"stackpilot/internal/ports"
)

// PortLeaseRepository persists whole-plan reservations and lease transitions.
type PortLeaseRepository struct {
	database *sql.DB
}

// NewPortLeaseRepository constructs a port lease repository over a migrated database.
func NewPortLeaseRepository(database *sql.DB) (*PortLeaseRepository, error) {
	if database == nil {
		return nil, fmt.Errorf("port lease repository database is required")
	}
	return &PortLeaseRepository{database: database}, nil
}

// Reserve expires stale reservations, exposes active leases, and inserts one complete plan atomically.
func (repository *PortLeaseRepository) Reserve(ctx context.Context, reservation ports.Reservation, selectLeases ports.SelectLeases) error {
	if selectLeases == nil || !reservation.Now.Equal(reservation.Now.UTC()) || !reservation.ExpiresAt.After(reservation.Now) {
		return ports.ErrInvalidInput
	}
	return executeImmediate(ctx, repository.database, func(connection *sql.Conn) error {
		if err := expireReservations(ctx, connection, reservation.Now); err != nil {
			return err
		}
		active, err := listActiveLeases(ctx, connection)
		if err != nil {
			return err
		}
		leases, err := selectLeases(active)
		if err != nil {
			return err
		}
		return insertReservedLeases(ctx, connection, reservation, leases)
	})
}

// MarkBound associates a reserved lease with the created runtime instance.
func (repository *PortLeaseRepository) MarkBound(ctx context.Context, id domain.PortLeaseID, instanceID domain.SystemInstanceID, now time.Time) error {
	result, err := repository.database.ExecContext(ctx, `UPDATE port_leases
        SET instance_id = ?, state = 'bound', updated_at = ? WHERE id = ? AND state = 'reserved'`,
		instanceID.String(), formatDatabaseTime(now), id.String())
	if err != nil {
		return fmt.Errorf("mark port lease bound: %w", err)
	}
	return requireLeaseTransition(result)
}

// Release moves a reserved or bound lease to released after OS ownership was checked.
func (repository *PortLeaseRepository) Release(ctx context.Context, id domain.PortLeaseID, now time.Time) error {
	result, err := repository.database.ExecContext(ctx, `UPDATE port_leases
        SET state = 'released', updated_at = ? WHERE id = ? AND state IN ('reserved', 'bound')`,
		formatDatabaseTime(now), id.String())
	if err != nil {
		return fmt.Errorf("release port lease: %w", err)
	}
	return requireLeaseTransition(result)
}

// ExpireReserved expires stale unbound reservations during startup and periodic reconciliation.
func (repository *PortLeaseRepository) ExpireReserved(ctx context.Context, now time.Time) (int64, error) {
	if now.IsZero() || now.Location() != time.UTC {
		return 0, ports.ErrInvalidInput
	}
	result, err := repository.database.ExecContext(ctx, `UPDATE port_leases SET state = 'expired', updated_at = ?
		WHERE state = 'reserved' AND expires_at <= ? AND NOT EXISTS (
			SELECT 1 FROM operations o WHERE o.id = port_leases.operation_id
			AND o.state IN ('queued','running','cancelling'))`, formatDatabaseTime(now), formatDatabaseTime(now))
	if err != nil {
		return 0, fmt.Errorf("expire stale port leases: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("inspect expired port leases: %w", err)
	}
	return count, nil
}

// ListPlan returns the stable persisted lease order for a plan.
func (repository *PortLeaseRepository) ListPlan(ctx context.Context, planID domain.PortPlanID) ([]ports.Lease, error) {
	rows, err := repository.database.QueryContext(ctx, portLeaseSelect+` WHERE plan_id = ? ORDER BY logical_name`, planID.String())
	if err != nil {
		return nil, fmt.Errorf("list port plan leases: %w", err)
	}
	defer rows.Close()
	return scanLeases(rows)
}

func expireReservations(ctx context.Context, connection *sql.Conn, now time.Time) error {
	_, err := connection.ExecContext(ctx, `UPDATE port_leases SET state = 'expired', updated_at = ?
		WHERE state = 'reserved' AND expires_at <= ? AND NOT EXISTS (
			SELECT 1 FROM operations o WHERE o.id = port_leases.operation_id
			AND o.state IN ('queued','running','cancelling'))`, formatDatabaseTime(now), formatDatabaseTime(now))
	if err != nil {
		return fmt.Errorf("expire stale port leases: %w", err)
	}
	return nil
}

func listActiveLeases(ctx context.Context, connection *sql.Conn) ([]ports.Lease, error) {
	rows, err := connection.QueryContext(ctx, portLeaseSelect+` WHERE state IN ('reserved', 'bound') ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("list active port leases: %w", err)
	}
	defer rows.Close()
	return scanLeases(rows)
}

func insertReservedLeases(ctx context.Context, connection *sql.Conn, reservation ports.Reservation, leases []ports.Lease) error {
	if len(leases) == 0 {
		return ports.ErrInvalidInput
	}
	for _, lease := range leases {
		if err := validateReservedLease(reservation, lease); err != nil {
			return err
		}
		_, err := connection.ExecContext(ctx, `INSERT INTO port_leases
            (id,plan_id,workspace_id,operation_id,manifest_digest,logical_name,protocol,host,port,state,expires_at,created_at,updated_at)
            VALUES (?,?,?,?,?,?,?,?,?,'reserved',?,?,?)`, lease.ID.String(), lease.PlanID.String(), lease.WorkspaceID.String(),
			lease.OperationID.String(), lease.ManifestDigest, lease.LogicalName, lease.Protocol, lease.Host, lease.Port,
			formatDatabaseTime(lease.ExpiresAt), formatDatabaseTime(lease.CreatedAt), formatDatabaseTime(lease.UpdatedAt))
		if err != nil {
			return classifyLeaseInsert(err)
		}
	}
	return nil
}

func validateReservedLease(reservation ports.Reservation, lease ports.Lease) error {
	if lease.PlanID != reservation.PlanID || lease.WorkspaceID != reservation.WorkspaceID || lease.OperationID != reservation.OperationID ||
		lease.ManifestDigest != reservation.ManifestDigest || lease.State != ports.LeaseReserved || lease.ExpiresAt != reservation.ExpiresAt ||
		lease.CreatedAt != reservation.Now || lease.UpdatedAt != reservation.Now || lease.InstanceID != nil {
		return ports.ErrInvalidInput
	}
	if _, err := domain.ParsePortLeaseID(lease.ID.String()); err != nil {
		return ports.ErrInvalidInput
	}
	return nil
}

func classifyLeaseInsert(err error) error {
	var sqliteError *sqlite.Error
	if errors.As(err, &sqliteError) && sqliteError.Code()&0xff == 19 {
		return fmt.Errorf("%w: %v", ports.ErrLeaseConflict, err)
	}
	return fmt.Errorf("insert port lease: %w", err)
}

func requireLeaseTransition(result sql.Result) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read port lease transition result: %w", err)
	}
	if affected != 1 {
		return ports.ErrLeaseState
	}
	return nil
}

const portLeaseSelect = `SELECT id, plan_id, workspace_id, instance_id, operation_id, manifest_digest,
    logical_name, protocol, host, port, state, expires_at, created_at, updated_at FROM port_leases`

func scanLeases(rows *sql.Rows) ([]ports.Lease, error) {
	result := make([]ports.Lease, 0)
	for rows.Next() {
		lease, err := scanLease(rows)
		if err != nil {
			return nil, fmt.Errorf("scan port lease: %w", err)
		}
		result = append(result, lease)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate port leases: %w", err)
	}
	return result, nil
}

func scanLease(scanner rowScanner) (ports.Lease, error) {
	var lease ports.Lease
	var id, planID, workspaceID, operationID, state, expiresAt, createdAt, updatedAt string
	var instanceID sql.NullString
	err := scanner.Scan(&id, &planID, &workspaceID, &instanceID, &operationID, &lease.ManifestDigest,
		&lease.LogicalName, &lease.Protocol, &lease.Host, &lease.Port, &state, &expiresAt, &createdAt, &updatedAt)
	if err != nil {
		return lease, err
	}
	lease.ID, lease.PlanID = domain.PortLeaseID(id), domain.PortPlanID(planID)
	lease.WorkspaceID, lease.OperationID, lease.State = domain.WorkspaceID(workspaceID), domain.OperationID(operationID), ports.LeaseState(state)
	if instanceID.Valid {
		value := domain.SystemInstanceID(instanceID.String)
		lease.InstanceID = &value
	}
	if lease.ExpiresAt, err = parseDatabaseTime(expiresAt); err != nil {
		return lease, err
	}
	if lease.CreatedAt, err = parseDatabaseTime(createdAt); err != nil {
		return lease, err
	}
	lease.UpdatedAt, err = parseDatabaseTime(updatedAt)
	return lease, err
}
