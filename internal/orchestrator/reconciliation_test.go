package orchestrator_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"stackpilot/internal/domain"
	"stackpilot/internal/driver"
	"stackpilot/internal/health"
	"stackpilot/internal/logs"
	"stackpilot/internal/manifest"
	"stackpilot/internal/orchestrator"
	"stackpilot/internal/runner"
	"stackpilot/internal/storage"
	"stackpilot/internal/workspace"
)

func TestReconcileResumesProvenRuntimeAndMarksSystem(t *testing.T) {
	fixture := newReconciliationFixture(t, nil)
	if err := fixture.service.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	status, err := fixture.service.Status(context.Background(), fixture.workspaceID)
	if err != nil || status.Services[0].State != domain.ServiceReady || status.System.State != domain.SystemRunning {
		t.Fatalf("RuntimeStatus() = (%#v, %v)", status, err)
	}
	if status.System.LastReconciledAt == nil || len(fixture.resumed) != 1 {
		t.Fatalf("reconciliation marker/captures = (%v, %d)", status.System.LastReconciledAt, len(fixture.resumed))
	}
}

func TestReconcileMissingAndUnprovableRuntimes(t *testing.T) {
	for _, test := range []struct {
		name  string
		cause error
		state domain.ServiceState
		code  string
	}{
		{name: "missing", cause: driver.ErrRuntimeNotFound, state: domain.ServiceFailed, code: "SUPERVISOR_EXITED"},
		{name: "identity mismatch", cause: driver.ErrIdentityMismatch, state: domain.ServiceUnknown, code: "PROCESS_IDENTITY_MISMATCH"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newReconciliationFixture(t, test.cause)
			if err := fixture.service.Reconcile(context.Background()); err != nil {
				t.Fatalf("Reconcile() error = %v", err)
			}
			status, err := fixture.service.Status(context.Background(), fixture.workspaceID)
			if err != nil || status.Services[0].State != test.state || status.System.State != domain.SystemFailed {
				t.Fatalf("RuntimeStatus() = (%#v, %v)", status, err)
			}
			if len(fixture.resumed) != 0 || fixture.driver.stops != 0 {
				t.Fatalf("unsafe recovery side effects: captures=%d stops=%d", len(fixture.resumed), fixture.driver.stops)
			}
			var eventCode string
			var operationID sql.NullString
			if err := fixture.database.QueryRow(`SELECT json_extract(payload_json, '$.errorCode'), operation_id FROM events
                    WHERE service_instance_id = ? ORDER BY id DESC LIMIT 1`, fixture.serviceID.String()).Scan(&eventCode, &operationID); err != nil || eventCode != test.code || operationID.Valid {
				t.Fatalf("reconciliation event = (%q, %#v, %v), want code %q and no Operation", eventCode, operationID, err, test.code)
			}
		})
	}
}

func TestPeriodicReconciliationRejectsIntervalsBelowDesignMinimum(t *testing.T) {
	fixture := newReconciliationFixture(t, nil)
	if err := fixture.service.StartPeriodicReconciliation(9*time.Second, 30*time.Second); err == nil {
		t.Fatal("process interval below 10 seconds was accepted")
	}
	if err := fixture.service.StartPeriodicReconciliation(10*time.Second, 29*time.Second); err == nil {
		t.Fatal("lease interval below 30 seconds was accepted")
	}
}

func TestRuntimeReconciliationSkipsWorkspaceWithActiveOperation(t *testing.T) {
	fixture := newReconciliationFixture(t, driver.ErrRuntimeNotFound)
	queuedID := "op_01ARZ3NDEKTSV4RRFFQ69G5FAW"
	if _, err := fixture.database.Exec(`INSERT INTO operations
        (id,workspace_id,system_id,type,state,idempotency_subject,route_key,request_digest,cancellable,created_at)
        VALUES (?,?, 'btc','restart','queued','test','system:restart',?,1,?)`,
		queuedID, fixture.workspaceID.String(), reconciliationDigest, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("seed active Operation: %v", err)
	}
	if err := fixture.service.ReconcileRuntimes(context.Background()); err != nil {
		t.Fatalf("ReconcileRuntimes() error = %v", err)
	}
	status, err := fixture.service.Status(context.Background(), fixture.workspaceID)
	if err != nil || status.Services[0].State != domain.ServiceReady || status.System.State != domain.SystemRunning {
		t.Fatalf("active Operation runtime changed = (%#v, %v)", status, err)
	}
	if len(fixture.resumed) != 0 {
		t.Fatalf("active Operation capture count = %d, want 0", len(fixture.resumed))
	}
}

func TestReconcileDiscoversIdentityBeforeDatabaseAttachment(t *testing.T) {
	fixture := newReconciliationFixture(t, nil)
	if _, err := fixture.database.Exec(`UPDATE service_instances SET state='starting', pid=NULL,
        process_started_at=NULL, executable_path=NULL, command_digest=NULL, platform_token=NULL,
        state_version=1 WHERE id=?`, fixture.serviceID.String()); err != nil {
		t.Fatalf("clear runtime identity: %v", err)
	}
	fixture.driver.discovered = &driver.RecoveredRuntime{
		Identity:    fixture.identity,
		Observation: driver.RuntimeObservation{State: "running", Identity: fixture.identity},
	}
	if err := fixture.service.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	status, err := fixture.service.Status(context.Background(), fixture.workspaceID)
	if err != nil || status.Services[0].Identity == nil || status.Services[0].State != domain.ServiceWaitingReady {
		t.Fatalf("discovered RuntimeStatus() = (%#v, %v)", status, err)
	}
	if len(fixture.resumed) != 1 {
		t.Fatalf("discovered capture count = %d, want 1", len(fixture.resumed))
	}
}

func TestReconcileKeepsFailedRuntimeWithoutIdentity(t *testing.T) {
	fixture := newReconciliationFixture(t, nil)
	if _, err := fixture.database.Exec(`UPDATE service_instances SET state='failed', pid=NULL,
        process_started_at=NULL, executable_path=NULL, command_digest=NULL, platform_token=NULL,
        state_version=state_version+1 WHERE id=?`, fixture.serviceID.String()); err != nil {
		t.Fatalf("seed failed runtime without identity: %v", err)
	}
	if _, err := fixture.database.Exec(`UPDATE system_instances SET state='failed', last_reconciled_at=NULL WHERE id=?`, reconciliationSystemID.String()); err != nil {
		t.Fatalf("seed failed system: %v", err)
	}
	fixture.driver.discovered = &driver.RecoveredRuntime{
		Identity:    fixture.identity,
		Observation: driver.RuntimeObservation{State: "running", Identity: fixture.identity},
	}
	if err := fixture.service.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	status, err := fixture.service.Status(context.Background(), fixture.workspaceID)
	if err != nil || status.Services[0].State != domain.ServiceFailed || status.Services[0].Identity != nil {
		t.Fatalf("failed RuntimeStatus() = (%#v, %v)", status, err)
	}
	if status.System.LastReconciledAt == nil || len(fixture.resumed) != 0 {
		t.Fatalf("failed runtime reconciliation marker/captures = (%v, %d)", status.System.LastReconciledAt, len(fixture.resumed))
	}
}

func TestReconcileSettlesExitedOneshotFromPersistedMode(t *testing.T) {
	for _, test := range []struct {
		name     string
		exitCode uint32
		state    domain.ServiceState
	}{
		{name: "success", exitCode: 0, state: domain.ServiceCompleted},
		{name: "failure", exitCode: 23, state: domain.ServiceFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newReconciliationFixture(t, nil)
			if _, err := fixture.database.Exec(`UPDATE service_instances SET process_mode='oneshot', state='waiting_ready' WHERE id=?`, fixture.serviceID.String()); err != nil {
				t.Fatal(err)
			}
			fixture.driver.observation = driver.RuntimeObservation{State: "exited", Identity: fixture.identity, ExitCode: &test.exitCode}
			if err := fixture.service.Reconcile(context.Background()); err != nil {
				t.Fatal(err)
			}
			status, err := fixture.service.Status(context.Background(), fixture.workspaceID)
			if err != nil || status.Services[0].State != test.state || status.Services[0].Identity != nil || status.Services[0].ExitCode == nil || *status.Services[0].ExitCode != test.exitCode {
				t.Fatalf("reconciled oneshot = (%+v, %v)", status, err)
			}
			if fixture.driver.stops != 1 || len(fixture.resumed) != 0 {
				t.Fatalf("reconciled oneshot resources = stops %d, captures %d", fixture.driver.stops, len(fixture.resumed))
			}
		})
	}
}

type reconciliationFixture struct {
	service     *orchestrator.SingleService
	database    *sql.DB
	driver      *reconciliationDriver
	workspaceID domain.WorkspaceID
	serviceID   domain.ServiceInstanceID
	identity    domain.ProcessIdentity
	resumed     []logs.CaptureRequest
}

func newReconciliationFixture(t *testing.T, recoverError error) *reconciliationFixture {
	t.Helper()
	dataDir := t.TempDir()
	database, err := storage.OpenDataDir(context.Background(), dataDir)
	if err != nil {
		t.Fatalf("OpenDataDir() error = %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	seedReconciliationCatalog(t, database, now)
	operationsRepository, _ := storage.NewOperationRepository(database)
	operations, _ := orchestrator.NewManager(operationsRepository)
	runtimeRepository, _ := storage.NewRuntimeInstanceRepository(database, nil)
	system, runtime, identity := reconciliationRuntime(now)
	if err := runtimeRepository.Create(context.Background(), reconciliationOperationID, system, runtime); err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	attached, err := runtimeRepository.AttachIdentity(context.Background(), reconciliationOperationID, runtime.ID, 1, identity, now.Add(time.Second))
	if err != nil {
		t.Fatalf("attach identity: %v", err)
	}
	if _, err := runtimeRepository.TransitionService(context.Background(), reconciliationOperationID, runtime.ID, attached.StateVersion, domain.ServiceReady, "", nil, now.Add(2*time.Second)); err != nil {
		t.Fatalf("mark runtime ready: %v", err)
	}
	if _, err := runtimeRepository.TransitionSystem(context.Background(), reconciliationOperationID, system.ID, domain.SystemRunning, now.Add(2*time.Second)); err != nil {
		t.Fatalf("mark system running: %v", err)
	}
	driverValue := &reconciliationDriver{identity: identity, recoverError: recoverError}
	fixture := &reconciliationFixture{
		database: database, driver: driverValue, workspaceID: system.WorkspaceID,
		serviceID: runtime.ID, identity: identity,
	}
	loader, _ := manifest.NewLoader()
	workspaceRepository, _ := storage.NewWorkspaceRepository(database)
	workspaces, _ := workspace.NewManager(workspaceRepository, loader, manifest.NewValidator())
	fixture.service, err = orchestrator.NewSingleService(orchestrator.SingleServiceConfig{
		Context: context.Background(), Operations: operations, Workspaces: workspaces,
		Runner: reconciliationRunner{}, Driver: driverValue, Runtime: runtimeRepository,
		Readiness: reconciliationReadiness{}, DataDir: dataDir,
		StartLogs: func(context.Context, logs.CaptureRequest) (orchestrator.CaptureSession, error) {
			return fakeReconciliationCapture{}, nil
		},
		ResumeLogs: func(_ context.Context, request logs.CaptureRequest) (orchestrator.CaptureSession, error) {
			fixture.resumed = append(fixture.resumed, request)
			return fakeReconciliationCapture{}, nil
		},
	})
	if err != nil {
		t.Fatalf("NewSingleService() error = %v", err)
	}
	t.Cleanup(fixture.service.Wait)
	return fixture
}

func seedReconciliationCatalog(t *testing.T, database *sql.DB, now time.Time) {
	t.Helper()
	digest := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	databaseTime := now.Format(time.RFC3339Nano)
	values := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO systems(id,name,created_at,updated_at) VALUES ('btc','BTC',?,?)`, []any{now, now}},
		{`INSERT INTO manifest_snapshots(digest,system_id,api_version,normalized_yaml,parsed_json,created_at) VALUES (?,'btc','stackpilot.io/v1alpha1','{}','{}',?)`, []any{digest, now}},
		{`INSERT INTO workspaces(id,system_id,root_path,canonical_path,manifest_status,last_valid_digest,created_at,updated_at)
            VALUES (?,'btc','E:\\workspace','E:\\workspace','valid',?,?,?)`, []any{reconciliationWorkspaceID, digest, now, now}},
		{`INSERT INTO services(workspace_id,service_id,driver,mode,required,definition_digest) VALUES (?,'backend','process','daemon',1,?)`, []any{reconciliationWorkspaceID, digest}},
		{`INSERT INTO operations(id,workspace_id,system_id,type,state,idempotency_subject,route_key,request_digest,cancellable,created_at,started_at,finished_at)
            VALUES (?,?, 'btc','start','succeeded','test','system:start',?,1,?,?,?)`, []any{reconciliationOperationID, reconciliationWorkspaceID, digest, databaseTime, databaseTime, databaseTime}},
	}
	for _, value := range values {
		if _, err := database.Exec(value.query, value.args...); err != nil {
			t.Fatalf("seed reconciliation catalog: %v", err)
		}
	}
}

func reconciliationRuntime(now time.Time) (domain.SystemInstance, domain.ServiceInstance, domain.ProcessIdentity) {
	system := domain.SystemInstance{
		ID: reconciliationSystemID, WorkspaceID: reconciliationWorkspaceID, SystemID: "btc",
		ManifestDigest: reconciliationDigest, ResolvedSpecDigest: reconciliationDigest,
		State: domain.SystemStarting, StartedAt: now,
	}
	runtime := domain.ServiceInstance{
		ID: reconciliationServiceID, SystemInstanceID: system.ID, ServiceID: "backend", Driver: domain.DriverProcess, ProcessMode: domain.ProcessDaemon, State: domain.ServiceStarting,
		GracefulTimeout: time.Second, StateVersion: 1, CreatedAt: now, UpdatedAt: now,
	}
	identity := domain.ProcessIdentity{
		PID: 42, StartedAt: now.Add(time.Second), ExecutablePath: `C:\fixture.exe`,
		CommandDigest: reconciliationDigest, PlatformToken: "opaque-token",
	}
	return system, runtime, identity
}

type reconciliationDriver struct {
	identity     domain.ProcessIdentity
	recoverError error
	discovered   *driver.RecoveredRuntime
	observation  driver.RuntimeObservation
	stops        int
}

func (*reconciliationDriver) Preflight(context.Context, driver.ResolvedServiceSpec) error { return nil }
func (*reconciliationDriver) Start(context.Context, driver.StartRequest) (driver.RuntimeIdentity, error) {
	return driver.RuntimeIdentity{}, errors.New("unexpected start")
}
func (value *reconciliationDriver) Stop(context.Context, driver.StopRequest) error {
	value.stops++
	return nil
}
func (*reconciliationDriver) Inspect(context.Context, driver.RuntimeIdentity) (driver.RuntimeObservation, error) {
	return driver.RuntimeObservation{}, errors.New("unexpected inspect")
}
func (value *reconciliationDriver) Recover(context.Context, driver.RuntimeIdentity) (driver.RecoveredRuntime, error) {
	if value.recoverError != nil {
		return driver.RecoveredRuntime{}, value.recoverError
	}
	observation := value.observation
	if observation.State == "" {
		observation = driver.RuntimeObservation{State: "running", Identity: value.identity}
	}
	return driver.RecoveredRuntime{Identity: value.identity, Observation: observation}, nil
}
func (value *reconciliationDriver) Discover(context.Context, driver.DiscoveryRequest) (driver.RecoveredRuntime, error) {
	if value.discovered == nil {
		return driver.RecoveredRuntime{}, driver.ErrRuntimeNotFound
	}
	return *value.discovered, nil
}

type fakeReconciliationCapture struct{}

func (fakeReconciliationCapture) Close() error { return nil }

type reconciliationRunner struct{}

func (reconciliationRunner) Resolve(context.Context, runner.ResolveRequest) (*runner.ResolvedCommand, error) {
	return &runner.ResolvedCommand{Executable: `C:\fixture.exe`, ExecutableDigest: reconciliationDigest}, nil
}

type reconciliationReadiness struct{}

func (reconciliationReadiness) Await(context.Context, health.Request) (health.Outcome, error) {
	return health.Outcome{Ready: true}, nil
}

const (
	reconciliationDigest      = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	reconciliationWorkspaceID = domain.WorkspaceID("ws_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	reconciliationOperationID = domain.OperationID("op_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	reconciliationSystemID    = domain.SystemInstanceID("si_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	reconciliationServiceID   = domain.ServiceInstanceID("svi_01ARZ3NDEKTSV4RRFFQ69G5FAV")
)
