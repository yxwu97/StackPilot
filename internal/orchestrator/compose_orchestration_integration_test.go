package orchestrator_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"stackpilot/internal/domain"
	"stackpilot/internal/driver"
	"stackpilot/internal/driver/compose"
	"stackpilot/internal/events"
	"stackpilot/internal/logs"
	"stackpilot/internal/manifest"
	"stackpilot/internal/orchestrator"
	"stackpilot/internal/ports"
	"stackpilot/internal/storage"
	"stackpilot/internal/workspace"
)

func TestComposeOrchestrationPersistsIdentityAndReconcilesAfterServerRestart(t *testing.T) {
	harness := newComposeHarness(t)
	firstContext, cancelFirst := context.WithCancel(context.Background())
	first, resumed := harness.newService(t, firstContext)
	result, err := first.SubmitStart(context.Background(), orchestrator.StartSingleServiceInput{WorkspaceID: harness.workspace.ID, SystemID: harness.workspace.SystemID, IdempotencySubject: "test", IdempotencyKey: "compose-start", Request: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	awaitOperation(t, first, result.Operation.ID, domain.OperationSucceeded)
	status, err := first.Status(context.Background(), harness.workspace.ID)
	if err != nil || len(status.Services) != 1 || status.Services[0].Driver != domain.DriverCompose || status.Services[0].ComposeIdentity == "" || status.Services[0].Identity != nil {
		t.Fatalf("started Compose status = (%+v, %v)", status, err)
	}
	instanceID := status.System.ID
	cancelFirst()
	first.Wait()

	secondContext, cancelSecond := context.WithCancel(context.Background())
	defer cancelSecond()
	second, resumedAfterRestart := harness.newService(t, secondContext)
	if err := second.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, err = second.Status(context.Background(), harness.workspace.ID)
	if err != nil || status.System.ID != instanceID || status.Services[0].State != domain.ServiceReady || *resumedAfterRestart != 1 {
		t.Fatalf("reconciled Compose status = (%+v, %v), resumes=%d", status, err, *resumedAfterRestart)
	}
	if *resumed != 0 {
		t.Fatalf("initial service unexpectedly resumed logs: %d", *resumed)
	}
	stop, err := second.SubmitStop(context.Background(), orchestrator.StopSingleServiceInput{WorkspaceID: harness.workspace.ID, SystemID: harness.workspace.SystemID, IdempotencySubject: "test", IdempotencyKey: "compose-stop", Request: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	awaitOperation(t, second, stop.Operation.ID, domain.OperationSucceeded)
	if status, err := second.Status(context.Background(), harness.workspace.ID); err != nil || status.System != nil {
		t.Fatalf("stopped Compose status = (%+v, %v)", status, err)
	}
	harness.assertLifecycle(t)
}

func TestComposeReconciliationKeepsFailedRuntimeWithoutIdentity(t *testing.T) {
	harness := newComposeHarness(t)
	firstContext, cancelFirst := context.WithCancel(context.Background())
	first, _ := harness.newService(t, firstContext)
	result, err := first.SubmitStart(context.Background(), orchestrator.StartSingleServiceInput{
		WorkspaceID: harness.workspace.ID, SystemID: harness.workspace.SystemID,
		IdempotencySubject: "test", IdempotencyKey: "compose-failed-reconcile", Request: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	awaitOperation(t, first, result.Operation.ID, domain.OperationSucceeded)
	status, err := first.Status(context.Background(), harness.workspace.ID)
	if err != nil || status.System == nil || len(status.Services) != 1 {
		t.Fatalf("started Compose status = (%+v, %v)", status, err)
	}
	cancelFirst()
	first.Wait()
	if _, err := harness.database.Exec(`UPDATE service_instances SET state='failed', compose_project_token=NULL,
        state_version=state_version+1 WHERE id=?`, status.Services[0].ID.String()); err != nil {
		t.Fatalf("seed failed Compose runtime without identity: %v", err)
	}
	if _, err := harness.database.Exec(`UPDATE system_instances SET state='failed', last_reconciled_at=NULL WHERE id=?`, status.System.ID.String()); err != nil {
		t.Fatalf("seed failed Compose system: %v", err)
	}

	secondContext, cancelSecond := context.WithCancel(context.Background())
	defer cancelSecond()
	second, resumed := harness.newService(t, secondContext)
	if err := second.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	status, err = second.Status(context.Background(), harness.workspace.ID)
	if err != nil || status.System == nil || status.System.LastReconciledAt == nil ||
		status.Services[0].State != domain.ServiceFailed || status.Services[0].ComposeIdentity != "" || *resumed != 0 {
		t.Fatalf("reconciled failed Compose status = (%+v, %v), resumes=%d", status, err, *resumed)
	}
	stop, err := second.SubmitStop(context.Background(), orchestrator.StopSingleServiceInput{
		WorkspaceID: harness.workspace.ID, SystemID: harness.workspace.SystemID,
		IdempotencySubject: "test", IdempotencyKey: "compose-discovered-stop", Request: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	awaitOperation(t, second, stop.Operation.ID, domain.OperationSucceeded)
	if status, err := second.Status(context.Background(), harness.workspace.ID); err != nil || status.System != nil {
		t.Fatalf("discovered Compose stop status = (%+v, %v)", status, err)
	}
	cancelSecond()
	second.Wait()
}

func TestComposeStopSettlesMissingProjectWithoutIdentity(t *testing.T) {
	harness := newComposeHarness(t)
	serviceContext, cancel := context.WithCancel(context.Background())
	defer cancel()
	service, _ := harness.newService(t, serviceContext)
	result, err := service.SubmitStart(context.Background(), orchestrator.StartSingleServiceInput{
		WorkspaceID: harness.workspace.ID, SystemID: harness.workspace.SystemID,
		IdempotencySubject: "test", IdempotencyKey: "compose-missing-start", Request: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	awaitOperation(t, service, result.Operation.ID, domain.OperationSucceeded)
	status, err := service.Status(context.Background(), harness.workspace.ID)
	if err != nil || status.System == nil || len(status.Services) != 1 {
		t.Fatalf("started Compose status = (%+v, %v)", status, err)
	}
	if _, err := harness.database.Exec(`UPDATE service_instances SET state='failed', compose_project_token=NULL,
        state_version=state_version+1 WHERE id=?`, status.Services[0].ID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := harness.database.Exec(`UPDATE system_instances SET state='failed' WHERE id=?`, status.System.ID.String()); err != nil {
		t.Fatal(err)
	}
	*harness.discoverMissing = true
	stop, err := service.SubmitStop(context.Background(), orchestrator.StopSingleServiceInput{
		WorkspaceID: harness.workspace.ID, SystemID: harness.workspace.SystemID,
		IdempotencySubject: "test", IdempotencyKey: "compose-missing-stop", Request: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	awaitOperation(t, service, stop.Operation.ID, domain.OperationSucceeded)
	if status, err := service.Status(context.Background(), harness.workspace.ID); err != nil || status.System != nil {
		t.Fatalf("missing Compose project stop status = (%+v, %v)", status, err)
	}
}

func TestComposeBuildPolicyDistinguishesSystemAndServiceRestart(t *testing.T) {
	harness := newComposeHarnessWithBuild(t, true)
	serviceContext, cancel := context.WithCancel(context.Background())
	service, _ := harness.newService(t, serviceContext)
	defer func() { cancel(); service.Wait() }()
	start, err := service.SubmitStart(context.Background(), orchestrator.StartSingleServiceInput{
		WorkspaceID: harness.workspace.ID, SystemID: harness.workspace.SystemID,
		IdempotencySubject: "test", IdempotencyKey: "compose-build-start", Request: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	awaitOperation(t, service, start.Operation.ID, domain.OperationSucceeded)
	assertComposeCommandCounts(t, harness, 1, 1)

	serviceRestart, err := service.SubmitServiceRestart(context.Background(), orchestrator.RestartServiceInput{
		WorkspaceID: harness.workspace.ID, SystemID: harness.workspace.SystemID, ServiceID: "infrastructure",
		IdempotencySubject: "test", IdempotencyKey: "compose-service-restart", Request: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	awaitOperation(t, service, serviceRestart.Operation.ID, domain.OperationSucceeded)
	assertComposeCommandCounts(t, harness, 1, 2)

	systemRestart, err := service.SubmitRestart(context.Background(), orchestrator.RestartSystemInput{
		WorkspaceID: harness.workspace.ID, SystemID: harness.workspace.SystemID,
		IdempotencySubject: "test", IdempotencyKey: "compose-system-restart", Request: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	awaitOperation(t, service, systemRestart.Operation.ID, domain.OperationSucceeded)
	assertComposeCommandCounts(t, harness, 2, 3)
}

func TestComposeBuildFailureFailsOperationBeforeUp(t *testing.T) {
	harness := newComposeHarnessWithBuild(t, true)
	*harness.failBuild = true
	serviceContext, cancel := context.WithCancel(context.Background())
	service, _ := harness.newService(t, serviceContext)
	defer func() { cancel(); service.Wait() }()
	start, err := service.SubmitStart(context.Background(), orchestrator.StartSingleServiceInput{
		WorkspaceID: harness.workspace.ID, SystemID: harness.workspace.SystemID,
		IdempotencySubject: "test", IdempotencyKey: "compose-build-failure", Request: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	awaitOperation(t, service, start.Operation.ID, domain.OperationFailed)
	operation, err := service.GetOperation(context.Background(), start.Operation.ID)
	if err != nil || operation.ErrorCode != "COMPOSE_BUILD_FAILED" {
		t.Fatalf("failed build operation = (%#v, %v)", operation, err)
	}
	assertComposeCommandCounts(t, harness, 1, 0)
}

func TestInstalledComposeOrchestrationSurvivesControlPlaneRestart(t *testing.T) {
	if os.Getenv("STACKPILOT_COMPOSE_ORCHESTRATION_INTEGRATION") != "1" {
		t.Skip("set STACKPILOT_COMPOSE_ORCHESTRATION_INTEGRATION=1 for the real control-plane restart Gate")
	}
	harness := newComposeHarness(t)
	docker, err := exec.LookPath("docker.exe")
	if err != nil {
		t.Fatal(err)
	}
	image := "stackpilot-p2b06-orchestrator:" + strconv.Itoa(os.Getpid())
	buildComposeOrchestrationImage(t, harness.workspace.CanonicalPath, image, docker)
	lifecycle, err := compose.NewLifecycle(compose.LifecycleConfig{DockerExecutable: docker})
	if err != nil {
		t.Fatal(err)
	}
	harness.lifecycle = lifecycle
	firstContext, cancelFirst := context.WithCancel(context.Background())
	first, _ := harness.newService(t, firstContext)
	result, err := first.SubmitStart(context.Background(), orchestrator.StartSingleServiceInput{WorkspaceID: harness.workspace.ID, SystemID: harness.workspace.SystemID, IdempotencySubject: "gate", IdempotencyKey: "real-compose-start", Request: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	awaitOperation(t, first, result.Operation.ID, domain.OperationSucceeded)
	status, err := first.Status(context.Background(), harness.workspace.ID)
	if err != nil || status.Services[0].ComposeIdentity == "" {
		t.Fatalf("real Compose start status = (%+v, %v)", status, err)
	}
	identity, err := compose.DecodeProjectIdentity(status.Services[0].ComposeIdentity)
	if err != nil {
		t.Fatal(err)
	}
	cleanupRealComposeOrchestration(t, docker, image, identity)
	cancelFirst()
	first.Wait()
	secondContext, cancelSecond := context.WithCancel(context.Background())
	defer cancelSecond()
	second, resumed := harness.newService(t, secondContext)
	if err := second.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, err = second.Status(context.Background(), harness.workspace.ID)
	if err != nil || status.System.ID != identity.InstanceID || status.Services[0].State != domain.ServiceReady || *resumed != 1 {
		t.Fatalf("real Compose recovery = (%+v, %v), resumes=%d", status, err, *resumed)
	}
	stop, err := second.SubmitStop(context.Background(), orchestrator.StopSingleServiceInput{WorkspaceID: harness.workspace.ID, SystemID: harness.workspace.SystemID, IdempotencySubject: "gate", IdempotencyKey: "real-compose-stop", Request: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	awaitOperation(t, second, stop.Operation.ID, domain.OperationSucceeded)
	t.Logf("project=%s instance=%s recovered=true resumed=%d stopped=true", identity.ProjectName, identity.InstanceID, *resumed)
}

func buildComposeOrchestrationImage(t *testing.T, root, image, docker string) {
	t.Helper()
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	goExecutable := filepath.Join(repositoryRoot, ".tools", "go", "bin", "go.exe")
	fixture := filepath.Join(root, "fixture")
	command := exec.Command(goExecutable, "build", "-o", fixture, "./test/fixtures/process-fixture")
	command.Dir = repositoryRoot
	command.Env = append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build Linux fixture: %v: %s", err, output)
	}
	dockerfile := "FROM scratch\nCOPY fixture /fixture\nENTRYPOINT [\"/fixture\",\"--mode\",\"hold-port\",\"--port\",\"5432\"]\n"
	composeFile := fmt.Sprintf("services:\n  database:\n    image: %s\n    volumes: [gate-data:/data]\n    healthcheck:\n      test: [\"CMD\", \"/fixture\", \"-version\"]\n      interval: 1s\n      timeout: 1s\n      retries: 10\nvolumes:\n  gate-data: {}\n", image)
	if err := os.WriteFile(filepath.Join(root, "Dockerfile"), []byte(dockerfile), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "compose.yaml"), []byte(composeFile), 0o600); err != nil {
		t.Fatal(err)
	}
	runExternalGate(t, root, docker, "build", "--tag", image, "--file", filepath.Join(root, "Dockerfile"), root)
}

func cleanupRealComposeOrchestration(t *testing.T, docker, image string, identity compose.ProjectIdentity) {
	t.Helper()
	t.Cleanup(func() {
		arguments := []string{"compose", "--project-name", identity.ProjectName, "--file", identity.ComposeFile, "--file", identity.OverrideFile, "down", "--volumes", "--remove-orphans"}
		runExternalGate(t, filepath.Dir(identity.ComposeFile), docker, arguments...)
		runExternalGate(t, filepath.Dir(identity.ComposeFile), docker, "image", "rm", "--force", image)
	})
}

func runExternalGate(t *testing.T, directory, executable string, arguments ...string) {
	t.Helper()
	command := exec.Command(executable, arguments...)
	command.Dir = directory
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("external Gate command %v: %v: %s", arguments, err, output)
	}
}

type composeHarness struct {
	database        *sql.DB
	workspace       workspace.Record
	workspaces      *workspace.Manager
	operations      *orchestrator.Manager
	runtime         *storage.RuntimeInstanceRepository
	planner         *ports.Planner
	leases          *storage.PortLeaseRepository
	specs           *storage.ResolvedSpecRepository
	overrides       *compose.OverrideGenerator
	lifecycle       *compose.Lifecycle
	commands        *[][]string
	mutex           *sync.Mutex
	discoverMissing *bool
	failBuild       *bool
	dataDir         string
}

func newComposeHarness(t *testing.T) composeHarness {
	return newComposeHarnessWithBuild(t, false)
}

func newComposeHarnessWithBuild(t *testing.T, build bool) composeHarness {
	t.Helper()
	root, dataDir := t.TempDir(), t.TempDir()
	writeComposeManifest(t, root, build)
	database, err := storage.OpenDataDir(context.Background(), dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	workspaceRepository, _ := storage.NewWorkspaceRepository(database)
	loader, _ := manifest.NewLoader()
	workspaces, _ := workspace.NewManager(workspaceRepository, loader, manifest.NewValidatorWithCapabilities("compose", "compose-build"))
	record, err := workspaces.Register(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	broker := events.NewBroker(16)
	operationRepository, _ := storage.NewOperationRepositoryWithNotifier(database, broker)
	operations, _ := orchestrator.NewManager(operationRepository)
	runtimeRepository, _ := storage.NewRuntimeInstanceRepository(database, broker)
	leases, _ := storage.NewPortLeaseRepository(database)
	planner, _ := ports.NewPlanner(ports.Config{Store: leases})
	specs, _ := storage.NewResolvedSpecRepository(database)
	overrides, _ := compose.NewOverrideGenerator(dataDir)
	commands, mutex := make([][]string, 0), &sync.Mutex{}
	discoverMissing := false
	failBuild := false
	lifecycle := composeFixtureLifecycle(t, root, dataDir, &commands, mutex, &discoverMissing, &failBuild)
	return composeHarness{database: database, workspace: *record, workspaces: workspaces, operations: operations, runtime: runtimeRepository, planner: planner, leases: leases, specs: specs, overrides: overrides, lifecycle: lifecycle, commands: &commands, mutex: mutex, discoverMissing: &discoverMissing, failBuild: &failBuild, dataDir: dataDir}
}

func (harness composeHarness) newService(t *testing.T, serviceContext context.Context) (*orchestrator.SingleService, *int) {
	t.Helper()
	resumed := 0
	service, err := orchestrator.NewSingleService(orchestrator.SingleServiceConfig{Context: serviceContext, Operations: harness.operations, Workspaces: harness.workspaces, Runner: fakeRunner{}, Driver: &fakeDriver{observation: driverRunning()}, Compose: harness.lifecycle, Overrides: harness.overrides, Runtime: harness.runtime, Readiness: immediateReadiness{}, DataDir: harness.dataDir, PortPlanner: harness.planner, PortLeases: harness.leases, ResolvedSpecs: harness.specs, StartLogs: func(context.Context, logs.CaptureRequest) (orchestrator.CaptureSession, error) {
		return fakeCapture{}, nil
	}, ResumeLogs: func(context.Context, logs.CaptureRequest) (orchestrator.CaptureSession, error) {
		resumed++
		return fakeCapture{}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	return service, &resumed
}

func driverRunning() driver.RuntimeObservation { return driver.RuntimeObservation{State: "running"} }

func composeFixtureLifecycle(t *testing.T, root, dataDir string, commands *[][]string, mutex *sync.Mutex, discoverMissing, failBuild *bool) *compose.Lifecycle {
	t.Helper()
	docker := filepath.Join(dataDir, "docker.exe")
	if err := os.WriteFile(docker, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	var project, systemID, workspaceID, instanceID string
	lifecycle, err := compose.NewLifecycle(compose.LifecycleConfig{DockerExecutable: docker, Preflight: func(_ context.Context, request compose.PreflightRequest) (*compose.PreflightResult, error) {
		result := &compose.PreflightResult{Readiness: request.Readiness}
		if request.BuildPolicy == "always" {
			result.BuildServices = []string{"database"}
		}
		return result, nil
	}, Run: func(_ context.Context, _ string, arguments []string, _ string, _ map[string]string) (compose.CommandOutput, error) {
		mutex.Lock()
		*commands = append(*commands, append([]string(nil), arguments...))
		if containsTestArgument(arguments, "build") && *failBuild {
			mutex.Unlock()
			return compose.CommandOutput{Stderr: []byte("sensitive build failure")}, errors.New("sensitive build failure")
		}
		if value := argumentAfter(arguments, "--project-name"); value != "" {
			project = value
		}
		if len(arguments) > 0 && arguments[0] == "ps" && containsTestArgument(arguments, "--quiet") {
			if *discoverMissing {
				mutex.Unlock()
				return compose.CommandOutput{}, nil
			}
			systemID = argumentWithPrefix(arguments, "label=stackpilot.system=")
			workspaceID = argumentWithPrefix(arguments, "label=stackpilot.workspace=")
			instanceID = argumentWithPrefix(arguments, "label=stackpilot.instance=")
			mutex.Unlock()
			return compose.CommandOutput{Stdout: []byte(strings.Repeat("a", 64) + "\n")}, nil
		}
		if len(arguments) > 0 && arguments[0] == "inspect" {
			record := []map[string]any{{
				"Id": strings.Repeat("a", 64), "Name": "/" + project + "-database-1",
				"Config": map[string]any{"Labels": map[string]string{
					"stackpilot.system": systemID, "stackpilot.workspace": workspaceID,
					"stackpilot.instance": instanceID, "stackpilot.service": "database",
					"com.docker.compose.project": project, "com.docker.compose.service": "database",
				}},
				"State": map[string]any{"Status": "exited", "ExitCode": 1},
			}}
			mutex.Unlock()
			encoded, _ := json.Marshal(record)
			return compose.CommandOutput{Stdout: encoded}, nil
		}
		mutex.Unlock()
		if containsTestArgument(arguments, "ps") {
			project := argumentAfter(arguments, "--project-name")
			encoded, _ := json.Marshal([]map[string]any{{"ID": "container-id", "Name": project + "-database-1", "Project": project, "Service": "database", "State": "running", "Health": "healthy", "ExitCode": 0}})
			return compose.CommandOutput{Stdout: encoded}, nil
		}
		return compose.CommandOutput{}, nil
	}, StartLog: func(ctx context.Context, _ string, _ []string, _ string, _ map[string]string, _, _ io.Writer) (compose.LogProcess, error) {
		return composeTestLogProcess{ctx: ctx}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	_ = root
	return lifecycle
}

type composeTestLogProcess struct{ ctx context.Context }

func (process composeTestLogProcess) Wait() error { <-process.ctx.Done(); return nil }

func writeComposeManifest(t *testing.T, root string, build bool) {
	t.Helper()
	if err := os.Mkdir(filepath.Join(root, ".stackpilot"), 0o700); err != nil {
		t.Fatal(err)
	}
	composeContents := "services:\n  database: {image: scratch}\n"
	if build {
		if err := os.Mkdir(filepath.Join(root, "database"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "database", "Dockerfile"), []byte("FROM scratch\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		composeContents = "services:\n  database:\n    build: ./database\n    healthcheck: {test: [CMD, true]}\n"
	}
	if err := os.WriteFile(filepath.Join(root, "compose.yaml"), []byte(composeContents), 0o600); err != nil {
		t.Fatal(err)
	}
	buildFields := ""
	if build {
		buildFields = "        buildPolicy: always\n        readiness: {database: healthy}\n"
	}
	contents := "apiVersion: stackpilot.io/v1alpha1\nkind: System\nmetadata: {id: compose-fixture, name: Compose Fixture}\nspec:\n  services:\n    infrastructure:\n      driver: compose\n      compose:\n        file: ./compose.yaml\n        services: [database]\n" + buildFields + "      stop: {gracefulTimeout: 1s}\n      readiness: {type: compose, timeout: 3s, interval: 100ms}\n"
	if err := os.WriteFile(filepath.Join(root, ".stackpilot", "system.yaml"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertComposeCommandCounts(t *testing.T, harness composeHarness, wantBuild, wantUp int) {
	t.Helper()
	harness.mutex.Lock()
	defer harness.mutex.Unlock()
	builds, starts := 0, 0
	for _, command := range *harness.commands {
		if containsTestArgument(command, "build") {
			builds++
		}
		if containsTestArgument(command, "up") {
			starts++
		}
	}
	if builds != wantBuild || starts != wantUp {
		t.Fatalf("Compose commands = build %d, up %d; want build %d, up %d (%#v)", builds, starts, wantBuild, wantUp, *harness.commands)
	}
}

func (harness composeHarness) assertLifecycle(t *testing.T) {
	t.Helper()
	harness.mutex.Lock()
	defer harness.mutex.Unlock()
	starts, stops, inspections := 0, 0, 0
	for _, command := range *harness.commands {
		if containsTestArgument(command, "up") {
			starts++
		}
		if containsTestArgument(command, "stop") {
			stops++
		}
		if containsTestArgument(command, "ps") {
			inspections++
		}
		if containsTestArgument(command, "down") || containsTestArgument(command, "--volumes") {
			t.Fatalf("destructive Compose command: %#v", command)
		}
	}
	if starts != 1 || stops != 1 || inspections < 2 {
		t.Fatalf("Compose lifecycle counts = start %d, stop %d, inspect %d", starts, stops, inspections)
	}
}

func containsTestArgument(arguments []string, expected string) bool {
	for _, argument := range arguments {
		if argument == expected {
			return true
		}
	}
	return false
}

func argumentAfter(arguments []string, expected string) string {
	for index := 0; index+1 < len(arguments); index++ {
		if arguments[index] == expected {
			return arguments[index+1]
		}
	}
	return ""
}

func argumentWithPrefix(arguments []string, prefix string) string {
	for _, argument := range arguments {
		if strings.HasPrefix(argument, prefix) {
			return strings.TrimPrefix(argument, prefix)
		}
	}
	return ""
}
