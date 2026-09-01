package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"testing/fstest"
	"time"

	"stackpilot/internal/domain"
)

func TestMigratorUpgradesHistoricalVersion(t *testing.T) {
	database := openUnmigratedTestDatabase(t)
	first := migrationSource(map[string]string{
		"migrations/000001_alpha.sql": `CREATE TABLE alpha (id INTEGER PRIMARY KEY) STRICT;`,
	})
	all := migrationSource(map[string]string{
		"migrations/000001_alpha.sql": `CREATE TABLE alpha (id INTEGER PRIMARY KEY) STRICT;`,
		"migrations/000002_beta.sql":  `CREATE TABLE beta (id INTEGER PRIMARY KEY) STRICT;`,
	})
	applyTestMigrator(t, database, first)
	applyTestMigrator(t, database, all)
	assertTableExists(t, database, "alpha")
	assertTableExists(t, database, "beta")

	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatalf("count migration history: %v", err)
	}
	if count != 2 {
		t.Fatalf("migration count = %d, want 2", count)
	}
}

func TestProductionMigratorUpgradesVersionOneDatabase(t *testing.T) {
	database := openUnmigratedTestDatabase(t)
	versionOne, err := productionMigrations.ReadFile("migrations/000001_system_catalog.sql")
	if err != nil {
		t.Fatalf("read production version one migration: %v", err)
	}
	applyTestMigrator(t, database, migrationSource(map[string]string{
		"migrations/000001_system_catalog.sql": string(versionOne),
	}))
	if err := applyMigrations(context.Background(), database); err != nil {
		t.Fatalf("upgrade production migrations: %v", err)
	}
	assertTableExists(t, database, "operations")
	assertTableExists(t, database, "operation_steps")
	assertTableExists(t, database, "system_instances")
	assertTableExists(t, database, "service_instances")
	assertTableExists(t, database, "log_segments")
	assertTableExists(t, database, "health_results")
	assertTableExists(t, database, "events")
	assertTableExists(t, database, "port_leases")
	assertTableExists(t, database, "incidents")
	assertTableExists(t, database, "incident_analyses")
	assertTableExists(t, database, "health_hourly_aggregates")
	assertTableExists(t, database, "service_restart_attempts")
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatalf("count production migration history: %v", err)
	}
	if count != 19 {
		t.Fatalf("production migration count = %d, want 19", count)
	}
}

func TestProductionMigratorUpgradesVersionTwoDatabase(t *testing.T) {
	database := openUnmigratedTestDatabase(t)
	versionOne, err := productionMigrations.ReadFile("migrations/000001_system_catalog.sql")
	if err != nil {
		t.Fatalf("read production version one migration: %v", err)
	}
	versionTwo, err := productionMigrations.ReadFile("migrations/000002_operations.sql")
	if err != nil {
		t.Fatalf("read production version two migration: %v", err)
	}
	applyTestMigrator(t, database, migrationSource(map[string]string{
		"migrations/000001_system_catalog.sql": string(versionOne),
		"migrations/000002_operations.sql":     string(versionTwo),
	}))
	if err := applyMigrations(context.Background(), database); err != nil {
		t.Fatalf("upgrade production version two: %v", err)
	}
	assertTableExists(t, database, "log_segments")
	assertTableExists(t, database, "health_results")
	assertTableExists(t, database, "events")
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatalf("count production migration history: %v", err)
	}
	if count != 19 {
		t.Fatalf("production migration count = %d, want 19", count)
	}
}

func TestProductionMigratorUpgradesVersionThreeDatabase(t *testing.T) {
	database := openUnmigratedTestDatabase(t)
	files := make(map[string]string)
	for _, name := range []string{"000001_system_catalog.sql", "000002_operations.sql", "000003_runtime_logs.sql"} {
		contents, err := productionMigrations.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatalf("read production migration %s: %v", name, err)
		}
		files["migrations/"+name] = string(contents)
	}
	applyTestMigrator(t, database, migrationSource(files))
	if err := applyMigrations(context.Background(), database); err != nil {
		t.Fatalf("upgrade production version three: %v", err)
	}
	assertTableExists(t, database, "health_results")
	assertTableExists(t, database, "events")
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatalf("count production migration history: %v", err)
	}
	if count != 19 {
		t.Fatalf("production migration count = %d, want 19", count)
	}
}

func TestProductionMigratorUpgradesVersionFourDatabase(t *testing.T) {
	database := openUnmigratedTestDatabase(t)
	files := make(map[string]string)
	for _, name := range []string{"000001_system_catalog.sql", "000002_operations.sql", "000003_runtime_logs.sql", "000004_health_results.sql"} {
		contents, err := productionMigrations.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatalf("read production migration %s: %v", name, err)
		}
		files["migrations/"+name] = string(contents)
	}
	applyTestMigrator(t, database, migrationSource(files))
	if err := applyMigrations(context.Background(), database); err != nil {
		t.Fatalf("upgrade production version four: %v", err)
	}
	assertTableExists(t, database, "events")
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatalf("count production migration history: %v", err)
	}
	if count != 19 {
		t.Fatalf("production migration count = %d, want 19", count)
	}
}

func TestProductionMigratorUpgradesVersionFiveRuntimeRows(t *testing.T) {
	database := openUnmigratedTestDatabase(t)
	files := make(map[string]string)
	for _, name := range []string{
		"000001_system_catalog.sql", "000002_operations.sql", "000003_runtime_logs.sql",
		"000004_health_results.sql", "000005_events.sql",
	} {
		contents, err := productionMigrations.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatalf("read production migration %s: %v", name, err)
		}
		files["migrations/"+name] = string(contents)
	}
	applyTestMigrator(t, database, migrationSource(files))
	serviceID := seedRuntimeInstance(t, database)
	if err := applyMigrations(context.Background(), database); err != nil {
		t.Fatalf("upgrade production version five: %v", err)
	}
	var timeout int64
	if err := database.QueryRow(`SELECT graceful_timeout_ms FROM service_instances WHERE id = ?`, serviceID.String()).Scan(&timeout); err != nil {
		t.Fatalf("read upgraded stop policy: %v", err)
	}
	if timeout != 15000 {
		t.Fatalf("upgraded graceful timeout = %d, want 15000", timeout)
	}
}

func TestProductionMigratorUpgradesVersionElevenRuntimeMode(t *testing.T) {
	database := openUnmigratedTestDatabase(t)
	files := make(map[string]string)
	entries, err := productionMigrations.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() >= "000012_" {
			continue
		}
		contents, readErr := productionMigrations.ReadFile("migrations/" + entry.Name())
		if readErr != nil {
			t.Fatal(readErr)
		}
		files["migrations/"+entry.Name()] = string(contents)
	}
	applyTestMigrator(t, database, migrationSource(files))
	serviceID := seedRuntimeInstance(t, database)
	if err := applyMigrations(context.Background(), database); err != nil {
		t.Fatalf("upgrade production version eleven: %v", err)
	}
	var mode string
	if err := database.QueryRow(`SELECT process_mode FROM service_instances WHERE id = ?`, serviceID.String()).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "daemon" {
		t.Fatalf("historical runtime process mode = %q, want daemon", mode)
	}
}

func TestProductionMigratorUpgradesVersionTwelveComposeHealth(t *testing.T) {
	database := openUnmigratedTestDatabase(t)
	files := make(map[string]string)
	entries, err := productionMigrations.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() >= "000013_" {
			continue
		}
		contents, readErr := productionMigrations.ReadFile("migrations/" + entry.Name())
		if readErr != nil {
			t.Fatal(readErr)
		}
		files["migrations/"+entry.Name()] = string(contents)
	}
	applyTestMigrator(t, database, migrationSource(files))
	serviceID := seedRuntimeInstance(t, database)
	if _, err := database.Exec(`INSERT INTO health_results
        (service_instance_id,kind,success,duration_ms,error_code,summary,checked_at)
        VALUES (?,'tcp',0,5,'TCP_REFUSED','historical','2026-08-18T00:00:00Z')`, serviceID.String()); err != nil {
		t.Fatal(err)
	}
	if err := applyMigrations(context.Background(), database); err != nil {
		t.Fatalf("upgrade production version twelve: %v", err)
	}
	var id int64
	var kind, summary string
	if err := database.QueryRow(`SELECT id,kind,summary FROM health_results`).Scan(&id, &kind, &summary); err != nil {
		t.Fatal(err)
	}
	if id != 1 || kind != "tcp" || summary != "historical" {
		t.Fatalf("upgraded health row = (%d, %q, %q)", id, kind, summary)
	}
	if _, err := database.Exec(`INSERT INTO health_results
        (service_instance_id,kind,success,duration_ms,error_code,summary,checked_at)
        VALUES (?,'compose',0,5,'CONTAINER_UNHEALTHY','unhealthy','2026-08-18T00:00:01Z')`, serviceID.String()); err != nil {
		t.Fatalf("insert Compose health after upgrade: %v", err)
	}
}

func TestProductionMigratorUpgradesVersionThirteenRuntimeDriver(t *testing.T) {
	database := openUnmigratedTestDatabase(t)
	files := make(map[string]string)
	entries, err := productionMigrations.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() >= "000014_" {
			continue
		}
		contents, readErr := productionMigrations.ReadFile("migrations/" + entry.Name())
		if readErr != nil {
			t.Fatal(readErr)
		}
		files["migrations/"+entry.Name()] = string(contents)
	}
	applyTestMigrator(t, database, migrationSource(files))
	serviceID := seedRuntimeInstance(t, database)
	if err := applyMigrations(context.Background(), database); err != nil {
		t.Fatalf("upgrade production version thirteen: %v", err)
	}
	var driver string
	var composeToken sql.NullString
	if err := database.QueryRow(`SELECT driver, compose_project_token FROM service_instances WHERE id = ?`, serviceID.String()).Scan(&driver, &composeToken); err != nil {
		t.Fatal(err)
	}
	if driver != "process" || composeToken.Valid {
		t.Fatalf("historical runtime driver = (%q, %#v)", driver, composeToken)
	}
	if _, err := database.Exec(`UPDATE service_instances SET driver = 'compose', compose_project_token = 'opaque' WHERE id = ?`, serviceID.String()); err != nil {
		t.Fatalf("store Compose identity after upgrade: %v", err)
	}
	if _, err := database.Exec(`UPDATE service_instances SET driver = 'process', compose_project_token = 'invalid' WHERE id = ?`, serviceID.String()); err == nil {
		t.Fatal("process runtime unexpectedly accepted Compose identity")
	}
}

func TestProductionMigratorUpgradesPhase20Database(t *testing.T) {
	database := openUnmigratedTestDatabase(t)
	files := make(map[string]string)
	entries, err := productionMigrations.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() >= "000015_" {
			continue
		}
		contents, readErr := productionMigrations.ReadFile("migrations/" + entry.Name())
		if readErr != nil {
			t.Fatal(readErr)
		}
		files["migrations/"+entry.Name()] = string(contents)
	}
	applyTestMigrator(t, database, migrationSource(files))
	serviceID := seedRuntimeInstance(t, database)
	if _, err := database.Exec(`INSERT INTO health_results
        (service_instance_id,kind,success,duration_ms,error_code,summary,checked_at)
		VALUES (?,'process',1,7,NULL,'phase-2.0-row','2026-08-18T00:00:00Z')`, serviceID.String()); err != nil {
		t.Fatal(err)
	}
	if err := applyMigrations(context.Background(), database); err != nil {
		t.Fatalf("upgrade Phase 2.0 database: %v", err)
	}
	for _, table := range []string{"health_hourly_aggregates", "incidents", "incident_analyses", "service_restart_attempts"} {
		assertTableExists(t, database, table)
	}
	var summary string
	if err := database.QueryRow(`SELECT summary FROM health_results WHERE service_instance_id=?`, serviceID.String()).Scan(&summary); err != nil || summary != "phase-2.0-row" {
		t.Fatalf("historical Phase 2.0 health row = (%q, %v)", summary, err)
	}
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil || count != 19 {
		t.Fatalf("Phase 2E migration count = (%d, %v)", count, err)
	}
}

func TestProductionMigratorUpgradesVersionSevenDatabase(t *testing.T) {
	database := openUnmigratedTestDatabase(t)
	files := make(map[string]string)
	for _, name := range []string{
		"000001_system_catalog.sql", "000002_operations.sql", "000003_runtime_logs.sql",
		"000004_health_results.sql", "000005_events.sql", "000006_service_stop_policy.sql",
		"000007_port_plans.sql",
	} {
		contents, err := productionMigrations.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatalf("read production migration %s: %v", name, err)
		}
		files["migrations/"+name] = string(contents)
	}
	applyTestMigrator(t, database, migrationSource(files))
	if err := applyMigrations(context.Background(), database); err != nil {
		t.Fatalf("upgrade production version seven: %v", err)
	}
	assertTableExists(t, database, "auth_tokens")
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil || count != 19 {
		t.Fatalf("migration count after version seven upgrade = (%d, %v)", count, err)
	}
}

func TestProductionMigratorUpgradesVersionEightDatabase(t *testing.T) {
	database := openUnmigratedTestDatabase(t)
	files := make(map[string]string)
	for _, name := range []string{
		"000001_system_catalog.sql", "000002_operations.sql", "000003_runtime_logs.sql",
		"000004_health_results.sql", "000005_events.sql", "000006_service_stop_policy.sql",
		"000007_port_plans.sql", "000008_local_auth.sql",
	} {
		contents, err := productionMigrations.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatalf("read production migration %s: %v", name, err)
		}
		files["migrations/"+name] = string(contents)
	}
	applyTestMigrator(t, database, migrationSource(files))
	if err := applyMigrations(context.Background(), database); err != nil {
		t.Fatalf("upgrade production version eight: %v", err)
	}
	assertTableExists(t, database, "auth_token_rotation")
	assertTableExists(t, database, "audit_events")
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil || count != 19 {
		t.Fatalf("migration count after version eight upgrade = (%d, %v)", count, err)
	}
}

func TestProductionMigratorUpgradesVersionNineDatabase(t *testing.T) {
	database := openUnmigratedTestDatabase(t)
	files := make(map[string]string)
	for _, name := range []string{
		"000001_system_catalog.sql", "000002_operations.sql", "000003_runtime_logs.sql",
		"000004_health_results.sql", "000005_events.sql", "000006_service_stop_policy.sql",
		"000007_port_plans.sql", "000008_local_auth.sql", "000009_security_audit.sql",
	} {
		contents, err := productionMigrations.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatalf("read production migration %s: %v", name, err)
		}
		files["migrations/"+name] = string(contents)
	}
	applyTestMigrator(t, database, migrationSource(files))
	if err := applyMigrations(context.Background(), database); err != nil {
		t.Fatalf("upgrade production version nine: %v", err)
	}
	assertTableExists(t, database, "secret_metadata")
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil || count != 19 {
		t.Fatalf("migration count after version nine upgrade = (%d, %v)", count, err)
	}
}

func TestProductionMigratorUpgradesVersionTenDatabase(t *testing.T) {
	database := openUnmigratedTestDatabase(t)
	files := make(map[string]string)
	for _, name := range []string{
		"000001_system_catalog.sql", "000002_operations.sql", "000003_runtime_logs.sql",
		"000004_health_results.sql", "000005_events.sql", "000006_service_stop_policy.sql",
		"000007_port_plans.sql", "000008_local_auth.sql", "000009_security_audit.sql",
		"000010_secret_metadata.sql",
	} {
		contents, err := productionMigrations.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatalf("read production migration %s: %v", name, err)
		}
		files["migrations/"+name] = string(contents)
	}
	applyTestMigrator(t, database, migrationSource(files))
	if err := applyMigrations(context.Background(), database); err != nil {
		t.Fatalf("upgrade production version ten: %v", err)
	}
	assertTableExists(t, database, "service_instance_secret_versions")
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil || count != 19 {
		t.Fatalf("migration count after version ten upgrade = (%d, %v)", count, err)
	}
}

func TestProductionMigratorUpgradesVersionSixteenWithoutLosingOperationEvidence(t *testing.T) {
	database := openUnmigratedTestDatabase(t)
	files := make(map[string]string)
	entries, err := productionMigrations.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() >= "000017_" {
			continue
		}
		contents, readErr := productionMigrations.ReadFile("migrations/" + entry.Name())
		if readErr != nil {
			t.Fatal(readErr)
		}
		files["migrations/"+entry.Name()] = string(contents)
	}
	applyTestMigrator(t, database, migrationSource(files))
	serviceInstanceID := seedRuntimeInstance(t, database)
	seedVersionSixteenOperationEvidence(t, database, serviceInstanceID)

	if err := applyMigrations(context.Background(), database); err != nil {
		t.Fatalf("upgrade production version sixteen: %v", err)
	}
	for _, table := range []string{"system_revision_snapshots", "change_plans", "runtime_metric_samples", "runtime_metric_hourly_aggregates"} {
		assertTableExists(t, database, table)
	}
	for _, table := range []string{"operations", "operation_steps", "events", "port_leases"} {
		var count int
		if err := database.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("preserved %s rows = (%d, %v), want 1", table, count, err)
		}
	}
	assertVersionSeventeenOperationTypes(t, database)
	assertNoForeignKeyViolations(t, database)
}

func TestProductionMigratorUpgradesVersionEighteenWithoutInventingLiveness(t *testing.T) {
	database := openUnmigratedTestDatabase(t)
	files := make(map[string]string)
	entries, err := productionMigrations.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() >= "000019_" {
			continue
		}
		contents, readErr := productionMigrations.ReadFile("migrations/" + entry.Name())
		if readErr != nil {
			t.Fatal(readErr)
		}
		files["migrations/"+entry.Name()] = string(contents)
	}
	applyTestMigrator(t, database, migrationSource(files))
	serviceID := seedRuntimeInstance(t, database)
	if _, err := database.Exec(`INSERT INTO health_results(service_instance_id,kind,success,duration_ms,summary,checked_at)
        VALUES (?,'http',1,5,'legacy health','2026-08-31T00:00:00Z')`, serviceID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO health_hourly_aggregates(service_instance_id,kind,bucket_start,check_count,success_count,duration_total_ms,duration_max_ms)
        VALUES (?,'http','2026-08-31T00:00:00Z',1,1,5,5)`, serviceID.String()); err != nil {
		t.Fatal(err)
	}
	if err := applyMigrations(context.Background(), database); err != nil {
		t.Fatalf("upgrade production version eighteen: %v", err)
	}
	var detailPurpose, aggregatePurpose string
	if err := database.QueryRow(`SELECT purpose FROM health_results`).Scan(&detailPurpose); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT purpose FROM health_hourly_aggregates`).Scan(&aggregatePurpose); err != nil {
		t.Fatal(err)
	}
	if detailPurpose != "readiness" || aggregatePurpose != "readiness" {
		t.Fatalf("historical health purposes = %q / %q", detailPurpose, aggregatePurpose)
	}
}

func seedVersionSixteenOperationEvidence(t *testing.T, database *sql.DB, serviceInstanceID domain.ServiceInstanceID) {
	t.Helper()
	const operationID = "op_01ARZ3NDEKTSV4RRFFQ69G5FAX"
	now := "2026-08-18T12:01:00Z"
	statements := []string{
		`INSERT INTO operations(id,workspace_id,system_id,type,state,idempotency_subject,route_key,request_digest,cancellable,created_at,finished_at) VALUES ('` + operationID + `','ws_01ARZ3NDEKTSV4RRFFQ69G5FAV','btc','start','succeeded','test','system:start','bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',0,'` + now + `','` + now + `')`,
		`INSERT INTO operation_steps(operation_id,step_no,step_key,state,attempt,started_at,finished_at) VALUES ('` + operationID + `',1,'start','succeeded',1,'` + now + `','` + now + `')`,
		`INSERT INTO events(event_type,workspace_id,system_id,instance_id,service_instance_id,operation_id,payload_json,occurred_at) VALUES ('operation.succeeded','ws_01ARZ3NDEKTSV4RRFFQ69G5FAV','btc','si_01ARZ3NDEKTSV4RRFFQ69G5FAV','` + serviceInstanceID.String() + `','` + operationID + `','{}','` + now + `')`,
		`INSERT INTO port_leases(id,plan_id,workspace_id,instance_id,operation_id,manifest_digest,logical_name,protocol,host,port,state,expires_at,created_at,updated_at) VALUES ('pl_01ARZ3NDEKTSV4RRFFQ69G5FAX','plan','ws_01ARZ3NDEKTSV4RRFFQ69G5FAV','si_01ARZ3NDEKTSV4RRFFQ69G5FAV','` + operationID + `','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','http','tcp','127.0.0.1',32101,'released','` + now + `','` + now + `','` + now + `')`,
	}
	for _, statement := range statements {
		if _, err := database.Exec(statement); err != nil {
			t.Fatalf("seed version sixteen operation evidence: %v", err)
		}
	}
}

func assertVersionSeventeenOperationTypes(t *testing.T, database *sql.DB) {
	t.Helper()
	for index, operationType := range []string{"change-plan", "verified-restart"} {
		_, err := database.Exec(`INSERT INTO operations(id,workspace_id,system_id,type,state,idempotency_subject,route_key,request_digest,cancellable,created_at,finished_at) VALUES (?,?,?,?, 'succeeded','test',?,'cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc',0,'2026-08-18T12:02:00Z','2026-08-18T12:02:00Z')`,
			fmt.Sprintf("op_01ARZ3NDEKTSV4RRFFQ69G5FA%d", index), "ws_01ARZ3NDEKTSV4RRFFQ69G5FAV", "btc", operationType, operationType)
		if err != nil {
			t.Fatalf("insert %s operation after upgrade: %v", operationType, err)
		}
	}
	if _, err := database.Exec(`INSERT INTO operations(id,workspace_id,system_id,type,state,idempotency_subject,route_key,request_digest,cancellable,created_at) VALUES ('op_01ARZ3NDEKTSV4RRFFQ69G5FAZ','ws_01ARZ3NDEKTSV4RRFFQ69G5FAV','btc','upgrade','queued','test','upgrade','dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd',0,'2026-08-18T12:02:00Z')`); err == nil {
		t.Fatal("unbounded upgrade operation type unexpectedly persisted")
	}
}

func assertNoForeignKeyViolations(t *testing.T, database *sql.DB) {
	t.Helper()
	rows, err := database.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("migration left a foreign key violation")
	}
}

func TestFailedMigrationRollsBackSchemaAndHistory(t *testing.T) {
	database := openUnmigratedTestDatabase(t)
	source := migrationSource(map[string]string{
		"migrations/000001_alpha.sql":  `CREATE TABLE alpha (id INTEGER PRIMARY KEY) STRICT;`,
		"migrations/000002_broken.sql": `CREATE TABLE transient_table (id INTEGER); INVALID SQL;`,
	})
	migrator, err := newMigrator(source, time.Now)
	if err != nil {
		t.Fatalf("newMigrator() error = %v", err)
	}
	if err := migrator.apply(context.Background(), database); err == nil {
		t.Fatal("broken migration unexpectedly succeeded")
	}
	assertTableExists(t, database, "alpha")
	assertTableMissing(t, database, "transient_table")

	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatalf("count migration history: %v", err)
	}
	if count != 1 {
		t.Fatalf("migration count = %d, want 1", count)
	}
}

func TestForeignKeysOffMigrationRollsBackViolationAndRestoresEnforcement(t *testing.T) {
	database := openUnmigratedTestDatabase(t)
	source := migrationSource(map[string]string{
		"migrations/000001_parent.sql": `
CREATE TABLE parent(id INTEGER PRIMARY KEY) STRICT;
CREATE TABLE child(id INTEGER PRIMARY KEY, parent_id INTEGER NOT NULL REFERENCES parent(id)) STRICT;
INSERT INTO parent(id) VALUES (1);
INSERT INTO child(id,parent_id) VALUES (1,1);`,
		"migrations/000002_break_parent.sql": foreignKeysOffDirective + `
DROP TABLE parent;
CREATE TABLE parent(id INTEGER PRIMARY KEY, name TEXT) STRICT;`,
	})
	migrator, err := newMigrator(source, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrator.apply(context.Background(), database); err == nil {
		t.Fatal("foreign-key violating migration unexpectedly succeeded")
	}
	var parentCount, migrationCount, foreignKeys int
	if err := database.QueryRow(`SELECT COUNT(*) FROM parent`).Scan(&parentCount); err != nil || parentCount != 1 {
		t.Fatalf("rolled-back parent rows = (%d, %v), want 1", parentCount, err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&migrationCount); err != nil || migrationCount != 1 {
		t.Fatalf("migration history = (%d, %v), want 1", migrationCount, err)
	}
	if err := database.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil || foreignKeys != 1 {
		t.Fatalf("foreign key enforcement = (%d, %v), want 1", foreignKeys, err)
	}
}

func TestMigratorRejectsUnknownHistory(t *testing.T) {
	database := openUnmigratedTestDatabase(t)
	source := migrationSource(map[string]string{
		"migrations/000001_alpha.sql": `CREATE TABLE alpha (id INTEGER PRIMARY KEY) STRICT;`,
	})
	applyTestMigrator(t, database, source)
	if _, err := database.Exec(`UPDATE schema_migrations SET version = 2 WHERE version = 1`); err != nil {
		t.Fatalf("alter migration history: %v", err)
	}
	migrator, err := newMigrator(source, time.Now)
	if err != nil {
		t.Fatalf("newMigrator() error = %v", err)
	}
	if err := migrator.apply(context.Background(), database); !errors.Is(err, ErrMigrationHistoryInvalid) {
		t.Fatalf("apply() error = %v, want invalid history", err)
	}
}

func TestMigratorRecordsUTCApplicationTime(t *testing.T) {
	database := openUnmigratedTestDatabase(t)
	source := migrationSource(map[string]string{
		"migrations/000001_alpha.sql": `CREATE TABLE alpha (id INTEGER PRIMARY KEY) STRICT;`,
	})
	fixed := time.Date(2026, 8, 17, 23, 30, 0, 123, time.FixedZone("offset", 8*60*60))
	migrator, err := newMigrator(source, func() time.Time { return fixed })
	if err != nil {
		t.Fatalf("newMigrator() error = %v", err)
	}
	if err := migrator.apply(context.Background(), database); err != nil {
		t.Fatalf("apply() error = %v", err)
	}
	var appliedAt string
	if err := database.QueryRow(`SELECT applied_at FROM schema_migrations WHERE version = 1`).Scan(&appliedAt); err != nil {
		t.Fatalf("read application time: %v", err)
	}
	if appliedAt != "2026-08-17T15:30:00.000000123Z" {
		t.Fatalf("applied_at = %q, want normalized UTC timestamp", appliedAt)
	}
}

func applyTestMigrator(t *testing.T, database *sql.DB, source fstest.MapFS) {
	t.Helper()
	migrator, err := newMigrator(source, time.Now)
	if err != nil {
		t.Fatalf("newMigrator() error = %v", err)
	}
	if err := migrator.apply(context.Background(), database); err != nil {
		t.Fatalf("apply() error = %v", err)
	}
}

func migrationSource(files map[string]string) fstest.MapFS {
	source := make(fstest.MapFS, len(files))
	for name, contents := range files {
		source[name] = &fstest.MapFile{Data: []byte(contents)}
	}
	return source
}

func openUnmigratedTestDatabase(t *testing.T) *sql.DB {
	t.Helper()
	path := t.TempDir() + `/migration.db`
	database, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	configurePool(database)
	if err := database.Ping(); err != nil {
		database.Close()
		t.Fatalf("ping test database: %v", err)
	}
	t.Cleanup(func() { closeDatabase(t, database) })
	return database
}

func assertTableMissing(t *testing.T, database *sql.DB, name string) {
	t.Helper()
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type = 'table' AND name = ?`, name).Scan(&count); err != nil {
		t.Fatalf("query table %s: %v", name, err)
	}
	if count != 0 {
		t.Errorf("table %s count = %d, want 0", name, count)
	}
}
