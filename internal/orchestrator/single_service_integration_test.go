package orchestrator_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"stackpilot/internal/domain"
	"stackpilot/internal/driver"
	"stackpilot/internal/events"
	"stackpilot/internal/health"
	"stackpilot/internal/logs"
	"stackpilot/internal/manifest"
	"stackpilot/internal/orchestrator"
	"stackpilot/internal/runner"
	"stackpilot/internal/storage"
	"stackpilot/internal/workspace"
)

func TestSingleServiceStartStopVerticalSlice(t *testing.T) {
	harness := newSingleServiceHarness(t, immediateReadiness{})
	start, err := harness.service.SubmitStart(context.Background(), orchestrator.StartSingleServiceInput{
		WorkspaceID: harness.workspace.ID, SystemID: harness.workspace.SystemID,
		IdempotencySubject: "test-user", IdempotencyKey: "start-1", Request: []byte(`{"workspaceId":"test"}`),
	})
	if err != nil {
		t.Fatalf("SubmitStart() error = %v", err)
	}
	started := awaitOperation(t, harness.service, start.Operation.ID, domain.OperationSucceeded)
	if len(started.Steps) != 6 || started.ErrorCode != "" {
		t.Fatalf("started Operation = %+v", started)
	}
	status, err := harness.service.Status(context.Background(), harness.workspace.ID)
	if err != nil || status.System == nil || status.System.State != domain.SystemRunning || status.Services[0].State != domain.ServiceReady {
		t.Fatalf("running status = (%+v, %v)", status, err)
	}
	stop, err := harness.service.SubmitStop(context.Background(), orchestrator.StopSingleServiceInput{
		WorkspaceID: harness.workspace.ID, SystemID: harness.workspace.SystemID,
		IdempotencySubject: "test-user", IdempotencyKey: "stop-1", Request: []byte(`{"workspaceId":"test"}`),
	})
	if err != nil {
		t.Fatalf("SubmitStop() error = %v", err)
	}
	awaitOperation(t, harness.service, stop.Operation.ID, domain.OperationSucceeded)
	status, err = harness.service.Status(context.Background(), harness.workspace.ID)
	if err != nil || status.System != nil {
		t.Fatalf("stopped status = (%+v, %v)", status, err)
	}
	if starts, stops := harness.driver.counts(); starts != 1 || stops != 1 {
		t.Fatalf("driver calls = start %d, stop %d", starts, stops)
	}
	if timeout := harness.driver.lastStopTimeout(); timeout != 2*time.Second {
		t.Fatalf("persisted stop timeout = %s, want 2s", timeout)
	}
}

func TestSingleServiceCancellationStopsCreatedRuntime(t *testing.T) {
	readiness := &blockingReadiness{entered: make(chan struct{})}
	harness := newSingleServiceHarness(t, readiness)
	result, err := harness.service.SubmitStart(context.Background(), orchestrator.StartSingleServiceInput{
		WorkspaceID: harness.workspace.ID, SystemID: harness.workspace.SystemID,
		IdempotencySubject: "test-user", IdempotencyKey: "cancel-1", Request: []byte(`{"workspaceId":"test"}`),
	})
	if err != nil {
		t.Fatalf("SubmitStart() error = %v", err)
	}
	select {
	case <-readiness.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("readiness was not reached")
	}
	if _, err := harness.service.CancelOperation(context.Background(), result.Operation.ID); err != nil {
		t.Fatalf("CancelOperation() error = %v", err)
	}
	awaitOperation(t, harness.service, result.Operation.ID, domain.OperationCancelled)
	status, _ := harness.service.Status(context.Background(), harness.workspace.ID)
	if status.System != nil {
		t.Fatalf("cancelled runtime remained active: %+v", status)
	}
	if _, stops := harness.driver.counts(); stops != 1 {
		t.Fatalf("driver stop calls = %d, want 1", stops)
	}
}

func TestSingleServiceStopFailureUsesStopCodeAndSkipsRemainingSteps(t *testing.T) {
	harness := newSingleServiceHarness(t, immediateReadiness{})
	start, err := harness.service.SubmitStart(context.Background(), orchestrator.StartSingleServiceInput{
		WorkspaceID: harness.workspace.ID, SystemID: harness.workspace.SystemID,
		IdempotencySubject: "test-user", IdempotencyKey: "stop-failure-start", Request: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	awaitOperation(t, harness.service, start.Operation.ID, domain.OperationSucceeded)
	harness.driver.setStopError(errors.New("fixture stop failed"))
	stop, err := harness.service.SubmitStop(context.Background(), orchestrator.StopSingleServiceInput{
		WorkspaceID: harness.workspace.ID, SystemID: harness.workspace.SystemID,
		IdempotencySubject: "test-user", IdempotencyKey: "stop-failure", Request: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	failed := awaitOperation(t, harness.service, stop.Operation.ID, domain.OperationFailed)
	if failed.ErrorCode != "PROCESS_STOP_FAILED" {
		t.Fatalf("stop failure code = %q", failed.ErrorCode)
	}
	if failed.Steps[1].State != domain.OperationStepFailed || failed.Steps[2].State != domain.OperationStepSkipped {
		t.Fatalf("stop failure steps = %+v", failed.Steps)
	}
}

func TestSingleServiceReadinessFailureRetainsSceneByDefault(t *testing.T) {
	harness := newSingleServiceHarness(t, failedReadiness{})
	result, err := harness.service.SubmitStart(context.Background(), orchestrator.StartSingleServiceInput{
		WorkspaceID: harness.workspace.ID, SystemID: harness.workspace.SystemID,
		IdempotencySubject: "test-user", IdempotencyKey: "failure-1", Request: []byte(`{"workspaceId":"test"}`),
	})
	if err != nil {
		t.Fatalf("SubmitStart() error = %v", err)
	}
	failed := awaitOperation(t, harness.service, result.Operation.ID, domain.OperationFailed)
	if failed.ErrorCode != string(health.CodeReadinessTimeout) {
		t.Fatalf("failure code = %q", failed.ErrorCode)
	}
	status, err := harness.service.Status(context.Background(), harness.workspace.ID)
	if err != nil || status.System == nil || status.System.State != domain.SystemFailed || status.Services[0].State != domain.ServiceFailed {
		t.Fatalf("retained failure status = (%+v, %v)", status, err)
	}
	if _, stops := harness.driver.counts(); stops != 0 {
		t.Fatalf("failure scene stop calls = %d", stops)
	}
}

func TestSingleServiceCancellationFailureEndsFailed(t *testing.T) {
	readiness := &blockingReadiness{entered: make(chan struct{})}
	harness := newSingleServiceHarness(t, readiness)
	harness.driver.setStopError(driver.ErrIdentityMismatch)
	result, err := harness.service.SubmitStart(context.Background(), orchestrator.StartSingleServiceInput{
		WorkspaceID: harness.workspace.ID, SystemID: harness.workspace.SystemID,
		IdempotencySubject: "test-user", IdempotencyKey: "cancel-failure", Request: []byte(`{"workspaceId":"test"}`),
	})
	if err != nil {
		t.Fatalf("SubmitStart() error = %v", err)
	}
	select {
	case <-readiness.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("readiness was not reached")
	}
	if _, err := harness.service.CancelOperation(context.Background(), result.Operation.ID); err != nil {
		t.Fatal(err)
	}
	failed := awaitOperation(t, harness.service, result.Operation.ID, domain.OperationFailed)
	if failed.ErrorCode != "PROCESS_IDENTITY_MISMATCH" {
		t.Fatalf("cancellation failure code = %q", failed.ErrorCode)
	}
	status, _ := harness.service.Status(context.Background(), harness.workspace.ID)
	if status.System == nil || status.System.State != domain.SystemFailed || status.Services[0].State != domain.ServiceFailed {
		t.Fatalf("failed cancellation status = %+v", status)
	}
}

func TestSingleServiceFailurePolicyCleansFailedRuntime(t *testing.T) {
	harness := newSingleServiceHarness(t, failedReadiness{})
	cleanup := true
	result, err := harness.service.SubmitStart(context.Background(), orchestrator.StartSingleServiceInput{
		WorkspaceID: harness.workspace.ID, SystemID: harness.workspace.SystemID,
		IdempotencySubject: "test-user", IdempotencyKey: "cleanup-failure", Request: []byte(`{"cleanupOnFailure":true}`),
		FailurePolicy: orchestrator.FailurePolicyOverride{CleanupOnFailure: &cleanup},
	})
	if err != nil {
		t.Fatal(err)
	}
	failed := awaitOperation(t, harness.service, result.Operation.ID, domain.OperationFailed)
	if failed.ErrorCode != string(health.CodeReadinessTimeout) {
		t.Fatalf("cleanup failure code = %q", failed.ErrorCode)
	}
	status, _ := harness.service.Status(context.Background(), harness.workspace.ID)
	if status.System != nil {
		t.Fatalf("cleanup left an active runtime: %+v", status)
	}
	if _, stops := harness.driver.counts(); stops != 1 {
		t.Fatalf("cleanup stop calls = %d", stops)
	}
}

func TestSingleServiceOneshotCompletesAndStopsWithoutSecondDriverCall(t *testing.T) {
	harness := newOneshotHarness(t, "2s")
	harness.driver.setObservation("exited", uint32PointerForTest(0))
	result := submitHarnessStart(t, harness, "oneshot-success")
	operation := awaitOperation(t, harness.service, result.Operation.ID, domain.OperationSucceeded)
	if operation.Steps[4].Key != "wait-complete:app" {
		t.Fatalf("oneshot wait step = %q", operation.Steps[4].Key)
	}
	status, err := harness.service.Status(context.Background(), harness.workspace.ID)
	if err != nil || status.System == nil || status.System.State != domain.SystemRunning || len(status.Services) != 1 {
		t.Fatalf("completed status = (%+v, %v)", status, err)
	}
	runtime := status.Services[0]
	if runtime.State != domain.ServiceCompleted || runtime.ExitCode == nil || *runtime.ExitCode != 0 || runtime.Identity != nil {
		t.Fatalf("completed runtime = %+v", runtime)
	}
	if _, stops := harness.driver.counts(); stops != 1 || harness.driver.lastStopTimeout() != 0 {
		t.Fatalf("oneshot reap calls = %d, timeout = %s", stops, harness.driver.lastStopTimeout())
	}
	stop, err := harness.service.SubmitStop(context.Background(), orchestrator.StopSingleServiceInput{
		WorkspaceID: harness.workspace.ID, SystemID: harness.workspace.SystemID,
		IdempotencySubject: "test-user", IdempotencyKey: "oneshot-stop", Request: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	awaitOperation(t, harness.service, stop.Operation.ID, domain.OperationSucceeded)
	if _, stops := harness.driver.counts(); stops != 1 {
		t.Fatalf("stopping Completed invoked Driver.Stop again: %d", stops)
	}
}

func TestSingleServiceOneshotNonzeroExitFailsWithExitCode(t *testing.T) {
	harness := newOneshotHarness(t, "2s")
	harness.driver.setObservation("exited", uint32PointerForTest(23))
	result := submitHarnessStart(t, harness, "oneshot-nonzero")
	operation := awaitOperation(t, harness.service, result.Operation.ID, domain.OperationFailed)
	if operation.ErrorCode != "PROCESS_EXITED" {
		t.Fatalf("oneshot failure code = %q", operation.ErrorCode)
	}
	status, _ := harness.service.Status(context.Background(), harness.workspace.ID)
	runtime := status.Services[0]
	if runtime.State != domain.ServiceFailed || runtime.ExitCode == nil || *runtime.ExitCode != 23 || runtime.Identity != nil {
		t.Fatalf("failed oneshot runtime = %+v", runtime)
	}
	if _, stops := harness.driver.counts(); stops != 1 {
		t.Fatalf("nonzero oneshot reap calls = %d", stops)
	}
}

func TestSingleServiceOneshotTimeoutForcesTreeAndPersistsExitCode(t *testing.T) {
	harness := newOneshotHarness(t, "1s")
	harness.driver.setObservation("running", nil)
	result := submitHarnessStart(t, harness, "oneshot-timeout")
	operation := awaitOperation(t, harness.service, result.Operation.ID, domain.OperationFailed)
	if operation.ErrorCode != "HEALTH_READINESS_TIMEOUT" {
		t.Fatalf("oneshot timeout code = %q", operation.ErrorCode)
	}
	status, _ := harness.service.Status(context.Background(), harness.workspace.ID)
	runtime := status.Services[0]
	if runtime.State != domain.ServiceFailed || runtime.ExitCode == nil || *runtime.ExitCode != 137 || runtime.Identity != nil {
		t.Fatalf("timed-out oneshot runtime = %+v", runtime)
	}
	if _, stops := harness.driver.counts(); stops != 1 || harness.driver.lastStopTimeout() != 0 {
		t.Fatalf("timeout reap calls = %d, timeout = %s", stops, harness.driver.lastStopTimeout())
	}
}

func TestSingleServiceOneshotCancellationStopsRuntime(t *testing.T) {
	harness := newOneshotHarness(t, "10s")
	harness.driver.setObservation("running", nil)
	result := submitHarnessStart(t, harness, "oneshot-cancel")
	select {
	case <-harness.driver.inspectEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("oneshot completion wait was not reached")
	}
	if _, err := harness.service.CancelOperation(context.Background(), result.Operation.ID); err != nil {
		t.Fatal(err)
	}
	awaitOperation(t, harness.service, result.Operation.ID, domain.OperationCancelled)
	status, _ := harness.service.Status(context.Background(), harness.workspace.ID)
	if status.System != nil {
		t.Fatalf("cancelled oneshot remained active: %+v", status)
	}
	if _, stops := harness.driver.counts(); stops != 1 {
		t.Fatalf("cancelled oneshot stop calls = %d", stops)
	}
}

func submitHarnessStart(t *testing.T, harness singleServiceHarness, key string) *orchestrator.CreateResult {
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

type singleServiceHarness struct {
	service   *orchestrator.SingleService
	workspace workspace.Record
	driver    *fakeDriver
}

func newSingleServiceHarness(t *testing.T, readiness interface {
	Await(context.Context, health.Request) (health.Outcome, error)
}) singleServiceHarness {
	return newSingleServiceHarnessWithWriter(t, readiness, func(root string) { writeSingleServiceManifest(t, root, availablePort(t)) })
}

func newOneshotHarness(t *testing.T, startTimeout string) singleServiceHarness {
	return newSingleServiceHarnessWithWriter(t, immediateReadiness{}, func(root string) { writeOneshotManifest(t, root, startTimeout) })
}

func newSingleServiceHarnessWithWriter(t *testing.T, readiness interface {
	Await(context.Context, health.Request) (health.Outcome, error)
}, writeManifest func(string)) singleServiceHarness {
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
	workspaceManager, _ := workspace.NewManager(workspaceRepository, loader, manifest.NewValidator())
	record, err := workspaceManager.Register(context.Background(), root)
	if err != nil {
		t.Fatalf("register test workspace: %v", err)
	}
	broker := events.NewBroker(16)
	operationRepository, _ := storage.NewOperationRepositoryWithNotifier(database, broker)
	operationManager, _ := orchestrator.NewManager(operationRepository)
	runtimeRepository, _ := storage.NewRuntimeInstanceRepository(database, broker)
	driverValue := &fakeDriver{inspectEntered: make(chan struct{}, 1), observation: driver.RuntimeObservation{State: "running"}}
	service, err := orchestrator.NewSingleService(orchestrator.SingleServiceConfig{
		Context: context.Background(), Operations: operationManager, Workspaces: workspaceManager,
		Runner: fakeRunner{}, Driver: driverValue, Runtime: runtimeRepository, Readiness: readiness,
		DataDir: filepath.Dir(filepath.Join(t.TempDir(), "unused")),
		StartLogs: func(context.Context, logs.CaptureRequest) (orchestrator.CaptureSession, error) {
			return fakeCapture{}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.Wait)
	return singleServiceHarness{service: service, workspace: *record, driver: driverValue}
}

type fakeRunner struct{}

func (fakeRunner) Resolve(context.Context, runner.ResolveRequest) (*runner.ResolvedCommand, error) {
	return &runner.ResolvedCommand{
		Executable: `C:\fixture\runner.exe`, Version: "1.0.0", ResolutionKind: runner.ResolutionPath,
		ExecutableDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}, nil
}

type fakeDriver struct {
	mutex          sync.Mutex
	starts         int
	stops          int
	stopTimeouts   []time.Duration
	startedIDs     []string
	stoppedIDs     []string
	stopErr        error
	stopErrors     map[string]error
	observation    driver.RuntimeObservation
	inspectEntered chan struct{}
}

func (*fakeDriver) Preflight(context.Context, driver.ResolvedServiceSpec) error { return nil }

func (value *fakeDriver) Start(_ context.Context, request driver.StartRequest) (driver.RuntimeIdentity, error) {
	value.mutex.Lock()
	value.starts++
	value.startedIDs = append(value.startedIDs, request.Spec.ServiceID.String())
	value.mutex.Unlock()
	return driver.RuntimeIdentity{
		PID: 1234, StartedAt: time.Now().UTC(), ExecutablePath: request.Spec.Command.Executable,
		CommandDigest: request.Spec.Command.ExecutableDigest, PlatformToken: request.Spec.ServiceID.String(),
	}, nil
}

func (value *fakeDriver) Stop(_ context.Context, request driver.StopRequest) error {
	value.mutex.Lock()
	value.stops++
	value.stoppedIDs = append(value.stoppedIDs, request.Identity.PlatformToken)
	value.stopTimeouts = append(value.stopTimeouts, request.GracefulTimeout)
	err := value.stopErr
	if serviceErr := value.stopErrors[request.Identity.PlatformToken]; serviceErr != nil {
		err = serviceErr
	}
	value.mutex.Unlock()
	return err
}

func (value *fakeDriver) setStopErrorForService(serviceID string, err error) {
	value.mutex.Lock()
	defer value.mutex.Unlock()
	if value.stopErrors == nil {
		value.stopErrors = make(map[string]error)
	}
	value.stopErrors[serviceID] = err
}

func (value *fakeDriver) setStopError(err error) {
	value.mutex.Lock()
	defer value.mutex.Unlock()
	value.stopErr = err
}

func (value *fakeDriver) lastStopTimeout() time.Duration {
	value.mutex.Lock()
	defer value.mutex.Unlock()
	if len(value.stopTimeouts) == 0 {
		return 0
	}
	return value.stopTimeouts[len(value.stopTimeouts)-1]
}

func (value *fakeDriver) Inspect(context.Context, driver.RuntimeIdentity) (driver.RuntimeObservation, error) {
	select {
	case value.inspectEntered <- struct{}{}:
	default:
	}
	value.mutex.Lock()
	defer value.mutex.Unlock()
	return value.observation, nil
}

func (value *fakeDriver) setObservation(state string, exitCode *uint32) {
	value.mutex.Lock()
	defer value.mutex.Unlock()
	value.observation = driver.RuntimeObservation{State: state, ExitCode: exitCode}
}

func (value *fakeDriver) Recover(_ context.Context, identity driver.RuntimeIdentity) (driver.RecoveredRuntime, error) {
	value.mutex.Lock()
	defer value.mutex.Unlock()
	observation := value.observation
	observation.Identity = identity
	return driver.RecoveredRuntime{Identity: identity, Observation: observation}, nil
}

func (value *fakeDriver) counts() (int, int) {
	value.mutex.Lock()
	defer value.mutex.Unlock()
	return value.starts, value.stops
}

func (value *fakeDriver) serviceOrder() ([]string, []string) {
	value.mutex.Lock()
	defer value.mutex.Unlock()
	return append([]string(nil), value.startedIDs...), append([]string(nil), value.stoppedIDs...)
}

type immediateReadiness struct{}

func (immediateReadiness) Await(context.Context, health.Request) (health.Outcome, error) {
	return health.Outcome{Ready: true, Attempts: 1}, nil
}

type failedReadiness struct{}

func (failedReadiness) Await(context.Context, health.Request) (health.Outcome, error) {
	return health.Outcome{Ready: false, Attempts: 3, ErrorCode: health.CodeReadinessTimeout}, nil
}

type blockingReadiness struct{ entered chan struct{} }

func (value *blockingReadiness) Await(ctx context.Context, _ health.Request) (health.Outcome, error) {
	close(value.entered)
	<-ctx.Done()
	return health.Outcome{}, ctx.Err()
}

type fakeCapture struct{}

func (fakeCapture) Close() error { return nil }

func awaitOperation(t *testing.T, service *orchestrator.SingleService, id domain.OperationID, target domain.OperationState) *orchestrator.Operation {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		operation, err := service.GetOperation(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if operation.State == target {
			return operation
		}
		if operation.State.Terminal() {
			t.Fatalf("Operation reached %s instead of %s: %+v", operation.State, target, operation)
		}
		time.Sleep(10 * time.Millisecond)
	}
	operation, _ := service.GetOperation(context.Background(), id)
	t.Fatalf("Operation did not reach %s: %+v", target, operation)
	return nil
}

func writeSingleServiceManifest(t *testing.T, root string, port int) {
	t.Helper()
	directory := filepath.Join(root, ".stackpilot")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	contents := fmt.Sprintf(`apiVersion: stackpilot.io/v1alpha1
kind: System
metadata:
  id: fixture
  name: Fixture
spec:
  ports:
    app:
      protocol: tcp
      preferred: %d
      exposure: loopback
  services:
    app:
      driver: process
      mode: daemon
      runner: java
      workingDirectory: .
      arguments: ["-version"]
      environment:
        APP_PORT: "${ports.app}"
      stop:
        gracefulTimeout: 2s
      readiness:
        type: tcp
        host: 127.0.0.1
        port: "${ports.app}"
        timeout: 3s
        interval: 100ms
`, port)
	if err := os.WriteFile(filepath.Join(directory, "system.yaml"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeOneshotManifest(t *testing.T, root, startTimeout string) {
	t.Helper()
	directory := filepath.Join(root, ".stackpilot")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	contents := fmt.Sprintf(`apiVersion: stackpilot.io/v1alpha1
kind: System
metadata:
  id: fixture
  name: Fixture
spec:
  policies:
    startTimeout: %s
  services:
    app:
      driver: process
      mode: oneshot
      runner: java
      workingDirectory: .
      arguments: ["-version"]
      stop:
        gracefulTimeout: 2s
`, startTimeout)
	if err := os.WriteFile(filepath.Join(directory, "system.yaml"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func uint32PointerForTest(value uint32) *uint32 { return &value }

func availablePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}
