package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"stackpilot/internal/domain"
)

// RestartAttemptRepository owns persisted automatic-restart sequence counters.
type RestartAttemptRepository struct{ database *sql.DB }

// NewRestartAttemptRepository constructs a restart-attempt repository.
func NewRestartAttemptRepository(database *sql.DB) (*RestartAttemptRepository, error) {
	if database == nil {
		return nil, fmt.Errorf("restart attempt repository database is required")
	}
	return &RestartAttemptRepository{database: database}, nil
}

// Claim increments a failure sequence if the finite policy still permits another attempt.
func (repository *RestartAttemptRepository) Claim(ctx context.Context, id domain.ServiceInstanceID, now time.Time, stableWindow time.Duration, maximum int) (int, bool, error) {
	if _, err := domain.ParseServiceInstanceID(id.String()); err != nil || now.IsZero() || now.Location() != time.UTC || stableWindow <= 0 || maximum < 1 || maximum > 100 {
		return 0, false, fmt.Errorf("invalid restart attempt claim")
	}
	attempt, allowed := 0, false
	err := executeImmediate(ctx, repository.database, func(connection *sql.Conn) error {
		current, started, ready, found, err := readRestartAttempt(ctx, connection, id)
		if err != nil {
			return err
		}
		if !found || (ready != nil && !now.Before(ready.Add(stableWindow))) {
			attempt, allowed = 1, true
			_, err = connection.ExecContext(ctx, `INSERT INTO service_restart_attempts(service_instance_id,attempt_count,sequence_started_at,last_attempt_at,ready_since)
                VALUES (?,?,?, ?,NULL) ON CONFLICT(service_instance_id) DO UPDATE SET attempt_count=1,sequence_started_at=excluded.sequence_started_at,last_attempt_at=excluded.last_attempt_at,ready_since=NULL`,
				id.String(), attempt, formatDatabaseTime(now), formatDatabaseTime(now))
			return err
		}
		_ = started
		attempt = current + 1
		if attempt > maximum {
			return nil
		}
		allowed = true
		_, err = connection.ExecContext(ctx, `UPDATE service_restart_attempts SET attempt_count=?,last_attempt_at=?,ready_since=NULL WHERE service_instance_id=?`, attempt, formatDatabaseTime(now), id.String())
		return err
	})
	return attempt, allowed, err
}

// MarkReady starts the stable-window clock without resetting attempts early.
func (repository *RestartAttemptRepository) MarkReady(ctx context.Context, id domain.ServiceInstanceID, now time.Time) error {
	if _, err := domain.ParseServiceInstanceID(id.String()); err != nil || now.IsZero() || now.Location() != time.UTC {
		return fmt.Errorf("invalid restart ready marker")
	}
	_, err := repository.database.ExecContext(ctx, `UPDATE service_restart_attempts SET ready_since=COALESCE(ready_since,?) WHERE service_instance_id=?`, formatDatabaseTime(now), id.String())
	return err
}

// ReleaseClaim conditionally returns an attempt that did not create an Operation.
func (repository *RestartAttemptRepository) ReleaseClaim(ctx context.Context, id domain.ServiceInstanceID, attempt int) error {
	if _, err := domain.ParseServiceInstanceID(id.String()); err != nil || attempt < 1 {
		return fmt.Errorf("invalid restart attempt release")
	}
	return executeImmediate(ctx, repository.database, func(connection *sql.Conn) error {
		if attempt == 1 {
			_, err := connection.ExecContext(ctx, `DELETE FROM service_restart_attempts WHERE service_instance_id=? AND attempt_count=1 AND ready_since IS NULL`, id.String())
			return err
		}
		_, err := connection.ExecContext(ctx, `UPDATE service_restart_attempts SET attempt_count=attempt_count-1
            WHERE service_instance_id=? AND attempt_count=? AND ready_since IS NULL`, id.String(), attempt)
		return err
	})
}

func readRestartAttempt(ctx context.Context, connection *sql.Conn, id domain.ServiceInstanceID) (int, time.Time, *time.Time, bool, error) {
	var attempts int
	var started string
	var ready sql.NullString
	err := connection.QueryRowContext(ctx, `SELECT attempt_count,sequence_started_at,ready_since FROM service_restart_attempts WHERE service_instance_id=?`, id.String()).Scan(&attempts, &started, &ready)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, time.Time{}, nil, false, nil
	}
	if err != nil {
		return 0, time.Time{}, nil, false, err
	}
	startedAt, err := parseDatabaseTime(started)
	if err != nil {
		return 0, time.Time{}, nil, false, err
	}
	readyAt, err := parseNullableDatabaseTime(ready)
	return attempts, startedAt, readyAt, true, err
}
