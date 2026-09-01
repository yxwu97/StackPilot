package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"stackpilot/internal/domain"
	"stackpilot/internal/metrics"
)

const maximumMetricBatch = 256

// MetricRetentionPolicy bounds detail aggregation and aggregate deletion.
type MetricRetentionPolicy struct {
	DetailWindow    time.Duration
	AggregateWindow time.Duration
	BatchLimit      int
}

// DefaultMetricRetentionPolicy keeps 24-hour detail and 30-day hourly history.
var DefaultMetricRetentionPolicy = MetricRetentionPolicy{
	DetailWindow: 24 * time.Hour, AggregateWindow: 30 * 24 * time.Hour, BatchLimit: 1000,
}

// MetricRepository persists bounded resource samples and hourly aggregates.
type MetricRepository struct {
	database *sql.DB
}

// NewMetricRepository constructs a repository over a migrated database.
func NewMetricRepository(database *sql.DB) (*MetricRepository, error) {
	if database == nil {
		return nil, fmt.Errorf("metric repository database is required")
	}
	return &MetricRepository{database: database}, nil
}

// SaveBatch validates and atomically persists one bounded sample batch.
func (repository *MetricRepository) SaveBatch(ctx context.Context, samples []metrics.Sample) error {
	if len(samples) < 1 || len(samples) > maximumMetricBatch {
		return metrics.ErrInvalidSample
	}
	for _, sample := range samples {
		if err := metrics.ValidateSample(sample); err != nil {
			return err
		}
	}
	return executeImmediate(ctx, repository.database, func(connection *sql.Conn) error {
		for _, sample := range samples {
			if err := insertMetricSample(ctx, connection, sample); err != nil {
				return err
			}
		}
		return nil
	})
}

func insertMetricSample(ctx context.Context, connection *sql.Conn, sample metrics.Sample) error {
	result, err := connection.ExecContext(ctx, `INSERT INTO runtime_metric_samples
        (service_instance_id,source,status,observed_at,interval_ms,cpu_total_ms,cpu_percent,memory_bytes,process_count,container_count,reason_code)
        VALUES (?,?,?,?,?,?,?,?,?,?,?)`, sample.ServiceInstanceID.String(), string(sample.Source), string(sample.Status),
		formatDatabaseTime(sample.ObservedAt), sample.Interval.Milliseconds(), nullableInt64(sample.CPUTotalMillis), nullableFloat64(sample.CPUPercent),
		nullableInt64(sample.MemoryBytes), nullableInt64(sample.ProcessCount), nullableInt64(sample.ContainerCount), nullableText(sample.ReasonCode))
	if err != nil {
		return fmt.Errorf("insert runtime metric sample: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("read runtime metric sample ID: %w", err)
	}
	for _, container := range sample.Containers {
		if _, err := connection.ExecContext(ctx, `INSERT INTO runtime_container_metric_samples
            (metric_sample_id,container_id,compose_service,cpu_percent,memory_bytes) VALUES (?,?,?,?,?)`,
			id, container.ContainerID, container.ComposeService, container.CPUPercent, container.MemoryBytes); err != nil {
			return fmt.Errorf("insert container metric sample: %w", err)
		}
	}
	return nil
}

// ListSamples returns bounded service-level detail samples.
func (repository *MetricRepository) ListSamples(ctx context.Context, query metrics.Query) ([]metrics.Sample, error) {
	if query.Hourly || metrics.ValidateQuery(query) != nil {
		return nil, metrics.ErrInvalidQuery
	}
	arguments := metricQueryArguments(query)
	rows, err := repository.database.QueryContext(ctx, `SELECT id,service_instance_id,source,status,observed_at,interval_ms,
        cpu_total_ms,cpu_percent,memory_bytes,process_count,container_count,reason_code FROM runtime_metric_samples
        WHERE service_instance_id IN (`+placeholders(len(query.ServiceInstanceIDs))+`) AND observed_at >= ? AND observed_at < ?
        ORDER BY observed_at,id LIMIT ?`, arguments...)
	if err != nil {
		return nil, fmt.Errorf("list runtime metric samples: %w", err)
	}
	defer rows.Close()
	return scanMetricSamples(rows)
}

// ListHourly returns bounded hourly aggregates.
func (repository *MetricRepository) ListHourly(ctx context.Context, query metrics.Query) ([]metrics.HourlyAggregate, error) {
	if !query.Hourly || metrics.ValidateQuery(query) != nil {
		return nil, metrics.ErrInvalidQuery
	}
	arguments := metricQueryArguments(query)
	rows, err := repository.database.QueryContext(ctx, `SELECT service_instance_id,source,bucket_start,sample_count,available_count,
        cpu_sample_count,cpu_min_percent,cpu_max_percent,cpu_total_percent,memory_sample_count,memory_min_bytes,memory_max_bytes,memory_total_bytes
        FROM runtime_metric_hourly_aggregates WHERE service_instance_id IN (`+placeholders(len(query.ServiceInstanceIDs))+`)
        AND bucket_start >= ? AND bucket_start < ? ORDER BY bucket_start,service_instance_id,source LIMIT ?`, arguments...)
	if err != nil {
		return nil, fmt.Errorf("list hourly metric aggregates: %w", err)
	}
	defer rows.Close()
	return scanMetricAggregates(rows)
}

// CompactDefault applies the accepted resource retention policy.
func (repository *MetricRepository) CompactDefault(ctx context.Context, now time.Time) (int64, error) {
	return repository.Compact(ctx, now, DefaultMetricRetentionPolicy)
}

// Compact aggregates and deletes one bounded detail batch and expires old aggregates.
func (repository *MetricRepository) Compact(ctx context.Context, now time.Time, policy MetricRetentionPolicy) (int64, error) {
	if !validMetricRetention(now, policy) {
		return 0, fmt.Errorf("invalid metric retention policy")
	}
	var removed int64
	err := executeImmediate(ctx, repository.database, func(connection *sql.Conn) error {
		detailCutoff := formatDatabaseTime(now.Add(-policy.DetailWindow))
		if err := aggregateMetricBatch(ctx, connection, detailCutoff, policy.BatchLimit); err != nil {
			return err
		}
		count, err := deleteMetricBatch(ctx, connection, detailCutoff, policy.BatchLimit)
		if err != nil {
			return err
		}
		removed = count
		return deleteOldMetricAggregates(ctx, connection, formatDatabaseTime(now.Add(-policy.AggregateWindow)), policy.BatchLimit)
	})
	return removed, err
}

func aggregateMetricBatch(ctx context.Context, connection *sql.Conn, cutoff string, limit int) error {
	_, err := connection.ExecContext(ctx, `WITH candidates AS (
        SELECT * FROM runtime_metric_samples WHERE observed_at < ? ORDER BY observed_at,id LIMIT ?
    ) INSERT INTO runtime_metric_hourly_aggregates
        (service_instance_id,source,bucket_start,sample_count,available_count,cpu_sample_count,cpu_min_percent,cpu_max_percent,cpu_total_percent,
         memory_sample_count,memory_min_bytes,memory_max_bytes,memory_total_bytes)
    SELECT service_instance_id,source,substr(observed_at,1,13)||':00:00Z',COUNT(*),SUM(status='available'),COUNT(cpu_percent),
        MIN(cpu_percent),MAX(cpu_percent),SUM(cpu_percent),COUNT(memory_bytes),MIN(memory_bytes),MAX(memory_bytes),SUM(memory_bytes)
    FROM candidates GROUP BY service_instance_id,source,substr(observed_at,1,13)
    ON CONFLICT(service_instance_id,source,bucket_start) DO UPDATE SET
        sample_count=sample_count+excluded.sample_count, available_count=available_count+excluded.available_count,
        cpu_sample_count=cpu_sample_count+excluded.cpu_sample_count,
        cpu_min_percent=CASE WHEN cpu_min_percent IS NULL THEN excluded.cpu_min_percent WHEN excluded.cpu_min_percent IS NULL THEN cpu_min_percent ELSE MIN(cpu_min_percent,excluded.cpu_min_percent) END,
        cpu_max_percent=CASE WHEN cpu_max_percent IS NULL THEN excluded.cpu_max_percent WHEN excluded.cpu_max_percent IS NULL THEN cpu_max_percent ELSE MAX(cpu_max_percent,excluded.cpu_max_percent) END,
		cpu_total_percent=CASE WHEN cpu_sample_count+excluded.cpu_sample_count=0 THEN NULL ELSE COALESCE(cpu_total_percent,0)+COALESCE(excluded.cpu_total_percent,0) END,
        memory_sample_count=memory_sample_count+excluded.memory_sample_count,
        memory_min_bytes=CASE WHEN memory_min_bytes IS NULL THEN excluded.memory_min_bytes WHEN excluded.memory_min_bytes IS NULL THEN memory_min_bytes ELSE MIN(memory_min_bytes,excluded.memory_min_bytes) END,
        memory_max_bytes=CASE WHEN memory_max_bytes IS NULL THEN excluded.memory_max_bytes WHEN excluded.memory_max_bytes IS NULL THEN memory_max_bytes ELSE MAX(memory_max_bytes,excluded.memory_max_bytes) END,
		memory_total_bytes=CASE WHEN memory_sample_count+excluded.memory_sample_count=0 THEN NULL ELSE COALESCE(memory_total_bytes,0)+COALESCE(excluded.memory_total_bytes,0) END`, cutoff, limit)
	if err != nil {
		return fmt.Errorf("aggregate runtime metrics: %w", err)
	}
	return nil
}

func deleteMetricBatch(ctx context.Context, connection *sql.Conn, cutoff string, limit int) (int64, error) {
	result, err := connection.ExecContext(ctx, `DELETE FROM runtime_metric_samples WHERE id IN (
        SELECT id FROM runtime_metric_samples WHERE observed_at < ? ORDER BY observed_at,id LIMIT ?
    )`, cutoff, limit)
	if err != nil {
		return 0, fmt.Errorf("delete compacted runtime metrics: %w", err)
	}
	count, err := result.RowsAffected()
	return count, err
}

func deleteOldMetricAggregates(ctx context.Context, connection *sql.Conn, cutoff string, limit int) error {
	_, err := connection.ExecContext(ctx, `DELETE FROM runtime_metric_hourly_aggregates WHERE (service_instance_id,source,bucket_start) IN (
        SELECT service_instance_id,source,bucket_start FROM runtime_metric_hourly_aggregates WHERE bucket_start < ? ORDER BY bucket_start LIMIT ?
    )`, cutoff, limit)
	if err != nil {
		return fmt.Errorf("delete expired metric aggregates: %w", err)
	}
	return nil
}

func scanMetricSamples(rows *sql.Rows) ([]metrics.Sample, error) {
	result := make([]metrics.Sample, 0)
	for rows.Next() {
		var sample metrics.Sample
		var serviceID, source, status, observedAt string
		var interval int64
		var cpuTotal, memory, processes, containers sql.NullInt64
		var cpu sql.NullFloat64
		var reason sql.NullString
		if err := rows.Scan(&sample.ID, &serviceID, &source, &status, &observedAt, &interval, &cpuTotal, &cpu, &memory, &processes, &containers, &reason); err != nil {
			return nil, fmt.Errorf("scan runtime metric sample: %w", err)
		}
		sample.ServiceInstanceID, sample.Source, sample.Status = domain.ServiceInstanceID(serviceID), domain.MetricSource(source), domain.MetricStatus(status)
		sample.Interval, sample.ReasonCode = time.Duration(interval)*time.Millisecond, reason.String
		sample.CPUTotalMillis, sample.MemoryBytes = int64FromNull(cpuTotal), int64FromNull(memory)
		sample.ProcessCount, sample.ContainerCount, sample.CPUPercent = int64FromNull(processes), int64FromNull(containers), float64FromNull(cpu)
		var err error
		if sample.ObservedAt, err = parseDatabaseTime(observedAt); err != nil || metrics.ValidateSample(sample) != nil {
			return nil, fmt.Errorf("validate persisted metric sample")
		}
		result = append(result, sample)
	}
	return result, rows.Err()
}

func scanMetricAggregates(rows *sql.Rows) ([]metrics.HourlyAggregate, error) {
	result := make([]metrics.HourlyAggregate, 0)
	for rows.Next() {
		var item metrics.HourlyAggregate
		var serviceID, source, bucket string
		var cpuMin, cpuMax, cpuTotal sql.NullFloat64
		var memoryMin, memoryMax, memoryTotal sql.NullInt64
		if err := rows.Scan(&serviceID, &source, &bucket, &item.SampleCount, &item.AvailableCount, &item.CPUSampleCount,
			&cpuMin, &cpuMax, &cpuTotal, &item.MemorySampleCount, &memoryMin, &memoryMax, &memoryTotal); err != nil {
			return nil, fmt.Errorf("scan hourly metric aggregate: %w", err)
		}
		item.ServiceInstanceID, item.Source = domain.ServiceInstanceID(serviceID), domain.MetricSource(source)
		item.CPUMinPercent, item.CPUMaxPercent, item.CPUTotalPercent = float64FromNull(cpuMin), float64FromNull(cpuMax), float64FromNull(cpuTotal)
		item.MemoryMinBytes, item.MemoryMaxBytes, item.MemoryTotalBytes = int64FromNull(memoryMin), int64FromNull(memoryMax), int64FromNull(memoryTotal)
		var err error
		if item.BucketStart, err = parseDatabaseTime(bucket); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func metricQueryArguments(query metrics.Query) []any {
	arguments := make([]any, 0, len(query.ServiceInstanceIDs)+3)
	for _, id := range query.ServiceInstanceIDs {
		arguments = append(arguments, id.String())
	}
	return append(arguments, formatDatabaseTime(query.Start), formatDatabaseTime(query.End), query.Limit)
}

func placeholders(count int) string { return strings.TrimSuffix(strings.Repeat("?,", count), ",") }

func nullableInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableFloat64(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}

func int64FromNull(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	result := value.Int64
	return &result
}

func float64FromNull(value sql.NullFloat64) *float64 {
	if !value.Valid {
		return nil
	}
	result := value.Float64
	return &result
}

func validMetricRetention(now time.Time, policy MetricRetentionPolicy) bool {
	return !now.IsZero() && now.Location() == time.UTC && policy.DetailWindow >= time.Hour && policy.DetailWindow <= 7*24*time.Hour &&
		policy.AggregateWindow >= 24*time.Hour && policy.AggregateWindow <= 365*24*time.Hour && policy.BatchLimit >= 1 && policy.BatchLimit <= 5000
}
