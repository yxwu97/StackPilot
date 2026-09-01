package orchestrator_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"stackpilot/internal/changeplan"
	"stackpilot/internal/domain"
	"stackpilot/internal/driver"
	"stackpilot/internal/events"
	"stackpilot/internal/health"
	"stackpilot/internal/incident"
	"stackpilot/internal/logs"
	"stackpilot/internal/manifest"
	"stackpilot/internal/orchestrator"
	"stackpilot/internal/ports"
	"stackpilot/internal/revision"
	"stackpilot/internal/runner"
	"stackpilot/internal/storage"
	"stackpilot/internal/workspace"
)

func TestSystemStartUsesDependencyOrderAndPersistsPlan(t *testing.T) {
	harness := newSystemServiceHarness(t, immediateReadiness{})
	result, err := harness.service.SubmitStart(context.Background(), orchestrator.StartSingleServiceInput{
		WorkspaceID: harness.workspace.ID, SystemID: harness.workspace.SystemID,
		IdempotencySubject: "test-user", IdempotencyKey: "system-start", Request: []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("SubmitStart() error = %v", err)
	}
	operation := awaitOperation(t, harness.service, result.Operation.ID, domain.OperationSucceeded)
	if len(operation.Steps) != 10 {
		t.Fatalf("system start steps = %d, want 10", len(operation.Steps))
	}
	started, _ := harness.driver.serviceOrder()
	if !reflect.DeepEqual(started, []string{"backend", "web"}) {
		t.Fatalf("start order = %#v", started)
	}
	status, err := harness.service.Status(context.Background(), harness.workspace.ID)
	if err != nil || status.System == nil || status.System.State != domain.SystemRunning || len(status.Services) != 2 {
		t.Fatalf("running system status = %#v, %v", status, err)
	}
	for _, runtime := range status.Services {
		if err := harness.healthResults.Record(context.Background(), runtime.ID, health.Result{
			Purpose: health.PurposeLiveness, Kind: health.KindTCP, Success: true,
			CheckedAt: time.Now().UTC(), Duration: time.Millisecond, Summary: "reachable",
		}); err != nil {
			t.Fatalf("record liveness for %s: %v", runtime.ServiceID, err)
		}
	}
	status, err = harness.service.Status(context.Background(), harness.workspace.ID)
	if err != nil {
		t.Fatalf("Status() with liveness evidence error = %v", err)
	}
	for _, runtime := range status.Services {
		coverage := status.HealthCoverage[runtime.ID]
		if coverage.Coverage != domain.HealthCoverageBusiness || coverage.Latest == nil ||
			coverage.Latest.Purpose != health.PurposeLiveness || !coverage.SatisfiesVerification {
			t.Fatalf("health coverage for %s = %#v", runtime.ServiceID, coverage)
		}
	}
	var bound, specs int
	if err := harness.database.QueryRow(`SELECT COUNT(*) FROM port_leases WHERE state='bound'`).Scan(&bound); err != nil || bound != 2 {
		t.Fatalf("bound leases = %d, %v", bound, err)
	}
	if err := harness.database.QueryRow(`SELECT COUNT(*) FROM resolved_system_specs`).Scan(&specs); err != nil || specs != 1 {
		t.Fatalf("resolved specs = %d, %v", specs, err)
	}
	stop, err := harness.service.SubmitStop(context.Background(), orchestrator.StopSingleServiceInput{
		WorkspaceID: harness.workspace.ID, SystemID: harness.workspace.SystemID,
		IdempotencySubject: "test-user", IdempotencyKey: "system-stop", Request: []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("SubmitStop() error = %v", err)
	}
	awaitOperation(t, harness.service, stop.Operation.ID, domain.OperationSucceeded)
	_, stopped := harness.driver.serviceOrder()
	if !reflect.DeepEqual(stopped, []string{"web", "backend"}) {
		t.Fatalf("stop order = %#v", stopped)
	}
	status, err = harness.service.Status(context.Background(), harness.workspace.ID)
	if err != nil || status.System != nil {
		t.Fatalf("stopped system status = %#v, %v", status, err)
	}
	var released int
	if err := harness.database.QueryRow(`SELECT COUNT(*) FROM port_leases WHERE state='released'`).Scan(&released); err != nil || released != 2 {
		t.Fatalf("released leases = %d, %v", released, err)
	}
}

func TestSystemStartFailureFreezesDependentByDefault(t *testing.T) {
	harness := newSystemServiceHarness(t, failedReadiness{})
	result, err := harness.service.SubmitStart(context.Background(), orchestrator.StartSingleServiceInput{
		WorkspaceID: harness.workspace.ID, SystemID: harness.workspace.SystemID,
		IdempotencySubject: "test-user", IdempotencyKey: "system-failure", Request: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	operation := awaitOperation(t, harness.service, result.Operation.ID, domain.OperationFailed)
	if operation.ErrorCode != string(health.CodeReadinessTimeout) {
		t.Fatalf("failure code = %q", operation.ErrorCode)
	}
	started, stopped := harness.driver.serviceOrder()
	if !reflect.DeepEqual(started, []string{"backend"}) || len(stopped) != 0 {
		t.Fatalf("driver order = start %#v, stop %#v", started, stopped)
	}
	status, _ := harness.service.Status(context.Background(), harness.workspace.ID)
	states := serviceStates(status)
	if states["backend"] != domain.ServiceFailed || states["web"] != domain.ServiceWaitingDependency || status.System.State != domain.SystemFailed {
		t.Fatalf("retained system states = %#v, system %s", states, status.System.State)
	}
}

func TestCompletedDependencyReleasesDownstreamAndRepeatedStartDoesNotRerun(t *testing.T) {
	harness := newCompletedDependencyHarness(t)
	harness.driver.setObservation("exited", uint32PointerForTest(0))
	first := submitSystemHarnessStart(t, harness, "completed-first")
	operation := awaitOperation(t, harness.service, first.Operation.ID, domain.OperationSucceeded)
	if stepNumberByKey(operation, "wait-complete:setup") == 0 {
		t.Fatalf("completed dependency steps = %+v", operation.Steps)
	}
	status, err := harness.service.Status(context.Background(), harness.workspace.ID)
	states := serviceStates(status)
	if err != nil || status.System.State != domain.SystemRunning || states["setup"] != domain.ServiceCompleted || states["app"] != domain.ServiceReady {
		t.Fatalf("completed dependency status = (%+v, %v)", status, err)
	}
	firstInstance := status.System.ID
	started, _ := harness.driver.serviceOrder()
	if !reflect.DeepEqual(started, []string{"setup", "app"}) {
		t.Fatalf("completed dependency start order = %#v", started)
	}

	repeated := submitSystemHarnessStart(t, harness, "completed-repeat")
	repeatedOperation := awaitOperation(t, harness.service, repeated.Operation.ID, domain.OperationSucceeded)
	for _, step := range repeatedOperation.Steps {
		if step.State != domain.OperationStepSkipped {
			t.Fatalf("repeated start step = %+v, want skipped", step)
		}
	}
	status, _ = harness.service.Status(context.Background(), harness.workspace.ID)
	started, _ = harness.driver.serviceOrder()
	if status.System.ID != firstInstance || !reflect.DeepEqual(started, []string{"setup", "app"}) {
		t.Fatalf("repeated start reran services: instance %s, starts %#v", status.System.ID, started)
	}

	restart, err := harness.service.SubmitRestart(context.Background(), orchestrator.RestartSystemInput{
		WorkspaceID: harness.workspace.ID, SystemID: harness.workspace.SystemID,
		IdempotencySubject: "test-user", IdempotencyKey: "completed-restart", Request: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	awaitOperation(t, harness.service, restart.Operation.ID, domain.OperationSucceeded)
	status, _ = harness.service.Status(context.Background(), harness.workspace.ID)
	started, stopped := harness.driver.serviceOrder()
	if status.System.ID == firstInstance || !reflect.DeepEqual(started, []string{"setup", "app", "setup", "app"}) ||
		!reflect.DeepEqual(stopped, []string{"setup", "app", "setup"}) {
		t.Fatalf("explicit restart did not rerun completed dependency: instance %s, starts %#v, stops %#v", status.System.ID, started, stopped)
	}
}

func TestCompletedDependencyFailureFreezesDownstream(t *testing.T) {
	harness := newCompletedDependencyHarness(t)
	harness.driver.setObservation("exited", uint32PointerForTest(23))
	result := submitSystemHarnessStart(t, harness, "completed-failure")
	operation := awaitOperation(t, harness.service, result.Operation.ID, domain.OperationFailed)
	if operation.ErrorCode != "PROCESS_EXITED" {
		t.Fatalf("completed dependency failure code = %q", operation.ErrorCode)
	}
	status, _ := harness.service.Status(context.Background(), harness.workspace.ID)
	states := serviceStates(status)
	if states["setup"] != domain.ServiceFailed || states["app"] != domain.ServiceWaitingDependency {
		t.Fatalf("failed completed dependency states = %#v", states)
	}
	started, _ := harness.driver.serviceOrder()
	if !reflect.DeepEqual(started, []string{"setup"}) {
		t.Fatalf("failed completed dependency starts = %#v", started)
	}
}

func submitSystemHarnessStart(t *testing.T, harness systemServiceHarness, key string) *orchestrator.CreateResult {
	t.Helper()
	result, err := harness.service.SubmitStart(context.Background(), orchestrator.StartSingleServiceInput{
		WorkspaceID: harness.workspace.ID, SystemID: harness.workspace.SystemID,
		IdempotencySubject: "test-user", IdempotencyKey: key, Request: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func stepNumberByKey(operation *orchestrator.Operation, key string) int {
	for _, step := range operation.Steps {
		if step.Key == key {
			return step.Number
		}
	}
	return 0
}

func TestSystemStartCleanupStopsInReverseTopology(t *testing.T) {
	harness := newSystemServiceHarness(t, &sequenceReadiness{outcomes: []bool{true, false}})
	cleanup, keepReady := true, false
	result, err := harness.service.SubmitStart(context.Background(), orchestrator.StartSingleServiceInput{
		WorkspaceID: harness.workspace.ID, SystemID: harness.workspace.SystemID,
		IdempotencySubject: "test-user", IdempotencyKey: "system-cleanup", Request: []byte(`{}`),
		FailurePolicy: orchestrator.FailurePolicyOverride{CleanupOnFailure: &cleanup, KeepReadyServices: &keepReady},
	})
	if err != nil {
		t.Fatal(err)
	}
	awaitOperation(t, harness.service, result.Operation.ID, domain.OperationFailed)
	started, stopped := harness.driver.serviceOrder()
	if !reflect.DeepEqual(started, []string{"backend", "web"}) || !reflect.DeepEqual(stopped, []string{"web", "backend"}) {
		t.Fatalf("cleanup order = start %#v, stop %#v", started, stopped)
	}
}

func TestSystemStartCancellationStopsAllServicesAndSystem(t *testing.T) {
	readiness := &blockingReadiness{entered: make(chan struct{})}
	harness := newSystemServiceHarness(t, readiness)
	result, err := harness.service.SubmitStart(context.Background(), orchestrator.StartSingleServiceInput{
		WorkspaceID: harness.workspace.ID, SystemID: harness.workspace.SystemID,
		IdempotencySubject: "test-user", IdempotencyKey: "system-cancel", Request: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-readiness.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("readiness was not reached")
	}
	if _, err := harness.service.CancelOperation(context.Background(), result.Operation.ID); err != nil {
		t.Fatal(err)
	}
	awaitOperation(t, harness.service, result.Operation.ID, domain.OperationCancelled)
	status, err := harness.service.Status(context.Background(), harness.workspace.ID)
	if err != nil || status.System != nil {
		t.Fatalf("cancelled system remained active: (%+v, %v)", status, err)
	}
	_, stopped := harness.driver.serviceOrder()
	if !reflect.DeepEqual(stopped, []string{"backend"}) {
		t.Fatalf("cancel stop order = %#v", stopped)
	}
}

func TestSystemStopContinuesAfterPartialFailure(t *testing.T) {
	harness := newSystemServiceHarness(t, immediateReadiness{})
	start, err := harness.service.SubmitStart(context.Background(), orchestrator.StartSingleServiceInput{
		WorkspaceID: harness.workspace.ID, SystemID: harness.workspace.SystemID,
		IdempotencySubject: "test-user", IdempotencyKey: "partial-stop-start", Request: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	awaitOperation(t, harness.service, start.Operation.ID, domain.OperationSucceeded)
	harness.driver.setStopErrorForService("web", errors.New("web stop failed"))
	stop, err := harness.service.SubmitStop(context.Background(), orchestrator.StopSingleServiceInput{
		WorkspaceID: harness.workspace.ID, SystemID: harness.workspace.SystemID,
		IdempotencySubject: "test-user", IdempotencyKey: "partial-stop", Request: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	operation := awaitOperation(t, harness.service, stop.Operation.ID, domain.OperationFailed)
	if operation.ErrorCode != "PROCESS_STOP_FAILED" {
		t.Fatalf("partial stop error code = %q", operation.ErrorCode)
	}
	_, stopped := harness.driver.serviceOrder()
	if !reflect.DeepEqual(stopped, []string{"web", "backend"}) {
		t.Fatalf("partial stop order = %#v", stopped)
	}
	status, _ := harness.service.Status(context.Background(), harness.workspace.ID)
	states := serviceStates(status)
	if status.System == nil || status.System.State != domain.SystemFailed || states["web"] != domain.ServiceFailed || states["backend"] != domain.ServiceStopped {
		t.Fatalf("partial stop status = system %+v, services %+v", status.System, states)
	}
}

func TestSystemRestartStopsThenFreshStartsInOneOperation(t *testing.T) {
	harness := newSystemServiceHarness(t, immediateReadiness{})
	start, err := harness.service.SubmitStart(context.Background(), orchestrator.StartSingleServiceInput{
		WorkspaceID: harness.workspace.ID, SystemID: harness.workspace.SystemID,
		IdempotencySubject: "test-user", IdempotencyKey: "before-restart", Request: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	awaitOperation(t, harness.service, start.Operation.ID, domain.OperationSucceeded)
	restart, err := harness.service.SubmitRestart(context.Background(), orchestrator.RestartSystemInput{
		WorkspaceID: harness.workspace.ID, SystemID: harness.workspace.SystemID,
		IdempotencySubject: "test-user", IdempotencyKey: "restart", Request: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	operation := awaitOperation(t, harness.service, restart.Operation.ID, domain.OperationSucceeded)
	if operation.Type != domain.OperationRestart {
		t.Fatalf("restart type = %s", operation.Type)
	}
	started, stopped := harness.driver.serviceOrder()
	if !reflect.DeepEqual(started, []string{"backend", "web", "backend", "web"}) || !reflect.DeepEqual(stopped, []string{"web", "backend"}) {
		t.Fatalf("restart order = start %#v, stop %#v", started, stopped)
	}
	status, _ := harness.service.Status(context.Background(), harness.workspace.ID)
	if status.System == nil || status.System.State != domain.SystemRunning {
		t.Fatalf("restarted status = %#v", status)
	}
}

func TestSystemStopAndRestartSettleMissingSupervisor(t *testing.T) {
	for _, action := range []string{"stop", "restart"} {
		t.Run(action, func(t *testing.T) {
			harness := newSystemServiceHarness(t, immediateReadiness{})
			started := submitSystemHarnessStart(t, harness, "missing-supervisor-start-"+action)
			awaitOperation(t, harness.service, started.Operation.ID, domain.OperationSucceeded)
			harness.driver.setStopErrorForService("web", driver.ErrRuntimeNotFound)
			harness.driver.setStopErrorForService("backend", driver.ErrRuntimeNotFound)

			input := orchestrator.StopSingleServiceInput{
				WorkspaceID: harness.workspace.ID, SystemID: harness.workspace.SystemID,
				IdempotencySubject: "test-user", IdempotencyKey: "missing-supervisor-" + action, Request: []byte(`{}`),
			}
			var operationID domain.OperationID
			if action == "stop" {
				result, err := harness.service.SubmitStop(context.Background(), input)
				if err != nil {
					t.Fatal(err)
				}
				operationID = result.Operation.ID
			} else {
				result, err := harness.service.SubmitRestart(context.Background(), orchestrator.RestartSystemInput{
					WorkspaceID: input.WorkspaceID, SystemID: input.SystemID,
					IdempotencySubject: input.IdempotencySubject, IdempotencyKey: input.IdempotencyKey, Request: input.Request,
				})
				if err != nil {
					t.Fatal(err)
				}
				operationID = result.Operation.ID
			}
			awaitOperation(t, harness.service, operationID, domain.OperationSucceeded)
			status, err := harness.service.Status(context.Background(), harness.workspace.ID)
			if err != nil || (action == "stop" && status.System != nil) ||
				(action == "restart" && (status.System == nil || status.System.State != domain.SystemRunning)) {
				t.Fatalf("%s status after missing Supervisor = (%+v, %v)", action, status, err)
			}
		})
	}
}

func TestServiceRestartUsesTargetDownstreamClosure(t *testing.T) {
	tests := []struct {
		target      domain.ServiceID
		wantStarted []string
		wantStopped []string
	}{
		{target: "web", wantStarted: []string{"backend", "web", "web"}, wantStopped: []string{"web"}},
		{target: "backend", wantStarted: []string{"backend", "web", "backend", "web"}, wantStopped: []string{"web", "backend"}},
	}
	for _, test := range tests {
		t.Run(test.target.String(), func(t *testing.T) {
			harness := newSystemServiceHarness(t, immediateReadiness{})
			start, err := harness.service.SubmitStart(context.Background(), orchestrator.StartSingleServiceInput{
				WorkspaceID: harness.workspace.ID, SystemID: harness.workspace.SystemID,
				IdempotencySubject: "test-user", IdempotencyKey: "before-service-restart", Request: []byte(`{}`),
			})
			if err != nil {
				t.Fatal(err)
			}
			awaitOperation(t, harness.service, start.Operation.ID, domain.OperationSucceeded)
			restart, err := harness.service.SubmitServiceRestart(context.Background(), orchestrator.RestartServiceInput{
				WorkspaceID: harness.workspace.ID, SystemID: harness.workspace.SystemID, ServiceID: test.target,
				IdempotencySubject: "test-user", IdempotencyKey: "service-restart", Request: []byte(`{}`),
			})
			if err != nil {
				t.Fatal(err)
			}
			awaitOperation(t, harness.service, restart.Operation.ID, domain.OperationSucceeded)
			started, stopped := harness.driver.serviceOrder()
			if !reflect.DeepEqual(started, test.wantStarted) || !reflect.DeepEqual(stopped, test.wantStopped) {
				t.Fatalf("service restart order = start %#v, stop %#v", started, stopped)
			}
			var bound int
			if err := harness.database.QueryRow(`SELECT COUNT(*) FROM port_leases WHERE state='bound'`).Scan(&bound); err != nil || bound != 2 {
				t.Fatalf("bound leases after service restart = %d, %v", bound, err)
			}
		})
	}
}

func TestServiceRestartRejectsChangedManifest(t *testing.T) {
	harness := newSystemServiceHarness(t, immediateReadiness{})
	start, err := harness.service.SubmitStart(context.Background(), orchestrator.StartSingleServiceInput{
		WorkspaceID: harness.workspace.ID, SystemID: harness.workspace.SystemID,
		IdempotencySubject: "test-user", IdempotencyKey: "before-manifest-change", Request: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	awaitOperation(t, harness.service, start.Operation.ID, domain.OperationSucceeded)
	manifestPath := filepath.Join(harness.workspace.CanonicalPath, ".stackpilot", "system.yaml")
	contents, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	contents = []byte(strings.Replace(string(contents), "name: Fixture", "name: Changed Fixture", 1))
	if err := os.WriteFile(manifestPath, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := harness.manager.Refresh(context.Background(), harness.workspace.ID); err != nil {
		t.Fatal(err)
	}
	_, err = harness.service.SubmitServiceRestart(context.Background(), orchestrator.RestartServiceInput{
		WorkspaceID: harness.workspace.ID, SystemID: harness.workspace.SystemID, ServiceID: "web",
		IdempotencySubject: "test-user", IdempotencyKey: "changed", Request: []byte(`{}`),
	})
	if !errors.Is(err, orchestrator.ErrManifestChanged) {
		t.Fatalf("SubmitServiceRestart() error = %v", err)
	}
}

type systemServiceHarness struct {
	service       *orchestrator.SingleService
	workspace     workspace.Record
	driver        *fakeDriver
	database      *sql.DB
	manager       *workspace.Manager
	healthResults *storage.HealthResultRepository
	liveness      *recordingLiveness
}

func newSystemServiceHarness(t *testing.T, readiness interface {
	Await(context.Context, health.Request) (health.Outcome, error)
}) systemServiceHarness {
	return newSystemServiceHarnessWithWriter(t, readiness, func(root string) {
		backendPort, webPort := availablePort(t), availablePort(t)
		for webPort == backendPort {
			webPort = availablePort(t)
		}
		writeSystemManifest(t, root, backendPort, webPort)
	})
}

func newCompletedDependencyHarness(t *testing.T) systemServiceHarness {
	return newSystemServiceHarnessWithWriter(t, immediateReadiness{}, func(root string) { writeCompletedDependencyManifest(t, root) })
}

func newSystemServiceHarnessWithWriter(t *testing.T, readiness interface {
	Await(context.Context, health.Request) (health.Outcome, error)
}, writeManifest func(string)) systemServiceHarness {
	return newSystemServiceHarnessWithRunner(t, readiness, writeManifest, fakeRunner{})
}

type harnessRunner interface {
	Resolve(context.Context, runner.ResolveRequest) (*runner.ResolvedCommand, error)
}

func newSystemServiceHarnessWithRunner(t *testing.T, readiness interface {
	Await(context.Context, health.Request) (health.Outcome, error)
}, writeManifest func(string), runnerValue harnessRunner) systemServiceHarness {
	return newSystemServiceHarnessWithContext(t, context.Background(), readiness, writeManifest, runnerValue)
}

func newSystemServiceHarnessWithContext(t *testing.T, serviceContext context.Context, readiness interface {
	Await(context.Context, health.Request) (health.Outcome, error)
}, writeManifest func(string), runnerValue harnessRunner) systemServiceHarness {
	t.Helper()
	root := t.TempDir()
	writeManifest(root)
	database, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "stackpilot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	workspaceRepository, _ := storage.NewWorkspaceRepository(database)
	loader, _ := manifest.NewLoader()
	workspaceManager, _ := workspace.NewManager(workspaceRepository, loader, manifest.NewValidatorWithCapabilities("auto-restart", "liveness"))
	record, err := workspaceManager.Register(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	broker := events.NewBroker(16)
	operationRepository, _ := storage.NewOperationRepositoryWithNotifier(database, broker)
	operationManager, _ := orchestrator.NewManager(operationRepository)
	runtimeRepository, _ := storage.NewRuntimeInstanceRepository(database, broker)
	leaseRepository, _ := storage.NewPortLeaseRepository(database)
	planner, _ := ports.NewPlanner(ports.Config{Store: leaseRepository})
	resolvedSpecs, _ := storage.NewResolvedSpecRepository(database)
	secretVersions, _ := storage.NewServiceSecretVersionRepository(database)
	restartAttempts, _ := storage.NewRestartAttemptRepository(database)
	healthResults, _ := storage.NewHealthResultRepository(database)
	incidentRepository, _ := storage.NewIncidentRepository(database)
	incidents, _ := incident.NewCoordinator(incidentRepository, nil)
	revisionRepository, _ := storage.NewRevisionRepository(database)
	collector, err := revision.NewCollector(revision.CollectorConfig{
		Workspaces: workspaceManager, Runtime: runtimeRepository, ResolvedSpecs: resolvedSpecs,
		SecretVersions: secretVersions, Runners: runnerValue,
	})
	if err != nil {
		t.Fatal(err)
	}
	revisions, err := revision.NewService(collector, revisionRepository)
	if err != nil {
		t.Fatal(err)
	}
	changePlanRepository, _ := storage.NewChangePlanRepository(database)
	changePlans, err := changeplan.NewService(changePlanRepository, revisionRepository)
	if err != nil {
		t.Fatal(err)
	}
	driverValue := &fakeDriver{inspectEntered: make(chan struct{}, 1), observation: driver.RuntimeObservation{State: "running"}}
	liveness := &recordingLiveness{results: healthResults, success: true, record: true}
	ownedContext, cancelService := context.WithCancel(serviceContext)
	service, err := orchestrator.NewSingleService(orchestrator.SingleServiceConfig{
		Context: ownedContext, Operations: operationManager, Workspaces: workspaceManager,
		Runner: runnerValue, Driver: driverValue, Runtime: runtimeRepository, Readiness: readiness,
		Liveness: liveness,
		DataDir:  t.TempDir(), PortPlanner: planner, PortLeases: leaseRepository, ResolvedSpecs: resolvedSpecs,
		RestartAttempts: restartAttempts, HealthResults: healthResults, Incidents: incidents,
		SecretVersions: secretVersions, Revisions: revisions, ChangePlans: changePlans,
		VerificationStableWindow: 20 * time.Millisecond, VerificationTimeout: 2 * time.Second,
		VerificationPollInterval: 5 * time.Millisecond,
		StartLogs: func(context.Context, logs.CaptureRequest) (orchestrator.CaptureSession, error) {
			return fakeCapture{}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancelService()
		service.Wait()
	})
	return systemServiceHarness{
		service: service, workspace: *record, driver: driverValue, database: database,
		manager: workspaceManager, healthResults: healthResults, liveness: liveness,
	}
}

type recordingLiveness struct {
	results *storage.HealthResultRepository
	mutex   sync.Mutex
	success bool
	record  bool
}

func (monitor *recordingLiveness) MonitorLiveness(ctx context.Context, request health.LivenessRequest, _ health.LivenessHandler) error {
	monitor.mutex.Lock()
	record, success := monitor.record, monitor.success
	monitor.mutex.Unlock()
	if record {
		result := health.Result{
			Purpose: health.PurposeLiveness, Kind: request.Spec.Kind, CheckedAt: time.Now().UTC(),
			Duration: time.Millisecond, Success: success,
		}
		if !success {
			result.ErrorCode = health.CodeHTTPStatusMismatch
			result.Summary = "fixture liveness failure"
		}
		if err := monitor.results.Record(ctx, request.ServiceInstanceID, result); err != nil {
			return err
		}
	}
	<-ctx.Done()
	return ctx.Err()
}

func (monitor *recordingLiveness) configure(record, success bool) {
	monitor.mutex.Lock()
	monitor.record, monitor.success = record, success
	monitor.mutex.Unlock()
}

func writeCompletedDependencyManifest(t *testing.T, root string) {
	t.Helper()
	for _, directory := range []string{"setup", "app", ".stackpilot"} {
		if err := os.Mkdir(filepath.Join(root, directory), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	contents := `apiVersion: stackpilot.io/v1alpha1
kind: System
metadata: {id: fixture, name: Fixture}
spec:
  services:
    setup:
      driver: process
      mode: oneshot
      runner: java
      workingDirectory: setup
      arguments: ["-version"]
      stop: {gracefulTimeout: 1s}
    app:
      driver: process
      mode: daemon
      runner: java
      workingDirectory: app
      arguments: ["-version"]
      dependsOn: {setup: completed}
      stop: {gracefulTimeout: 1s}
      readiness: {type: process, timeout: 3s, interval: 100ms}
`
	if err := os.WriteFile(filepath.Join(root, ".stackpilot", "system.yaml"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeSystemManifest(t *testing.T, root string, backendPort, webPort int) {
	t.Helper()
	for _, directory := range []string{"backend", "web", ".stackpilot"} {
		if err := os.Mkdir(filepath.Join(root, directory), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	contents := fmt.Sprintf(`apiVersion: stackpilot.io/v1alpha1
kind: System
metadata:
  id: fixture
  name: Fixture
spec:
  ports:
    backend: {protocol: tcp, preferred: %d, conflictPolicy: strict, exposure: loopback}
    web: {protocol: tcp, preferred: %d, conflictPolicy: strict, exposure: loopback}
  services:
    backend:
      driver: process
      mode: daemon
      runner: java
      workingDirectory: backend
      arguments: ["--port", "${ports.backend}"]
      stop: {gracefulTimeout: 1s}
      readiness: {type: tcp, host: 127.0.0.1, port: "${ports.backend}", timeout: 3s, interval: 100ms}
      liveness: {type: tcp, host: 127.0.0.1, port: "${ports.backend}", timeout: 3s, interval: 100ms}
    web:
      driver: process
      mode: daemon
      runner: java
      workingDirectory: web
      arguments: ["--port", "${ports.web}"]
      environment: {API_URL: "http://127.0.0.1:${ports.backend}"}
      dependsOn: {backend: ready}
      stop: {gracefulTimeout: 1s}
      readiness: {type: tcp, host: 127.0.0.1, port: "${ports.web}", timeout: 3s, interval: 100ms}
      liveness: {type: tcp, host: 127.0.0.1, port: "${ports.web}", timeout: 3s, interval: 100ms}
`, backendPort, webPort)
	if err := os.WriteFile(filepath.Join(root, ".stackpilot", "system.yaml"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func serviceStates(status orchestrator.RuntimeStatus) map[string]domain.ServiceState {
	result := make(map[string]domain.ServiceState, len(status.Services))
	for _, service := range status.Services {
		result[service.ServiceID.String()] = service.State
	}
	return result
}

type sequenceReadiness struct {
	mutex    sync.Mutex
	outcomes []bool
	index    int
}

func (readiness *sequenceReadiness) Await(context.Context, health.Request) (health.Outcome, error) {
	readiness.mutex.Lock()
	defer readiness.mutex.Unlock()
	ready := readiness.outcomes[readiness.index]
	readiness.index++
	if ready {
		return health.Outcome{Ready: true, Attempts: 1}, nil
	}
	return health.Outcome{Ready: false, Attempts: 1, ErrorCode: health.CodeReadinessTimeout}, nil
}
