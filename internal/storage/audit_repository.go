package storage

import (
	"context"
	"database/sql"
	"fmt"

	"stackpilot/internal/security"
)

// AuditRepository persists immutable security and mutation audit records.
type AuditRepository struct {
	database *sql.DB
}

// NewAuditRepository constructs an audit repository over the migrated database.
func NewAuditRepository(database *sql.DB) (*AuditRepository, error) {
	if database == nil {
		return nil, fmt.Errorf("audit repository database is required")
	}
	return &AuditRepository{database: database}, nil
}

// AppendAudit inserts one safe audit record.
func (repository *AuditRepository) AppendAudit(ctx context.Context, event security.AuditEvent) (security.AuditEvent, error) {
	if err := security.ValidateAuditEvent(event); err != nil {
		return security.AuditEvent{}, err
	}
	result, err := repository.database.ExecContext(ctx, `INSERT INTO audit_events
        (subject_type, action, target_type, target_id, result, trace_id, operation_id, client_type, error_code, occurred_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, event.SubjectType, event.Action, event.TargetType,
		nullableText(event.TargetID), event.Result, event.TraceID, nullableText(event.OperationID), event.ClientType,
		nullableText(event.ErrorCode), formatDatabaseTime(event.OccurredAt))
	if err != nil {
		return security.AuditEvent{}, fmt.Errorf("insert audit event: %w", err)
	}
	event.ID, err = result.LastInsertId()
	if err != nil {
		return security.AuditEvent{}, fmt.Errorf("read audit event ID: %w", err)
	}
	return event, nil
}

// ListAudit returns ascending audit records after a stable integer cursor.
func (repository *AuditRepository) ListAudit(ctx context.Context, after int64, limit int) ([]security.AuditEvent, error) {
	if after < 0 || limit < 1 || limit > security.MaximumAuditPageSize {
		return nil, security.ErrInvalidAuditEvent
	}
	rows, err := repository.database.QueryContext(ctx, `SELECT id, subject_type, action, target_type, target_id,
        result, trace_id, operation_id, client_type, error_code, occurred_at
        FROM audit_events WHERE id > ? ORDER BY id LIMIT ?`, after, limit)
	if err != nil {
		return nil, fmt.Errorf("list audit events: %w", err)
	}
	defer rows.Close()
	return scanAuditEvents(rows)
}

func scanAuditEvents(rows *sql.Rows) ([]security.AuditEvent, error) {
	result := make([]security.AuditEvent, 0)
	for rows.Next() {
		var event security.AuditEvent
		var targetID, operationID, errorCode sql.NullString
		var occurredAt string
		if err := rows.Scan(&event.ID, &event.SubjectType, &event.Action, &event.TargetType, &targetID,
			&event.Result, &event.TraceID, &operationID, &event.ClientType, &errorCode, &occurredAt); err != nil {
			return nil, fmt.Errorf("scan audit event: %w", err)
		}
		event.TargetID, event.OperationID, event.ErrorCode = targetID.String, operationID.String, errorCode.String
		var err error
		event.OccurredAt, err = parseDatabaseTime(occurredAt)
		if err != nil || security.ValidateAuditEvent(event) != nil {
			return nil, fmt.Errorf("invalid stored audit event")
		}
		result = append(result, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate audit events: %w", err)
	}
	return result, nil
}

var _ security.AuditStore = (*AuditRepository)(nil)
