package storage

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"stackpilot/internal/domain"
	"stackpilot/internal/logs"
)

// LogSegmentRepository persists closed NDJSON segment metadata.
type LogSegmentRepository struct {
	database *sql.DB
}

// LastTimestamp returns the newest durable log timestamp for recovery follow cursors.
func (repository *LogSegmentRepository) LastTimestamp(ctx context.Context, serviceInstanceID domain.ServiceInstanceID) (time.Time, bool, error) {
	if _, err := domain.ParseServiceInstanceID(serviceInstanceID.String()); err != nil {
		return time.Time{}, false, fmt.Errorf("invalid log segment query")
	}
	var value sql.NullString
	if err := repository.database.QueryRowContext(ctx, `SELECT MAX(last_timestamp) FROM log_segments WHERE service_instance_id = ?`, serviceInstanceID.String()).Scan(&value); err != nil {
		return time.Time{}, false, fmt.Errorf("query last log timestamp: %w", err)
	}
	if !value.Valid {
		return time.Time{}, false, nil
	}
	parsed, err := parseDatabaseTime(value.String)
	return parsed, err == nil, err
}

// NewLogSegmentRepository constructs a segment index over a migrated database.
func NewLogSegmentRepository(database *sql.DB) (*LogSegmentRepository, error) {
	if database == nil {
		return nil, fmt.Errorf("log segment repository database is required")
	}
	return &LogSegmentRepository{database: database}, nil
}

// RegisterClosed inserts one immutable closed segment.
func (repository *LogSegmentRepository) RegisterClosed(ctx context.Context, segment logs.Segment) error {
	if err := validateLogSegment(segment); err != nil {
		return err
	}
	_, err := repository.database.ExecContext(ctx, `INSERT INTO log_segments
        (service_instance_id, stream, path, first_sequence, last_sequence, first_timestamp,
         last_timestamp, size_bytes, closed_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		segment.ServiceInstanceID.String(), string(segment.Stream), segment.Path, segment.FirstSequence,
		segment.LastSequence, formatDatabaseTime(segment.FirstTimestamp), formatDatabaseTime(segment.LastTimestamp),
		segment.SizeBytes, formatDatabaseTime(segment.ClosedAt))
	if err != nil {
		return fmt.Errorf("register closed log segment: %w", err)
	}
	return nil
}

// ListAfter returns sequence-overlapping segments in stable order.
func (repository *LogSegmentRepository) ListAfter(ctx context.Context, serviceInstanceID domain.ServiceInstanceID, afterSequence int64) ([]logs.Segment, error) {
	if _, err := domain.ParseServiceInstanceID(serviceInstanceID.String()); err != nil || afterSequence < 0 {
		return nil, fmt.Errorf("invalid log segment query")
	}
	rows, err := repository.database.QueryContext(ctx, `SELECT id, service_instance_id, stream, path,
        first_sequence, last_sequence, first_timestamp, last_timestamp, size_bytes, closed_at
        FROM log_segments WHERE service_instance_id = ? AND last_sequence > ?
        ORDER BY first_sequence, id`, serviceInstanceID.String(), afterSequence)
	if err != nil {
		return nil, fmt.Errorf("query log segments: %w", err)
	}
	defer rows.Close()
	return scanLogSegments(rows)
}

// SequenceBounds returns the oldest and newest retained sequence bounds.
func (repository *LogSegmentRepository) SequenceBounds(ctx context.Context, serviceInstanceID domain.ServiceInstanceID) (int64, int64, bool, error) {
	if _, err := domain.ParseServiceInstanceID(serviceInstanceID.String()); err != nil {
		return 0, 0, false, fmt.Errorf("invalid log segment query")
	}
	var first, last sql.NullInt64
	err := repository.database.QueryRowContext(ctx, `SELECT MIN(first_sequence), MAX(last_sequence)
        FROM log_segments WHERE service_instance_id = ?`, serviceInstanceID.String()).Scan(&first, &last)
	if err != nil {
		return 0, 0, false, fmt.Errorf("query log sequence bounds: %w", err)
	}
	if !first.Valid || !last.Valid {
		return 0, 0, false, nil
	}
	return first.Int64, last.Int64, true, nil
}

func scanLogSegments(rows *sql.Rows) ([]logs.Segment, error) {
	segments := make([]logs.Segment, 0)
	for rows.Next() {
		var segment logs.Segment
		var serviceInstanceID, stream, firstTimestamp, lastTimestamp, closedAt string
		if err := rows.Scan(&segment.ID, &serviceInstanceID, &stream, &segment.Path, &segment.FirstSequence,
			&segment.LastSequence, &firstTimestamp, &lastTimestamp, &segment.SizeBytes, &closedAt); err != nil {
			return nil, fmt.Errorf("scan log segment: %w", err)
		}
		segment.ServiceInstanceID, segment.Stream = domain.ServiceInstanceID(serviceInstanceID), logs.Stream(stream)
		var err error
		if segment.FirstTimestamp, err = parseDatabaseTime(firstTimestamp); err != nil {
			return nil, err
		}
		if segment.LastTimestamp, err = parseDatabaseTime(lastTimestamp); err != nil {
			return nil, err
		}
		if segment.ClosedAt, err = parseDatabaseTime(closedAt); err != nil {
			return nil, err
		}
		segments = append(segments, segment)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate log segments: %w", err)
	}
	return segments, nil
}

func validateLogSegment(segment logs.Segment) error {
	if _, err := domain.ParseServiceInstanceID(segment.ServiceInstanceID.String()); err != nil {
		return err
	}
	if segment.Stream != logs.StreamStdout && segment.Stream != logs.StreamStderr {
		return fmt.Errorf("invalid log stream")
	}
	if segment.Path == "" || segment.FirstSequence <= 0 || segment.LastSequence < segment.FirstSequence ||
		segment.SizeBytes <= 0 || segment.FirstTimestamp.IsZero() || segment.LastTimestamp.IsZero() || segment.ClosedAt.IsZero() {
		return fmt.Errorf("invalid log segment metadata")
	}
	clean := filepath.Clean(filepath.FromSlash(segment.Path))
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) ||
		(clean != "logs" && !strings.HasPrefix(clean, "logs"+string(filepath.Separator))) || strings.Contains(filepath.Base(clean), ":") {
		return fmt.Errorf("invalid log segment path")
	}
	return nil
}

var _ logs.SegmentIndex = (*LogSegmentRepository)(nil)
