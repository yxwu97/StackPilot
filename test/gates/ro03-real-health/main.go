package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"time"

	_ "modernc.org/sqlite"

	"stackpilot/internal/security"
)

type report struct {
	SchemaVersion   string          `json:"schemaVersion"`
	GeneratedAt     time.Time       `json:"generatedAt"`
	DatabaseVersion int             `json:"databaseVersion"`
	GateStatus      string          `json:"gateStatus"`
	ActiveSystems   int             `json:"activeSystems"`
	RequiredDaemons int             `json:"requiredDaemons"`
	VerifiedDaemons int             `json:"verifiedDaemons"`
	Blockers        []string        `json:"blockers"`
	Services        []serviceReport `json:"services"`
}

type serviceReport struct {
	SystemID              string     `json:"systemId"`
	ServiceID             string     `json:"serviceId"`
	Driver                string     `json:"driver"`
	Mode                  string     `json:"mode"`
	Required              bool       `json:"required"`
	RuntimeState          string     `json:"runtimeState"`
	Coverage              string     `json:"coverage"`
	LatestLivenessKind    string     `json:"latestLivenessKind,omitempty"`
	LatestLivenessOK      *bool      `json:"latestLivenessSuccess,omitempty"`
	LatestLivenessAt      *time.Time `json:"latestLivenessAt,omitempty"`
	SatisfiesVerification bool       `json:"satisfiesVerification"`
}

func main() {
	databasePath := flag.String("database", "", "absolute control database opened read-only")
	expectedVersion := flag.Int("expected-version", 19, "required schema version")
	flag.Parse()
	if err := run(*databasePath, *expectedVersion); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(path string, expectedVersion int) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	database, err := openReadOnly(ctx, path)
	if err != nil {
		return err
	}
	defer database.Close()
	result, err := loadReport(ctx, database, expectedVersion)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}

func openReadOnly(ctx context.Context, path string) (*sql.DB, error) {
	if path == "" || !filepath.IsAbs(path) {
		return nil, fmt.Errorf("absolute control database path is required")
	}
	canonical, err := security.CanonicalExistingPath(path)
	if err != nil {
		return nil, fmt.Errorf("canonicalize control database: %w", err)
	}
	database, err := sql.Open("sqlite", readOnlyDSN(canonical))
	if err != nil {
		return nil, err
	}
	database.SetMaxOpenConns(1)
	if err := database.PingContext(ctx); err != nil {
		_ = database.Close()
		return nil, err
	}
	return database, nil
}

func readOnlyDSN(path string) string {
	location := filepath.ToSlash(path)
	if filepath.VolumeName(path) != "" {
		location = "/" + location
	}
	endpoint := url.URL{Scheme: "file", Path: location}
	query := url.Values{"mode": {"ro"}, "_pragma": {"query_only(ON)", "busy_timeout(5000)"}}
	endpoint.RawQuery = query.Encode()
	return endpoint.String()
}

func loadReport(ctx context.Context, database *sql.DB, expectedVersion int) (report, error) {
	var version int
	if err := database.QueryRowContext(ctx, "SELECT MAX(version) FROM schema_migrations").Scan(&version); err != nil {
		return report{}, err
	}
	if version != expectedVersion {
		return report{}, fmt.Errorf("control database version is %d, expected %d", version, expectedVersion)
	}
	rows, err := database.QueryContext(ctx, serviceQuery)
	if err != nil {
		return report{}, err
	}
	defer rows.Close()
	result := report{SchemaVersion: "ro03-real-health/v1", GeneratedAt: time.Now().UTC(), DatabaseVersion: version, Services: []serviceReport{}}
	systems := map[string]bool{}
	for rows.Next() {
		service, err := scanService(rows)
		if err != nil {
			return report{}, err
		}
		systems[service.SystemID] = true
		result.Services = append(result.Services, service)
	}
	if err := rows.Err(); err != nil {
		return report{}, err
	}
	result.ActiveSystems = len(systems)
	finalize(&result)
	return result, nil
}

const serviceQuery = `SELECT sy.id, svi.service_id, svi.driver, svi.process_mode, svc.required, svi.state,
       COALESCE(hr.kind, ''), hr.success, hr.checked_at
FROM system_instances si
JOIN workspaces w ON w.id=si.workspace_id
JOIN systems sy ON sy.id=w.system_id
JOIN service_instances svi ON svi.system_instance_id=si.id
JOIN services svc ON svc.workspace_id=w.id AND svc.service_id=svi.service_id
LEFT JOIN health_results hr ON hr.id=(
    SELECT id FROM health_results latest
    WHERE latest.service_instance_id=svi.id AND latest.purpose='liveness'
    ORDER BY latest.checked_at DESC, latest.id DESC LIMIT 1
)
WHERE si.state <> 'stopped'
ORDER BY sy.id, svi.service_id`

func scanService(rows *sql.Rows) (serviceReport, error) {
	var value serviceReport
	var required int
	var success sql.NullInt64
	var checkedAt sql.NullString
	if err := rows.Scan(&value.SystemID, &value.ServiceID, &value.Driver, &value.Mode, &required,
		&value.RuntimeState, &value.LatestLivenessKind, &success, &checkedAt); err != nil {
		return serviceReport{}, err
	}
	value.Required = required == 1
	if success.Valid {
		ok := success.Int64 == 1
		value.LatestLivenessOK = &ok
	}
	if checkedAt.Valid {
		parsed, err := time.Parse(time.RFC3339Nano, checkedAt.String)
		if err != nil {
			return serviceReport{}, err
		}
		value.LatestLivenessAt = &parsed
	}
	classify(&value)
	return value, nil
}

func classify(value *serviceReport) {
	if value.Mode == "oneshot" || value.LatestLivenessKind == "" {
		value.Coverage = "unavailable"
	} else if value.Driver == "compose" {
		value.Coverage = "container"
	} else if value.LatestLivenessKind == "process" {
		value.Coverage = "process-only"
	} else {
		value.Coverage = "business"
	}
	if !value.Required {
		value.SatisfiesVerification = true
	} else if value.Mode == "oneshot" {
		value.SatisfiesVerification = value.RuntimeState == "completed"
	} else {
		value.SatisfiesVerification = (value.Coverage == "business" || value.Coverage == "container") &&
			value.RuntimeState == "ready" && value.LatestLivenessOK != nil && *value.LatestLivenessOK
	}
}

func finalize(result *report) {
	for _, service := range result.Services {
		if service.Required && service.Mode == "daemon" {
			result.RequiredDaemons++
			if service.SatisfiesVerification {
				result.VerifiedDaemons++
			} else {
				result.Blockers = append(result.Blockers, service.SystemID+"/"+service.ServiceID)
			}
		} else if service.Required && !service.SatisfiesVerification {
			result.Blockers = append(result.Blockers, service.SystemID+"/"+service.ServiceID)
		}
	}
	if result.ActiveSystems != 5 {
		result.Blockers = append(result.Blockers, "ACTIVE_SYSTEM_COUNT")
	}
	sort.Strings(result.Blockers)
	result.GateStatus = "blocked"
	if len(result.Blockers) == 0 {
		result.GateStatus = "passed"
	}
}
