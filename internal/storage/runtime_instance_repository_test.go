package storage

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"stackpilot/internal/domain"
)

func TestRuntimeInstanceRepositoryPersistsStateAndEvents(t *testing.T) {
	database := openTestDatabase(t)
	seedRuntimeCatalog(t, database)
	repository, err := NewRuntimeInstanceRepository(database, nil)
	if err != nil {
		t.Fatalf("NewRuntimeInstanceRepository() error = %v", err)
	}
	system, service := testRuntimePair()
	operationID := domain.OperationID("op_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	if err := repository.Create(context.Background(), operationID, system, service); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	identity := domain.ProcessIdentity{
		PID: 42, StartedAt: system.StartedAt.Add(time.Second), ExecutablePath: `C:\tools\java.exe`,
		CommandDigest: system.ResolvedSpecDigest, PlatformToken: "opaque-token",
	}
	serviceResult, err := repository.AttachIdentity(context.Background(), operationID, service.ID, 1, identity, system.StartedAt.Add(time.Second))
	if err != nil {
		t.Fatalf("AttachIdentity() error = %v", err)
	}
	if serviceResult.State != domain.ServiceWaitingReady || serviceResult.StateVersion != 2 || serviceResult.Identity == nil {
		t.Fatalf("attached service = %+v", serviceResult)
	}
	serviceResult, err = repository.TransitionService(context.Background(), operationID, service.ID, 2, domain.ServiceReady, "", nil, system.StartedAt.Add(2*time.Second))
	if err != nil || serviceResult.State != domain.ServiceReady || serviceResult.StateVersion != 3 {
		t.Fatalf("ready transition = (%+v, %v)", serviceResult, err)
	}
	systemResult, err := repository.TransitionSystem(context.Background(), operationID, system.ID, domain.SystemRunning, system.StartedAt.Add(2*time.Second))
	if err != nil || systemResult.State != domain.SystemRunning {
		t.Fatalf("system transition = (%+v, %v)", systemResult, err)
	}
	var events int
	if err := database.QueryRow(`SELECT COUNT(*) FROM events WHERE instance_id = ?`, system.ID.String()).Scan(&events); err != nil || events != 5 {
		t.Fatalf("runtime event count = (%d, %v), want 5", events, err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM events WHERE instance_id = ? AND operation_id = ?`, system.ID.String(), operationID.String()).Scan(&events); err != nil || events != 5 {
		t.Fatalf("operation-scoped runtime event count = (%d, %v), want 5", events, err)
	}
}

func TestRuntimeInstanceRepositoryCompletesOneshotWithoutActiveIdentity(t *testing.T) {
	database := openTestDatabase(t)
	seedRuntimeCatalog(t, database)
	repository, _ := NewRuntimeInstanceRepository(database, nil)
	system, service := testRuntimePair()
	operationID := domain.OperationID("op_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	if err := repository.Create(context.Background(), operationID, system, service); err != nil {
		t.Fatal(err)
	}
	identity := domain.ProcessIdentity{
		PID: 42, StartedAt: system.StartedAt.Add(time.Second), ExecutablePath: `C:\tools\java.exe`,
		CommandDigest: system.ResolvedSpecDigest, PlatformToken: "opaque-token",
	}
	attached, err := repository.AttachIdentity(context.Background(), operationID, service.ID, 1, identity, system.StartedAt.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	exitCode := uint32(0)
	completed, err := repository.TransitionService(context.Background(), operationID, service.ID, attached.StateVersion, domain.ServiceCompleted, "", &exitCode, system.StartedAt.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if completed.Identity != nil || completed.ExitCode == nil || *completed.ExitCode != 0 || completed.State != domain.ServiceCompleted {
		t.Fatalf("completed oneshot = %+v", completed)
	}
}

func TestRuntimeInstanceRepositoryPersistsComposeIdentitySeparately(t *testing.T) {
	database := openTestDatabase(t)
	seedRuntimeCatalog(t, database)
	repository, _ := NewRuntimeInstanceRepository(database, nil)
	system, service := testRuntimePair()
	service.Driver = domain.DriverCompose
	operationID := domain.OperationID("op_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	if err := repository.Create(context.Background(), operationID, system, service); err != nil {
		t.Fatal(err)
	}
	attached, err := repository.AttachComposeIdentity(context.Background(), operationID, service.ID, 1, "opaque-compose-token", system.StartedAt.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if attached.Driver != domain.DriverCompose || attached.ComposeIdentity != "opaque-compose-token" || attached.Identity != nil || attached.State != domain.ServiceWaitingReady {
		t.Fatalf("attached Compose runtime = %+v", attached)
	}
	stopping, err := repository.TransitionService(context.Background(), operationID, service.ID, attached.StateVersion, domain.ServiceStopping, "", nil, system.StartedAt.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	stopped, err := repository.TransitionService(context.Background(), operationID, service.ID, stopping.StateVersion, domain.ServiceStopped, "", nil, system.StartedAt.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if stopped.ComposeIdentity != "" {
		t.Fatalf("terminal Compose identity was retained: %+v", stopped)
	}
}

func TestRuntimeInstanceRepositoryRejectsStaleAndSecondActiveInstance(t *testing.T) {
	database := openTestDatabase(t)
	seedRuntimeCatalog(t, database)
	repository, _ := NewRuntimeInstanceRepository(database, nil)
	system, service := testRuntimePair()
	operationID := domain.OperationID("op_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	if err := repository.Create(context.Background(), operationID, system, service); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if _, err := repository.TransitionService(context.Background(), operationID, service.ID, 2, domain.ServiceFailed, "TEST_FAILURE", nil, system.StartedAt); !errors.Is(err, ErrRuntimeStateConflict) {
		t.Fatalf("stale transition error = %v", err)
	}
	second := system
	second.ID = "si_01ARZ3NDEKTSV4RRFFQ69G5FAW"
	secondService := service
	secondService.ID = "svi_01ARZ3NDEKTSV4RRFFQ69G5FAW"
	secondService.SystemInstanceID = second.ID
	if err := repository.Create(context.Background(), operationID, second, secondService); !errors.Is(err, ErrActiveSystemInstance) {
		t.Fatalf("second active Create() error = %v", err)
	}
}

func TestRuntimeInstanceRepositoryCreatesWholeSystemAtomically(t *testing.T) {
	database := openTestDatabase(t)
	seedRuntimeCatalog(t, database)
	repository, _ := NewRuntimeInstanceRepository(database, nil)
	system, backend := testRuntimePair()
	web := backend
	web.ID = "svi_01ARZ3NDEKTSV4RRFFQ69G5FAW"
	web.ServiceID = "web"
	web.State = domain.ServiceWaitingDependency
	operationID := domain.OperationID("op_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	if err := repository.CreateSystem(context.Background(), operationID, system, []domain.ServiceInstance{backend, web}); err != nil {
		t.Fatalf("CreateSystem() error = %v", err)
	}
	services, err := repository.ListServices(context.Background(), system.ID)
	if err != nil || len(services) != 2 || services[1].ServiceID != "web" || services[1].State != domain.ServiceWaitingDependency {
		t.Fatalf("ListServices() = %#v, %v", services, err)
	}
	var events int
	if err := database.QueryRow(`SELECT COUNT(*) FROM events WHERE instance_id = ?`, system.ID.String()).Scan(&events); err != nil || events != 3 {
		t.Fatalf("creation events = %d, %v; want 3", events, err)
	}
}

func TestRuntimeInstanceRepositoryListsAndMarksActiveReconciliation(t *testing.T) {
	database := openTestDatabase(t)
	seedRuntimeCatalog(t, database)
	repository, _ := NewRuntimeInstanceRepository(database, nil)
	system, service := testRuntimePair()
	operationID := domain.OperationID("op_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	if err := repository.Create(context.Background(), operationID, system, service); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	instances, err := repository.ListActive(context.Background())
	if err != nil || len(instances) != 1 || instances[0].ID != system.ID {
		t.Fatalf("ListActive() = (%#v, %v)", instances, err)
	}
	reconciledAt := system.StartedAt.Add(5 * time.Minute)
	if err := repository.MarkReconciled(context.Background(), system.ID, reconciledAt); err != nil {
		t.Fatalf("MarkReconciled() error = %v", err)
	}
	got, err := repository.GetSystem(context.Background(), system.ID)
	if err != nil || got.LastReconciledAt == nil || !got.LastReconciledAt.Equal(reconciledAt) {
		t.Fatalf("reconciled system = (%#v, %v)", got, err)
	}
}

func seedRuntimeCatalog(t *testing.T, database *sql.DB) {
	t.Helper()
	now := "2026-08-18T12:00:00Z"
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO systems(id,name,created_at,updated_at) VALUES ('btc','BTC',?,?)`, []any{now, now}},
		{`INSERT INTO manifest_snapshots(digest,system_id,api_version,normalized_yaml,parsed_json,created_at) VALUES (?,'btc','stackpilot.io/v1alpha1','{}','{}',?)`, []any{testRuntimeDigest, now}},
		{`INSERT INTO workspaces(id,system_id,root_path,canonical_path,manifest_status,last_valid_digest,created_at,updated_at) VALUES (?,'btc','E:\\workspace','E:\\workspace','valid',?,?,?)`, []any{"ws_01ARZ3NDEKTSV4RRFFQ69G5FAV", testRuntimeDigest, now, now}},
		{`INSERT INTO services(workspace_id,service_id,driver,mode,required,definition_digest) VALUES (?,'backend','process','daemon',1,?)`, []any{"ws_01ARZ3NDEKTSV4RRFFQ69G5FAV", testRuntimeDigest}},
		{`INSERT INTO operations(id,workspace_id,system_id,type,state,idempotency_subject,route_key,request_digest,cancellable,created_at)
            VALUES ('op_01ARZ3NDEKTSV4RRFFQ69G5FAV',?,'btc','start','running','test','system:start',?,1,?)`, []any{"ws_01ARZ3NDEKTSV4RRFFQ69G5FAV", testRuntimeDigest, now}},
	}
	for _, statement := range statements {
		if _, err := database.Exec(statement.query, statement.args...); err != nil {
			t.Fatalf("seed runtime catalog: %v", err)
		}
	}
}

func testRuntimePair() (domain.SystemInstance, domain.ServiceInstance) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	system := domain.SystemInstance{
		ID: "si_01ARZ3NDEKTSV4RRFFQ69G5FAV", WorkspaceID: "ws_01ARZ3NDEKTSV4RRFFQ69G5FAV", SystemID: "btc",
		ManifestDigest: testRuntimeDigest, ResolvedSpecDigest: testRuntimeDigest, State: domain.SystemStarting, StartedAt: now,
	}
	service := domain.ServiceInstance{
		ID: "svi_01ARZ3NDEKTSV4RRFFQ69G5FAV", SystemInstanceID: system.ID, ServiceID: "backend",
		Driver: domain.DriverProcess, ProcessMode: domain.ProcessDaemon, State: domain.ServiceStarting, GracefulTimeout: time.Second, StateVersion: 1, CreatedAt: now, UpdatedAt: now,
	}
	return system, service
}

const testRuntimeDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
