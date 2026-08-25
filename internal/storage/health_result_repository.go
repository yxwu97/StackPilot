package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"
	"unicode/utf8"

	"stackpilot/internal/domain"
	"stackpilot/internal/health"
)

const maximumHealthResults = 1000

// HealthRetentionPolicy bounds one compaction transaction.
type HealthRetentionPolicy struct {
	DetailWindow time.Duration
	RecentLimit  int
	BatchLimit   int
}

// DefaultHealthRetentionPolicy keeps one day of details and at least 1000 rows per instance.
var DefaultHealthRetentionPolicy = HealthRetentionPolicy{DetailWindow: 24 * time.Hour, RecentLimit: 1000, BatchLimit: 500}

// CompactDefault applies the Phase 2E bounded retention policy.
func (repository *HealthResultRepository) CompactDefault(ctx context.Context, now time.Time) (int64, error) {
	return repository.Compact(ctx, now, DefaultHealthRetentionPolicy)
}

// HealthResultRepository persists bounded readiness evidence in SQLite.
type HealthResultRepository struct {
	database *sql.DB
}

// NewHealthResultRepository constructs a repository over a migrated database.
func NewHealthResultRepository(database *sql.DB) (*HealthResultRepository, error) {
	if database == nil {
		return nil, fmt.Errorf("health result repository database is required")
	}
	return &HealthResultRepository{database: database}, nil
}

// Record persists one completed and already-redacted check result.
func (repository *HealthResultRepository) Record(ctx context.Context, id domain.ServiceInstanceID, result health.Result) error {
	_, err := repository.RecordWithID(ctx, id, result)
	return err
}

// RecordWithID persists one result and returns its monotonic durable cursor.
func (repository *HealthResultRepository) RecordWithID(ctx context.Context, id domain.ServiceInstanceID, result health.Result) (int64, error) {
	if err := validateHealthResult(id, result); err != nil {
		return 0, err
	}
	inserted, err := repository.database.ExecContext(ctx, `INSERT INTO health_results
        (service_instance_id, kind, success, duration_ms, error_code, summary, checked_at)
        VALUES (?, ?, ?, ?, ?, ?, ?)`, id.String(), string(result.Kind), boolInteger(result.Success),
		result.Duration.Milliseconds(), nullableText(string(result.ErrorCode)), result.Summary, formatDatabaseTime(result.CheckedAt))
	if err != nil {
		return 0, fmt.Errorf("insert health result: %w", err)
	}
	idValue, err := inserted.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("read health result ID: %w", err)
	}
	return idValue, nil
}

// ListRecent returns newest-first results with a hard query bound.
func (repository *HealthResultRepository) ListRecent(ctx context.Context, id domain.ServiceInstanceID, limit int) ([]health.Result, error) {
	if _, err := domain.ParseServiceInstanceID(id.String()); err != nil || limit < 1 || limit > maximumHealthResults {
		return nil, fmt.Errorf("invalid health result query")
	}
	rows, err := repository.database.QueryContext(ctx, `SELECT id, kind, success, duration_ms,
        error_code, summary, checked_at FROM health_results WHERE service_instance_id = ?
        ORDER BY checked_at DESC, id DESC LIMIT ?`, id.String(), limit)
	if err != nil {
		return nil, fmt.Errorf("list health results: %w", err)
	}
	defer rows.Close()
	return scanHealthResults(rows)
}

// Compact aggregates and removes one bounded batch outside the detail retention window.
func (repository *HealthResultRepository) Compact(ctx context.Context, now time.Time, policy HealthRetentionPolicy) (int64, error) {
	if now.IsZero() || now.Location() != time.UTC || policy.DetailWindow < time.Hour || policy.DetailWindow > 7*24*time.Hour ||
		policy.RecentLimit < 1 || policy.RecentLimit > 10000 || policy.BatchLimit < 1 || policy.BatchLimit > 5000 {
		return 0, fmt.Errorf("invalid health retention policy")
	}
	var removed int64
	err := executeImmediate(ctx, repository.database, func(connection *sql.Conn) error {
		cutoff := formatDatabaseTime(now.Add(-policy.DetailWindow))
		if err := aggregateHealthBatch(ctx, connection, cutoff, policy); err != nil {
			return err
		}
		result, err := deleteHealthBatch(ctx, connection, cutoff, policy)
		if err != nil {
			return err
		}
		removed, err = result.RowsAffected()
		return err
	})
	return removed, err
}

func aggregateHealthBatch(ctx context.Context, connection *sql.Conn, cutoff string, policy HealthRetentionPolicy) error {
	_, err := connection.ExecContext(ctx, `WITH ranked AS (
        SELECT id, service_instance_id, kind, success, duration_ms, checked_at,
            ROW_NUMBER() OVER (PARTITION BY service_instance_id ORDER BY checked_at DESC, id DESC) AS rank
        FROM health_results
    ), candidates AS (
        SELECT * FROM ranked WHERE checked_at < ? AND rank > ? ORDER BY checked_at, id LIMIT ?
    )
    INSERT INTO health_hourly_aggregates
        (service_instance_id,kind,bucket_start,check_count,success_count,duration_total_ms,duration_max_ms)
    SELECT service_instance_id,kind,substr(checked_at,1,13) || ':00:00Z',COUNT(*),SUM(success),SUM(duration_ms),MAX(duration_ms)
    FROM candidates WHERE 1 GROUP BY service_instance_id,kind,substr(checked_at,1,13)
    ON CONFLICT(service_instance_id,kind,bucket_start) DO UPDATE SET
        check_count=check_count+excluded.check_count,
        success_count=success_count+excluded.success_count,
        duration_total_ms=duration_total_ms+excluded.duration_total_ms,
        duration_max_ms=MAX(duration_max_ms,excluded.duration_max_ms)`, cutoff, policy.RecentLimit, policy.BatchLimit)
	if err != nil {
		return fmt.Errorf("aggregate health results: %w", err)
	}
	return nil
}

func deleteHealthBatch(ctx context.Context, connection *sql.Conn, cutoff string, policy HealthRetentionPolicy) (sql.Result, error) {
	result, err := connection.ExecContext(ctx, `WITH ranked AS (
        SELECT id, checked_at, ROW_NUMBER() OVER (PARTITION BY service_instance_id ORDER BY checked_at DESC, id DESC) AS rank
        FROM health_results
    ), candidates AS (
        SELECT id FROM ranked WHERE checked_at < ? AND rank > ? ORDER BY checked_at, id LIMIT ?
    ) DELETE FROM health_results WHERE id IN (SELECT id FROM candidates)`, cutoff, policy.RecentLimit, policy.BatchLimit)
	if err != nil {
		return nil, fmt.Errorf("delete compacted health results: %w", err)
	}
	return result, nil
}

func scanHealthResults(rows *sql.Rows) ([]health.Result, error) {
	results := make([]health.Result, 0)
	for rows.Next() {
		var result health.Result
		var kind, checkedAt string
		var code sql.NullString
		var success int
		var durationMillis int64
		if err := rows.Scan(&result.ID, &kind, &success, &durationMillis, &code, &result.Summary, &checkedAt); err != nil {
			return nil, fmt.Errorf("scan health result: %w", err)
		}
		result.Kind, result.Success, result.ErrorCode = health.Kind(kind), success == 1, health.ErrorCode(code.String)
		result.Duration = durationFromMilliseconds(durationMillis)
		var err error
		if result.CheckedAt, err = parseDatabaseTime(checkedAt); err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate health results: %w", err)
	}
	return results, nil
}

func validateHealthResult(id domain.ServiceInstanceID, result health.Result) error {
	if _, err := domain.ParseServiceInstanceID(id.String()); err != nil {
		return err
	}
	if result.Kind != health.KindProcess && result.Kind != health.KindTCP && result.Kind != health.KindHTTP && result.Kind != health.KindCompose {
		return fmt.Errorf("invalid health result kind")
	}
	if result.Duration < 0 || result.CheckedAt.IsZero() || len(result.Summary) > 2048 || !utf8.ValidString(result.Summary) {
		return fmt.Errorf("invalid health result metadata")
	}
	if result.Success == (result.ErrorCode != "") || !validHealthErrorCode(result.ErrorCode, result.Success) {
		return fmt.Errorf("invalid health result outcome")
	}
	return nil
}

func validHealthErrorCode(code health.ErrorCode, success bool) bool {
	if success {
		return code == ""
	}
	switch code {
	case health.CodeProcessExited, health.CodeProcessIdentityMismatch, health.CodeTCPRefused,
		health.CodeTCPTimeout, health.CodeHTTPStatusMismatch, health.CodeHTTPBodyMismatch, health.CodeHTTPTimeout,
		health.CodeContainerUnhealthy:
		return true
	default:
		return false
	}
}

func durationFromMilliseconds(value int64) time.Duration {
	return time.Duration(value) * time.Millisecond
}

var _ health.Recorder = (*HealthResultRepository)(nil)
var _ health.IDRecorder = (*HealthResultRepository)(nil)
