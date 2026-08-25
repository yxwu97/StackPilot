//go:build windows

package process

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"stackpilot/internal/domain"
	base "stackpilot/internal/driver"
	"stackpilot/internal/platform/windows/supervisor"
	"stackpilot/internal/runner"
	"stackpilot/internal/security"
)

const driverFixtureArgument = "--stackpilot-process-driver-fixture"

func TestProcessDriverFixture(t *testing.T) {
	index := argumentIndex(os.Args, driverFixtureArgument)
	if index < 0 {
		return
	}
	arguments := os.Args[index+1:]
	if len(arguments) == 2 && arguments[0] == "exit" {
		_, _ = fmt.Fprintln(os.Stdout, "oneshot-output")
		if arguments[1] == "23" {
			os.Exit(23)
		}
		return
	}
	if len(arguments) != 1 {
		os.Exit(40)
	}
	contents := fmt.Sprintf("baseline=%s\nport=%s\n", os.Getenv("BASELINE_VALUE"), os.Getenv("SERVER_PORT"))
	if err := os.WriteFile(arguments[0], []byte(contents), 0o600); err != nil {
		os.Exit(41)
	}
	for {
		time.Sleep(time.Hour)
	}
}

func TestProcessDriverOneshotReportsExitAndFlushesOutput(t *testing.T) {
	instanceDir := t.TempDir()
	serviceDir := filepath.Join(instanceDir, "services", "backend")
	if err := os.MkdirAll(serviceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveResult := make(chan error, 1)
	go func() { serveResult <- supervisor.Serve(ctx, supervisor.Config{InstanceDir: instanceDir}) }()
	client, supervisorIdentity := waitForClient(t, instanceDir)
	driver := New(Config{BaselineEnvironment: map[string]string{
		"COMSPEC": os.Getenv("COMSPEC"), "SYSTEMROOT": os.Getenv("SYSTEMROOT"),
	}, connector: fixedConnector{client: client, identity: supervisorIdentity}})
	spec := validDriverSpec(t, instanceDir, filepath.Join(serviceDir, "unused"))
	spec.Mode = domain.ProcessOneshot
	spec.Arguments = []string{"-test.run=TestProcessDriverFixture", "--", driverFixtureArgument, "exit", "23"}
	identity, err := driver.Start(testDriverContext(t), base.StartRequest{Spec: spec})
	if err != nil {
		t.Fatal(err)
	}
	observation := waitForExitedObservation(t, driver, identity)
	if observation.ExitCode == nil || *observation.ExitCode != 23 {
		t.Fatalf("oneshot observation = %+v", observation)
	}
	if err := driver.Stop(testDriverContext(t), base.StopRequest{Identity: identity, GracefulTimeout: 0}); err != nil {
		t.Fatal(err)
	}
	assertFileContains(t, spec.StdoutPath, "oneshot-output")
	assertSupervisorExited(t, instanceDir, serveResult)
}

func waitForExitedObservation(t *testing.T, driver *Driver, identity base.RuntimeIdentity) base.RuntimeObservation {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		observation, err := driver.Inspect(testDriverContext(t), identity)
		if err != nil {
			t.Fatal(err)
		}
		if observation.State == "exited" {
			return observation
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("oneshot process did not exit")
	return base.RuntimeObservation{}
}

func TestProcessDriverStartInspectRecoverStop(t *testing.T) {
	instanceDir := t.TempDir()
	serviceDir := filepath.Join(instanceDir, "services", "backend")
	if err := os.MkdirAll(serviceDir, 0o700); err != nil {
		t.Fatalf("create service directory: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveResult := make(chan error, 1)
	go func() { serveResult <- supervisor.Serve(ctx, supervisor.Config{InstanceDir: instanceDir}) }()
	client, supervisorIdentity := waitForClient(t, instanceDir)
	driver := New(Config{
		BaselineEnvironment: map[string]string{
			"COMSPEC": os.Getenv("COMSPEC"), "SYSTEMROOT": os.Getenv("SYSTEMROOT"), "BASELINE_VALUE": "present",
		},
		connector: fixedConnector{client: client, identity: supervisorIdentity},
	})
	marker := filepath.Join(serviceDir, "environment.txt")
	spec := validDriverSpec(t, instanceDir, marker)
	identity, err := driver.Start(testDriverContext(t), base.StartRequest{Spec: spec})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitForFile(t, marker)
	assertFileContains(t, marker, "baseline=present", "port=32109")
	observation, err := driver.Inspect(testDriverContext(t), identity)
	if err != nil || observation.State != "running" || observation.Identity.PID != identity.PID {
		t.Fatalf("Inspect() = (%#v, %v), want running", observation, err)
	}
	recovered, err := driver.Recover(testDriverContext(t), identity)
	if err != nil || recovered.Observation.State != "running" {
		t.Fatalf("Recover() = (%#v, %v), want running", recovered, err)
	}
	discovered, err := driver.Discover(testDriverContext(t), base.DiscoveryRequest{InstanceDir: instanceDir, ServiceID: "backend"})
	if err != nil || discovered.Observation.State != "running" || discovered.Identity.PID != identity.PID {
		t.Fatalf("Discover() = (%#v, %v), want same running process", discovered, err)
	}
	tampered := identity
	tampered.StartedAt = tampered.StartedAt.Add(time.Nanosecond)
	if err := driver.Stop(testDriverContext(t), base.StopRequest{Identity: tampered}); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("Stop(tampered) error = %v, want identity mismatch", err)
	}
	if err := driver.Stop(testDriverContext(t), base.StopRequest{Identity: identity}); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	assertSupervisorExited(t, instanceDir, serveResult)
}

func TestNodeRunnerStartReadyLogsAndStopsProcessTree(t *testing.T) {
	node, err := exec.LookPath("node.exe")
	if err != nil {
		t.Skip("node.exe is not installed on PATH")
	}
	node, err = security.CanonicalExistingPath(node)
	if err != nil {
		t.Fatal(err)
	}
	instanceDir := t.TempDir()
	serviceDir := filepath.Join(instanceDir, "services", "web")
	if err := os.MkdirAll(serviceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(instanceDir, "node-state.json")
	script := filepath.Join(instanceDir, "server.js")
	source := `const fs=require('fs');const net=require('net');const cp=require('child_process');
const child=cp.spawn(process.execPath,['-e','setInterval(()=>{},1000)'],{stdio:'ignore'});
const server=net.createServer((socket)=>socket.end('ok'));
server.listen(0,'127.0.0.1',()=>{const port=server.address().port;fs.writeFileSync(process.argv[2],JSON.stringify({port,childPid:child.pid}));console.log('node-ready '+port);});`
	if err := os.WriteFile(script, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveResult := make(chan error, 1)
	go func() { serveResult <- supervisor.Serve(ctx, supervisor.Config{InstanceDir: instanceDir}) }()
	client, supervisorIdentity := waitForClient(t, instanceDir)
	driver := New(Config{BaselineEnvironment: map[string]string{
		"COMSPEC": os.Getenv("COMSPEC"), "SYSTEMROOT": os.Getenv("SYSTEMROOT"),
	}, connector: fixedConnector{client: client, identity: supervisorIdentity}})
	spec := base.ResolvedServiceSpec{ServiceID: "web", Driver: domain.DriverProcess, Mode: domain.ProcessDaemon,
		WorkspaceRoot: instanceDir, InstanceDir: instanceDir, WorkingDirectory: instanceDir,
		Command:   runner.ResolvedCommand{Executable: node, ExecutableDigest: executableDigest(t, node)},
		Arguments: []string{script, marker}, Environment: map[string]string{},
		StdoutPath: filepath.Join(serviceDir, "stdout.spool"), StderrPath: filepath.Join(serviceDir, "stderr.spool"),
		GracefulTimeout: time.Second}
	identity, err := driver.Start(testDriverContext(t), base.StartRequest{Spec: spec})
	if err != nil {
		t.Fatal(err)
	}
	state := waitForNodeState(t, marker)
	connection, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", state.Port), time.Second)
	if err != nil {
		t.Fatalf("dial Node readiness: %v", err)
	}
	_ = connection.Close()
	assertFileContains(t, spec.StdoutPath, "node-ready")
	if err := driver.Stop(testDriverContext(t), base.StopRequest{Identity: identity}); err != nil {
		t.Fatal(err)
	}
	waitForProcessExit(t, uint32(state.ChildPID))
	assertSupervisorExited(t, instanceDir, serveResult)
}

type nodeFixtureState struct {
	Port     int `json:"port"`
	ChildPID int `json:"childPid"`
}

func waitForNodeState(t *testing.T, path string) nodeFixtureState {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		contents, err := os.ReadFile(path)
		var state nodeFixtureState
		if err == nil && json.Unmarshal(contents, &state) == nil && state.Port > 0 && state.ChildPID > 0 {
			return state
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("Node fixture did not become ready")
	return nodeFixtureState{}
}

func waitForProcessExit(t *testing.T, pid uint32) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, pid)
		if err != nil {
			return
		}
		status, _ := windows.WaitForSingleObject(handle, 0)
		_ = windows.CloseHandle(handle)
		if status == windows.WAIT_OBJECT_0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("Node child process %d remained after stop", pid)
}

func TestProcessDriverStopTreatsMissingSupervisorAsMissingRuntime(t *testing.T) {
	pipeName, err := supervisor.NewPipeName()
	if err != nil {
		t.Fatal(err)
	}
	token, err := encodePlatformToken(platformToken{
		Supervisor: supervisor.SupervisorIdentity{
			PID: ^uint32(0), CreatedAt: time.Now().UTC(), ExecutablePath: `C:\fixture\stackpilot.exe`,
			AccountSID: "S-1-5-21-fixture", PipeName: pipeName, ProtocolVersion: supervisor.ProtocolVersion,
		},
		ServiceID: "backend",
	})
	if err != nil {
		t.Fatal(err)
	}
	driver := New(Config{})
	err = driver.Stop(testDriverContext(t), base.StopRequest{Identity: base.RuntimeIdentity{PlatformToken: token}})
	if !errors.Is(err, ErrRuntimeNotFound) {
		t.Fatalf("Stop(missing Supervisor) error = %v, want runtime not found", err)
	}
}

func TestProcessDriverPreflightAcceptsOneshotAndRejectsUnsafeSpecs(t *testing.T) {
	instanceDir := t.TempDir()
	serviceDir := filepath.Join(instanceDir, "services", "backend")
	if err := os.MkdirAll(serviceDir, 0o700); err != nil {
		t.Fatalf("create service directory: %v", err)
	}
	driver := New(Config{BaselineEnvironment: map[string]string{"COMSPEC": os.Getenv("COMSPEC")}})
	spec := validDriverSpec(t, instanceDir, filepath.Join(serviceDir, "unused"))
	spec.Mode = domain.ProcessOneshot
	if err := driver.Preflight(context.Background(), spec); err != nil {
		t.Fatalf("Preflight(oneshot) error = %v", err)
	}
	spec.Mode = domain.ProcessDaemon
	spec.Environment["PATH"] = `C:\untrusted`
	if err := driver.Preflight(context.Background(), spec); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("Preflight(PATH override) error = %v", err)
	}
	delete(spec.Environment, "PATH")
	spec.StdoutPath = filepath.Join(t.TempDir(), "escaped.log")
	if err := driver.Preflight(context.Background(), spec); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("Preflight(spool escape) error = %v", err)
	}
}

func TestStartMessageCarriesOnlySecretEnvironmentNames(t *testing.T) {
	instanceDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(instanceDir, "services", "backend"), 0o700); err != nil {
		t.Fatal(err)
	}
	spec := validDriverSpec(t, instanceDir, filepath.Join(instanceDir, "marker"))
	spec.Environment["TOKEN"] = "resolved-value"
	spec.SecretReferences = map[string]string{"TOKEN": "api-key"}
	message := startMessage(spec, spec.Environment)
	if len(message.SecretEnvironmentNames) != 1 || message.SecretEnvironmentNames[0] != "TOKEN" {
		t.Fatalf("Secret environment names = %#v", message.SecretEnvironmentNames)
	}
}

type fixedConnector struct {
	client   supervisorClient
	identity supervisor.SupervisorIdentity
}

func (connector fixedConnector) connect(context.Context, string) (supervisorClient, supervisor.SupervisorIdentity, error) {
	return connector.client, connector.identity, nil
}

func validDriverSpec(t *testing.T, instanceDir, marker string) base.ResolvedServiceSpec {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	serviceDir := filepath.Join(instanceDir, "services", "backend")
	return base.ResolvedServiceSpec{
		ServiceID: "backend", Driver: domain.DriverProcess, Mode: domain.ProcessDaemon,
		WorkspaceRoot: instanceDir, InstanceDir: instanceDir, WorkingDirectory: instanceDir,
		Command:     runner.ResolvedCommand{Executable: executable, ExecutableDigest: executableDigest(t, executable)},
		Arguments:   []string{"-test.run=TestProcessDriverFixture", "--", driverFixtureArgument, marker},
		Environment: map[string]string{"SERVER_PORT": "32109"},
		StdoutPath:  filepath.Join(serviceDir, "stdout.spool"), StderrPath: filepath.Join(serviceDir, "stderr.spool"),
		GracefulTimeout: time.Second,
	}
}

func executableDigest(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read test executable: %v", err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(contents))
}

func waitForClient(t *testing.T, instanceDir string) (*supervisor.Client, supervisor.SupervisorIdentity) {
	t.Helper()
	ctx := testDriverContext(t)
	path := filepath.Join(instanceDir, "supervisor.json")
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		identity, err := supervisor.ReadSupervisorIdentity(path)
		if err == nil {
			client, connectErr := supervisor.Connect(ctx, identity)
			if connectErr == nil {
				return client, identity
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("Supervisor was not connectable: %v", ctx.Err())
		case <-ticker.C:
		}
	}
}

func assertSupervisorExited(t *testing.T, instanceDir string, result <-chan error) {
	t.Helper()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Supervisor exit error = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Supervisor did not exit")
	}
	if _, err := os.Stat(filepath.Join(instanceDir, "supervisor.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Supervisor identity remained after stop: %v", err)
	}
}

func testDriverContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("fixture file was not written: %s", path)
}

func assertFileContains(t *testing.T, path string, values ...string) {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture file: %v", err)
	}
	for _, value := range values {
		if !strings.Contains(string(contents), value) {
			t.Fatalf("fixture file = %q, want %q", contents, value)
		}
	}
}

func argumentIndex(arguments []string, value string) int {
	for index, argument := range arguments {
		if argument == value {
			return index
		}
	}
	return -1
}
