package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"stackpilot/internal/domain"
	"stackpilot/internal/incident"
)

const maximumIncidentContextBytes = 512 * 1024

// IncidentRepository persists deduplicated incidents and versioned analyses.
type IncidentRepository struct{ database *sql.DB }

// NewIncidentRepository constructs an incident repository over a migrated database.
func NewIncidentRepository(database *sql.DB) (*IncidentRepository, error) {
	if database == nil {
		return nil, fmt.Errorf("incident repository database is required")
	}
	return &IncidentRepository{database: database}, nil
}

// UpsertOpen inserts an incident or merges it into the existing open fingerprint.
func (repository *IncidentRepository) UpsertOpen(ctx context.Context, record incident.Record) (*incident.Record, bool, error) {
	if err := incident.ValidateRecord(record); err != nil {
		return nil, false, err
	}
	encoded, err := json.Marshal(record.Context)
	if err != nil || len(encoded) > maximumIncidentContextBytes {
		return nil, false, fmt.Errorf("encode bounded incident context")
	}
	result, err := repository.database.ExecContext(ctx, `INSERT INTO incidents
        (id,workspace_id,system_instance_id,service_instance_id,kind,severity,state,fingerprint,occurrence_count,
         trigger_event_id,trigger_health_result_id,context_json,first_seen_at,last_seen_at)
        VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)
        ON CONFLICT(fingerprint) WHERE state='open' DO UPDATE SET
          occurrence_count=incidents.occurrence_count+1,last_seen_at=excluded.last_seen_at,
          trigger_event_id=COALESCE(excluded.trigger_event_id,incidents.trigger_event_id),
          trigger_health_result_id=COALESCE(excluded.trigger_health_result_id,incidents.trigger_health_result_id),
          context_json=excluded.context_json`,
		record.ID.String(), record.Context.WorkspaceID.String(), nullableText(record.Context.SystemInstanceID.String()),
		nullableText(record.Context.ServiceInstanceID.String()), string(record.Kind()), string(record.Severity), string(record.State), record.Fingerprint,
		record.OccurrenceCount, nullablePositive(int64(record.TriggerEventID)), nullablePositive(record.TriggerHealthResultID), string(encoded),
		formatDatabaseTime(record.FirstSeenAt), formatDatabaseTime(record.LastSeenAt))
	if err != nil {
		return nil, false, fmt.Errorf("upsert incident: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		return nil, false, fmt.Errorf("inspect incident upsert: %w", err)
	}
	stored, err := repository.getByFingerprint(ctx, record.Fingerprint)
	return stored, stored != nil && stored.ID == record.ID, err
}

// Get returns one incident with decoded bounded context.
func (repository *IncidentRepository) Get(ctx context.Context, id domain.IncidentID) (*incident.Record, error) {
	if _, err := domain.ParseIncidentID(id.String()); err != nil {
		return nil, err
	}
	return scanIncident(repository.database.QueryRowContext(ctx, incidentSelect+` WHERE id = ?`, id.String()))
}

// UpdateContext replaces only the bounded evidence snapshot for an existing incident.
func (repository *IncidentRepository) UpdateContext(ctx context.Context, id domain.IncidentID, value incident.Context) error {
	if _, err := domain.ParseIncidentID(id.String()); err != nil {
		return err
	}
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > maximumIncidentContextBytes {
		return fmt.Errorf("encode bounded incident context")
	}
	result, err := repository.database.ExecContext(ctx, `UPDATE incidents SET context_json=? WHERE id=?`, string(encoded), id.String())
	if err != nil {
		return fmt.Errorf("update incident context: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		return fmt.Errorf("incident was not found")
	}
	return nil
}

// List returns newest-first incidents scoped to one workspace.
func (repository *IncidentRepository) List(ctx context.Context, workspaceID domain.WorkspaceID, limit int) ([]incident.Record, error) {
	if _, err := domain.ParseWorkspaceID(workspaceID.String()); err != nil || limit < 1 || limit > 200 {
		return nil, fmt.Errorf("invalid incident query")
	}
	rows, err := repository.database.QueryContext(ctx, incidentSelect+` WHERE workspace_id = ? ORDER BY last_seen_at DESC,id DESC LIMIT ?`, workspaceID.String(), limit)
	if err != nil {
		return nil, fmt.Errorf("list incidents: %w", err)
	}
	defer rows.Close()
	result := make([]incident.Record, 0)
	for rows.Next() {
		record, err := scanIncident(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, *record)
	}
	return result, rows.Err()
}

// Resolve closes an open incident without deleting evidence.
func (repository *IncidentRepository) Resolve(ctx context.Context, id domain.IncidentID, at time.Time) error {
	if _, err := domain.ParseIncidentID(id.String()); err != nil || at.IsZero() || at.Location() != time.UTC {
		return fmt.Errorf("invalid incident resolution")
	}
	result, err := repository.database.ExecContext(ctx, `UPDATE incidents SET state='resolved',resolved_at=?,last_seen_at=? WHERE id=? AND state='open'`, formatDatabaseTime(at), formatDatabaseTime(at), id.String())
	if err != nil {
		return fmt.Errorf("resolve incident: %w", err)
	}
	return requireRuntimeTransition(result)
}

// AddAnalysis persists one structured engine result.
func (repository *IncidentRepository) AddAnalysis(ctx context.Context, analysis incident.Analysis) (int64, error) {
	var object map[string]json.RawMessage
	if _, err := domain.ParseIncidentID(analysis.IncidentID.String()); err != nil || analysis.Engine == "" || analysis.SchemaVersion == "" ||
		json.Unmarshal(analysis.Result, &object) != nil || object == nil || analysis.CreatedAt.IsZero() || analysis.CreatedAt.Location() != time.UTC {
		return 0, fmt.Errorf("invalid incident analysis")
	}
	result, err := repository.database.ExecContext(ctx, `INSERT INTO incident_analyses(incident_id,engine,schema_version,result_json,created_at) VALUES (?,?,?,?,?)`,
		analysis.IncidentID.String(), analysis.Engine, analysis.SchemaVersion, string(analysis.Result), formatDatabaseTime(analysis.CreatedAt))
	if err != nil {
		return 0, fmt.Errorf("insert incident analysis: %w", err)
	}
	return result.LastInsertId()
}

// ListAnalyses returns versioned analyses in creation order for one incident.
func (repository *IncidentRepository) ListAnalyses(ctx context.Context, id domain.IncidentID, limit int) ([]incident.Analysis, error) {
	if _, err := domain.ParseIncidentID(id.String()); err != nil || limit < 1 || limit > 100 {
		return nil, fmt.Errorf("invalid incident analysis query")
	}
	rows, err := repository.database.QueryContext(ctx, `SELECT id,incident_id,engine,schema_version,result_json,created_at
        FROM incident_analyses WHERE incident_id=? ORDER BY created_at,id LIMIT ?`, id.String(), limit)
	if err != nil {
		return nil, fmt.Errorf("list incident analyses: %w", err)
	}
	defer rows.Close()
	result := make([]incident.Analysis, 0)
	for rows.Next() {
		var analysis incident.Analysis
		var incidentID, payload, createdAt string
		if err := rows.Scan(&analysis.ID, &incidentID, &analysis.Engine, &analysis.SchemaVersion, &payload, &createdAt); err != nil {
			return nil, fmt.Errorf("scan incident analysis: %w", err)
		}
		analysis.IncidentID, analysis.Result = domain.IncidentID(incidentID), json.RawMessage(payload)
		analysis.CreatedAt, err = parseDatabaseTime(createdAt)
		if err != nil {
			return nil, err
		}
		result = append(result, analysis)
	}
	return result, rows.Err()
}

func (repository *IncidentRepository) getByFingerprint(ctx context.Context, fingerprint string) (*incident.Record, error) {
	return scanIncident(repository.database.QueryRowContext(ctx, incidentSelect+` WHERE fingerprint=? AND state='open'`, fingerprint))
}

const incidentSelect = `SELECT id,workspace_id,system_instance_id,service_instance_id,kind,severity,state,fingerprint,
    occurrence_count,trigger_event_id,trigger_health_result_id,context_json,first_seen_at,last_seen_at,resolved_at FROM incidents`

func scanIncident(scanner rowScanner) (*incident.Record, error) {
	var record incident.Record
	var id, workspaceID, kind, severity, state, contextJSON, firstSeen, lastSeen string
	var systemID, serviceID, resolvedAt sql.NullString
	var eventID, healthID sql.NullInt64
	err := scanner.Scan(&id, &workspaceID, &systemID, &serviceID, &kind, &severity, &state, &record.Fingerprint,
		&record.OccurrenceCount, &eventID, &healthID, &contextJSON, &firstSeen, &lastSeen, &resolvedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, sql.ErrNoRows
	}
	if err != nil {
		return nil, fmt.Errorf("scan incident: %w", err)
	}
	record.ID, record.Severity, record.State = domain.IncidentID(id), incident.Severity(severity), incident.State(state)
	record.TriggerEventID, record.TriggerHealthResultID = domain.EventID(eventID.Int64), healthID.Int64
	if err := json.Unmarshal([]byte(contextJSON), &record.Context); err != nil {
		return nil, fmt.Errorf("decode incident context: %w", err)
	}
	record.Context.WorkspaceID = domain.WorkspaceID(workspaceID)
	record.Context.SystemInstanceID = domain.SystemInstanceID(systemID.String)
	record.Context.ServiceInstanceID = domain.ServiceInstanceID(serviceID.String)
	record.Context.Kind = incident.Kind(kind)
	record.FirstSeenAt, err = parseDatabaseTime(firstSeen)
	if err == nil {
		record.LastSeenAt, err = parseDatabaseTime(lastSeen)
	}
	if err == nil {
		record.ResolvedAt, err = parseNullableDatabaseTime(resolvedAt)
	}
	return &record, err
}

func nullablePositive(value int64) any {
	if value <= 0 {
		return nil
	}
	return value
}
