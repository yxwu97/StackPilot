//go:build windows

package compose

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"stackpilot/internal/domain"
	stacklogs "stackpilot/internal/logs"
)

func TestLifecycleUsesFixedStartInspectAndNonDestructiveStop(t *testing.T) {
	request, docker := lifecycleFixture(t)
	commands := make([][]string, 0, 4)
	preflights := 0
	lifecycle, err := NewLifecycle(LifecycleConfig{
		DockerExecutable: docker,
		Environment:      map[string]string{"PATH": `C:\trusted`},
		Preflight: func(_ context.Context, value PreflightRequest) (*PreflightResult, error) {
			preflights++
			if value.ComposeFile != request.ComposeFile || !reflect.DeepEqual(value.Services, []string{"database", "web"}) {
				t.Fatalf("unexpected preflight request: %#v", value)
			}
			return &PreflightResult{}, nil
		},
		Run: func(_ context.Context, executable string, arguments []string, directory string, environment map[string]string) (CommandOutput, error) {
			if executable != docker || directory != filepath.Dir(request.ComposeFile) || environment["PATH"] != `C:\trusted` {
				t.Fatalf("unsafe lifecycle command inputs: %q %q %#v", executable, directory, environment)
			}
			commands = append(commands, append([]string(nil), arguments...))
			if containsArgument(arguments, "ps") {
				return CommandOutput{Stdout: lifecyclePSOutput(t, request)}, nil
			}
			return CommandOutput{}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := lifecycle.Start(context.Background(), request)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	observation, err := lifecycle.Inspect(context.Background(), identity)
	if err != nil || observation.State != "running" {
		t.Fatalf("Inspect() = %#v, %v", observation, err)
	}
	if err := lifecycle.Stop(context.Background(), identity); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	assertLifecycleCommands(t, identity, commands)
	if preflights != 1 {
		t.Fatalf("preflight calls = %d, want 1", preflights)
	}
}

func TestLifecycleRejectsTamperedIdentityAndEscapedOverride(t *testing.T) {
	request, docker := lifecycleFixture(t)
	runs := 0
	lifecycle, _ := NewLifecycle(LifecycleConfig{
		DockerExecutable: docker,
		Preflight:        func(context.Context, PreflightRequest) (*PreflightResult, error) { return &PreflightResult{}, nil },
		Run: func(_ context.Context, _ string, arguments []string, _ string, _ map[string]string) (CommandOutput, error) {
			runs++
			if containsArgument(arguments, "ps") {
				return CommandOutput{Stdout: lifecyclePSOutput(t, request)}, nil
			}
			return CommandOutput{}, nil
		},
	})
	identity, err := lifecycle.Start(context.Background(), request)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	identity.ProjectName = "tampered"
	if err := lifecycle.Stop(context.Background(), identity); !errors.Is(err, ErrProjectIdentityMismatch) {
		t.Fatalf("Stop(tampered) error = %v", err)
	}
	before := runs
	request.OverrideFile = filepath.Join(t.TempDir(), overrideFileName)
	if err := os.WriteFile(request.OverrideFile, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := lifecycle.Start(context.Background(), request); !errors.Is(err, ErrLifecycleInvalid) {
		t.Fatalf("Start(escaped override) error = %v", err)
	}
	if runs != before {
		t.Fatal("invalid identity or path executed Docker")
	}
}

func TestLifecycleMapsTimeoutAndDoesNotExposeCommandOutput(t *testing.T) {
	request, docker := lifecycleFixture(t)
	request.StartTimeout = 10 * time.Millisecond
	lifecycle, _ := NewLifecycle(LifecycleConfig{
		DockerExecutable: docker,
		Preflight:        func(context.Context, PreflightRequest) (*PreflightResult, error) { return &PreflightResult{}, nil },
		Run: func(ctx context.Context, _ string, _ []string, _ string, _ map[string]string) (CommandOutput, error) {
			<-ctx.Done()
			return CommandOutput{Stderr: []byte("sensitive Docker detail")}, ctx.Err()
		},
	})
	_, err := lifecycle.Start(context.Background(), request)
	if !errors.Is(err, ErrLifecycleTimeout) || strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("Start(timeout) error = %v", err)
	}
}

func TestLifecycleBuildsThenUsesNoBuildAndServiceRestartSkipsBuild(t *testing.T) {
	request, docker := lifecycleFixture(t)
	request.BuildPolicy = "always"
	commands := make([][]string, 0)
	lifecycle, _ := NewLifecycle(LifecycleConfig{
		DockerExecutable: docker,
		Preflight: func(_ context.Context, value PreflightRequest) (*PreflightResult, error) {
			return &PreflightResult{BuildServices: []string{"web"}, Readiness: value.Readiness}, nil
		},
		Run: func(_ context.Context, _ string, arguments []string, _ string, _ map[string]string) (CommandOutput, error) {
			commands = append(commands, append([]string(nil), arguments...))
			if containsArgument(arguments, "ps") {
				return CommandOutput{Stdout: lifecyclePSOutput(t, request)}, nil
			}
			return CommandOutput{}, nil
		},
	})
	identity, err := lifecycle.Start(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	base := []string{"compose", "--project-name", identity.ProjectName, "--file", identity.ComposeFile, "--file", identity.OverrideFile}
	wantBuild := append(append([]string(nil), base...), "build", "web")
	wantUp := append(append([]string(nil), base...), "up", "-d", "--wait", "--no-deps", "--no-build", "--wait-timeout", "45", "database", "web")
	if len(commands) < 2 || !reflect.DeepEqual(commands[0], wantBuild) || !reflect.DeepEqual(commands[1], wantUp) {
		t.Fatalf("build/up commands = %#v", commands)
	}
	commands = nil
	if _, err := lifecycle.StartWithoutBuild(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	for _, command := range commands {
		if containsArgument(command, "build") && !containsArgument(command, "--no-build") {
			t.Fatalf("service restart built image: %#v", commands)
		}
	}
	if identity.BuildPolicy != "always" || !reflect.DeepEqual(identity.BuildServices, []string{"web"}) {
		t.Fatalf("identity build facts = %#v", identity)
	}
}

func TestLifecycleBuildFailureTimeoutAndCancellationNeverRunUp(t *testing.T) {
	tests := []struct {
		name      string
		timeout   time.Duration
		run       func(context.Context) error
		want      error
		forbidden string
	}{
		{name: "failure", timeout: time.Second, run: func(context.Context) error { return errors.New("sensitive build output") }, want: ErrComposeBuildFailed, forbidden: "sensitive"},
		{name: "timeout", timeout: 10 * time.Millisecond, run: func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }, want: ErrComposeBuildTimeout},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, docker := lifecycleFixture(t)
			request.BuildPolicy, request.StartTimeout = "always", test.timeout
			commands := make([][]string, 0)
			lifecycle, _ := NewLifecycle(LifecycleConfig{
				DockerExecutable: docker,
				Preflight: func(_ context.Context, value PreflightRequest) (*PreflightResult, error) {
					return &PreflightResult{BuildServices: []string{"web"}, Readiness: value.Readiness}, nil
				},
				Run: func(ctx context.Context, _ string, arguments []string, _ string, _ map[string]string) (CommandOutput, error) {
					commands = append(commands, append([]string(nil), arguments...))
					return CommandOutput{Stderr: []byte("sensitive build output")}, test.run(ctx)
				},
			})
			_, err := lifecycle.Start(context.Background(), request)
			if !errors.Is(err, test.want) || (test.forbidden != "" && strings.Contains(err.Error(), test.forbidden)) {
				t.Fatalf("Start(build %s) error = %v", test.name, err)
			}
			if len(commands) != 1 || !containsArgument(commands[0], "build") || containsArgument(commands[0], "up") {
				t.Fatalf("commands after build %s = %#v", test.name, commands)
			}
		})
	}

	request, docker := lifecycleFixture(t)
	request.BuildPolicy = "always"
	commands := make([][]string, 0)
	ctx, cancel := context.WithCancel(context.Background())
	lifecycle, _ := NewLifecycle(LifecycleConfig{
		DockerExecutable: docker,
		Preflight: func(_ context.Context, value PreflightRequest) (*PreflightResult, error) {
			return &PreflightResult{BuildServices: []string{"web"}, Readiness: value.Readiness}, nil
		},
		Run: func(runContext context.Context, _ string, arguments []string, _ string, _ map[string]string) (CommandOutput, error) {
			commands = append(commands, append([]string(nil), arguments...))
			cancel()
			<-runContext.Done()
			return CommandOutput{}, runContext.Err()
		},
	})
	_, err := lifecycle.Start(ctx, request)
	if !errors.Is(err, context.Canceled) || len(commands) != 1 || !containsArgument(commands[0], "build") {
		t.Fatalf("Start(cancelled build) = %v, commands %#v", err, commands)
	}
}

func TestLifecycleFollowsLogsIntoBoundedRuntimeSpools(t *testing.T) {
	request, docker := lifecycleFixture(t)
	var logArguments []string
	lifecycle, _ := NewLifecycle(LifecycleConfig{
		DockerExecutable: docker,
		Preflight:        func(context.Context, PreflightRequest) (*PreflightResult, error) { return &PreflightResult{}, nil },
		Run: func(_ context.Context, _ string, arguments []string, _ string, _ map[string]string) (CommandOutput, error) {
			if containsArgument(arguments, "ps") {
				return CommandOutput{Stdout: lifecyclePSOutput(t, request)}, nil
			}
			return CommandOutput{}, nil
		},
		StartLog: func(ctx context.Context, executable string, arguments []string, directory string, _ map[string]string, stdout, stderr io.Writer) (LogProcess, error) {
			if executable != docker || directory != filepath.Dir(request.ComposeFile) {
				t.Fatalf("unsafe log command inputs: %q %q", executable, directory)
			}
			logArguments = append([]string(nil), arguments...)
			_, _ = io.WriteString(stdout, "database | 2026-08-18T00:00:00Z stdout line\n")
			_, _ = io.WriteString(stderr, "safe tool stderr\n")
			return testLogProcess{ctx: ctx}, nil
		},
	})
	identity, err := lifecycle.Start(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	spoolDir := filepath.Join(request.DataDir, "spools")
	if err := os.Mkdir(spoolDir, 0o700); err != nil {
		t.Fatal(err)
	}
	stdoutPath, stderrPath := filepath.Join(spoolDir, "stdout.log"), filepath.Join(spoolDir, "stderr.log")
	since := time.Date(2026, 8, 18, 0, 0, 0, 123, time.UTC)
	session, err := lifecycle.FollowLogs(context.Background(), LogFollowRequest{Identity: identity, StdoutPath: stdoutPath, StderrPath: stderrPath, Since: since})
	if err != nil {
		t.Fatalf("FollowLogs() error = %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("LogSession.Close() error = %v", err)
	}
	base := projectArguments(identity)
	want := append(append([]string(nil), base...), "logs", "--no-color", "--timestamps", "--follow", "--since", since.Format(time.RFC3339Nano), "database", "web")
	if !reflect.DeepEqual(logArguments, want) {
		t.Fatalf("log arguments = %#v, want %#v", logArguments, want)
	}
	stdout, _ := os.ReadFile(stdoutPath)
	stderr, _ := os.ReadFile(stderrPath)
	if !strings.Contains(string(stdout), "stdout line") || !strings.Contains(string(stderr), "safe tool stderr") {
		t.Fatalf("unexpected spools: stdout=%q stderr=%q", stdout, stderr)
	}
	outside := filepath.Join(t.TempDir(), "escaped.log")
	if _, err := lifecycle.FollowLogs(context.Background(), LogFollowRequest{Identity: identity, StdoutPath: outside, StderrPath: stderrPath}); !errors.Is(err, ErrLifecycleInvalid) {
		t.Fatalf("FollowLogs(escape) error = %v", err)
	}
}

func TestLifecycleChecksComposeContainerHealth(t *testing.T) {
	request, docker := lifecycleFixture(t)
	unhealthy := false
	lifecycle, _ := NewLifecycle(LifecycleConfig{
		DockerExecutable: docker,
		Preflight:        func(context.Context, PreflightRequest) (*PreflightResult, error) { return &PreflightResult{}, nil },
		Run: func(_ context.Context, _ string, arguments []string, _ string, _ map[string]string) (CommandOutput, error) {
			if !containsArgument(arguments, "ps") {
				return CommandOutput{}, nil
			}
			output := []byte(strings.Replace(string(lifecyclePSOutput(t, request)), `"Health":""`, `"Health":"healthy"`, 1))
			output = []byte(strings.Replace(string(output), "\"Health\":\"\"", "\"Health\":\"healthy\"", 1))
			if unhealthy {
				output = []byte(strings.Replace(string(output), "\"Health\":\"healthy\"", "\"Health\":\"unhealthy\"", 1))
				output = []byte(strings.Replace(string(output), `"Health":"healthy"`, `"Health":"unhealthy"`, 1))
			}
			return CommandOutput{Stdout: output}, nil
		},
	})
	identity, err := lifecycle.Start(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	health := lifecycle.CheckHealth(context.Background(), identity)
	if !health.Ready || health.ErrorCode != "" {
		t.Fatalf("healthy result = %#v", health)
	}
	unhealthy = true
	health = lifecycle.CheckHealth(context.Background(), identity)
	if health.Ready || health.ErrorCode != "CONTAINER_UNHEALTHY" {
		t.Fatalf("unhealthy result = %#v", health)
	}
}

type testLogProcess struct{ ctx context.Context }

func (process testLogProcess) Wait() error {
	<-process.ctx.Done()
	return process.ctx.Err()
}

func TestLifecycleRecoversAndDiscoversProjectIdentity(t *testing.T) {
	request, docker := lifecycleFixture(t)
	identity, _ := newProjectIdentity(normalizedLifecycleRequest(request))
	token, _ := EncodeProjectIdentity(identity)
	idA, idB := strings.Repeat("a", 64), strings.Repeat("b", 64)
	var commands [][]string
	lifecycle, _ := NewLifecycle(LifecycleConfig{
		DockerExecutable: docker,
		Preflight:        func(context.Context, PreflightRequest) (*PreflightResult, error) { return &PreflightResult{}, nil },
		Run: func(_ context.Context, _ string, arguments []string, _ string, _ map[string]string) (CommandOutput, error) {
			commands = append(commands, append([]string(nil), arguments...))
			switch arguments[0] {
			case "compose":
				return CommandOutput{Stdout: lifecyclePSOutput(t, request)}, nil
			case "ps":
				return CommandOutput{Stdout: []byte(idA + "\n" + idB + "\n")}, nil
			case "inspect":
				return CommandOutput{Stdout: discoveryInspectOutput(t, identity, idA, idB)}, nil
			default:
				return CommandOutput{}, errors.New("unexpected command")
			}
		},
	})
	recovered, observation, err := lifecycle.Recover(context.Background(), token)
	if err != nil || recovered.DefinitionDigest != identity.DefinitionDigest || observation.State != "running" {
		t.Fatalf("Recover() = (%#v, %#v, %v)", recovered, observation, err)
	}
	discovered, observation, err := lifecycle.Discover(context.Background(), request)
	if err != nil || discovered.ProjectName != identity.ProjectName || observation.State != "running" {
		t.Fatalf("Discover() = (%#v, %#v, %v)", discovered, observation, err)
	}
	filters := discoveryFilters(identity)
	for _, filter := range filters {
		if !containsArgument(commands[1], filter) {
			t.Fatalf("discovery command missing filter %q: %#v", filter, commands[1])
		}
	}
}

func TestLifecycleResourceObservationDoesNotBlockStop(t *testing.T) {
	request, docker := lifecycleFixture(t)
	identity, err := newProjectIdentity(normalizedLifecycleRequest(request))
	if err != nil {
		t.Fatal(err)
	}
	token, err := EncodeProjectIdentity(identity)
	if err != nil {
		t.Fatal(err)
	}
	idA, idB := strings.Repeat("a", 64), strings.Repeat("b", 64)
	psOutput := []byte(strings.NewReplacer("db-id", idA, "web-id", idB).Replace(string(lifecyclePSOutput(t, request))))
	statsOutput := []byte(fmt.Sprintf("{\"ID\":%q,\"CPUPerc\":\"0%%\",\"MemUsage\":\"1MiB / 2GiB\"}\n{\"ID\":%q,\"CPUPerc\":\"0%%\",\"MemUsage\":\"2MiB / 2GiB\"}\n", idA, idB))
	enteredStats := make(chan struct{})
	releaseStats := make(chan struct{})
	stopRan := make(chan struct{}, 1)

	lifecycle, err := NewLifecycle(LifecycleConfig{
		DockerExecutable: docker,
		Run: func(ctx context.Context, _ string, arguments []string, _ string, _ map[string]string) (CommandOutput, error) {
			switch arguments[0] {
			case "compose":
				if containsArgument(arguments, "ps") {
					return CommandOutput{Stdout: psOutput}, nil
				}
				if containsArgument(arguments, "stop") {
					stopRan <- struct{}{}
					return CommandOutput{}, nil
				}
			case "stats":
				close(enteredStats)
				select {
				case <-releaseStats:
					return CommandOutput{Stdout: statsOutput}, nil
				case <-ctx.Done():
					return CommandOutput{}, ctx.Err()
				}
			}
			return CommandOutput{}, fmt.Errorf("unexpected Docker arguments: %v", arguments)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	observeDone := make(chan error, 1)
	go func() {
		_, observeErr := lifecycle.ObserveResources(context.Background(), token)
		observeDone <- observeErr
	}()
	select {
	case <-enteredStats:
	case <-time.After(time.Second):
		t.Fatal("resource observation did not enter Docker stats")
	}
	stopDone := make(chan error, 1)
	go func() { stopDone <- lifecycle.Stop(context.Background(), identity) }()
	select {
	case stopErr := <-stopDone:
		if stopErr != nil {
			t.Fatalf("Stop() while stats was blocked error = %v", stopErr)
		}
	case <-time.After(time.Second):
		close(releaseStats)
		t.Fatal("Stop() waited for blocked resource observation")
	}
	select {
	case <-stopRan:
	default:
		t.Fatal("Stop() completed without running the fixed Compose stop command")
	}
	close(releaseStats)
	select {
	case observeErr := <-observeDone:
		if observeErr != nil {
			t.Fatalf("ObserveResources() after release error = %v", observeErr)
		}
	case <-time.After(time.Second):
		t.Fatal("resource observation did not finish after Docker stats was released")
	}
}

func discoveryInspectOutput(t *testing.T, identity ProjectIdentity, idA, idB string) []byte {
	t.Helper()
	record := func(id, service string) map[string]any {
		return map[string]any{
			"Id": id, "Name": "/" + identity.ProjectName + "-" + service + "-1",
			"Config": map[string]any{"Labels": map[string]string{
				"stackpilot.system": identity.SystemID.String(), "stackpilot.workspace": identity.WorkspaceID.String(),
				"stackpilot.instance": identity.InstanceID.String(), "stackpilot.service": service,
				"com.docker.compose.project": identity.ProjectName, "com.docker.compose.service": service,
			}},
			"State": map[string]any{"Status": "running", "ExitCode": 0, "Health": map[string]string{"Status": "healthy"}},
		}
	}
	encoded, err := json.Marshal([]map[string]any{record(idA, "database"), record(idB, "web")})
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestInstalledComposeLifecycle(t *testing.T) {
	if os.Getenv("STACKPILOT_COMPOSE_LIFECYCLE_INTEGRATION") != "1" {
		t.Skip("set STACKPILOT_COMPOSE_LIFECYCLE_INTEGRATION=1 for the real Docker lifecycle Gate")
	}
	docker, err := exec.LookPath("docker.exe")
	if err != nil {
		t.Fatalf("resolve docker.exe: %v", err)
	}
	root, dataDir := t.TempDir(), t.TempDir()
	image := "stackpilot-p2b04-gate:" + strconv.Itoa(os.Getpid())
	buildLinuxFixture(t, root)
	writeLifecycleComposeFixture(t, root, image)
	runGateCommand(t, root, docker, "build", "--tag", image, "--file", filepath.Join(root, "Dockerfile"), root)
	request := lifecycleIntegrationRequestForServices(t, root, dataDir, []string{"database", "web"})
	request.Readiness = map[string]string{"database": "healthy", "web": "healthy"}
	project, _ := ProjectName(request.SystemID, request.WorkspaceID, request.InstanceID)
	t.Cleanup(func() {
		cleanupContext, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		base := []string{"compose", "--project-name", project, "--file", request.ComposeFile, "--file", request.OverrideFile}
		environment := normalizedEnvironment(currentEnvironment())
		if _, err := runCommand(cleanupContext, docker, append(base, "down", "--volumes", "--remove-orphans"), root, environment); err != nil {
			t.Errorf("clean Compose Gate project: %v", err)
		}
		if _, err := runCommand(cleanupContext, docker, []string{"image", "rm", "--force", image}, root, environment); err != nil {
			t.Errorf("clean Compose Gate image: %v", err)
		}
	})
	lifecycle, err := NewLifecycle(LifecycleConfig{DockerExecutable: docker})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := lifecycle.Start(context.Background(), request)
	if err != nil {
		t.Fatalf("Start(real Compose) error = %v", err)
	}
	observation, err := lifecycle.Inspect(context.Background(), identity)
	if err != nil || observation.State != "running" || observation.Containers[0].Health != "healthy" {
		t.Fatalf("Inspect(running) = %#v, %v", observation, err)
	}
	token, err := EncodeProjectIdentity(identity)
	if err != nil {
		t.Fatal(err)
	}
	resources, err := lifecycle.ObserveResources(context.Background(), token)
	if err != nil || resources.ObservedAt.Location() != time.UTC || resources.MemoryBytes <= 0 ||
		resources.CPUPercent < 0 || resources.CPUPercent > 100 || !resourceServices(resources, "database", "web") {
		t.Fatalf("ObserveResources(real Compose) = %#v, %v", resources, err)
	}
	restarted, _ := NewLifecycle(LifecycleConfig{DockerExecutable: docker})
	recovered, recoveredObservation, err := restarted.Recover(context.Background(), token)
	if err != nil || recovered.DefinitionDigest != identity.DefinitionDigest || recoveredObservation.State != "running" {
		t.Fatalf("Recover(real Compose) = (%#v, %#v, %v)", recovered, recoveredObservation, err)
	}
	discovered, discoveredObservation, err := restarted.Discover(context.Background(), request)
	if err != nil || discovered.ProjectName != identity.ProjectName || discoveredObservation.State != "running" {
		t.Fatalf("Discover(real Compose) = (%#v, %#v, %v)", discovered, discoveredObservation, err)
	}
	if err := lifecycle.Stop(context.Background(), identity); err != nil {
		t.Fatalf("Stop(real Compose) error = %v", err)
	}
	volumeOutput, err := runCommand(context.Background(), docker, []string{"volume", "ls", "--quiet", "--filter", "label=com.docker.compose.project=" + project}, root, normalizedEnvironment(currentEnvironment()))
	if err != nil || strings.TrimSpace(string(volumeOutput.Stdout)) == "" {
		t.Fatalf("named volume was removed by ordinary stop: output=%q err=%v", volumeOutput.Stdout, err)
	}
	observation, err = lifecycle.Inspect(context.Background(), identity)
	if err != nil || observation.State != "stopped" {
		t.Fatalf("Inspect(stopped) = %#v, %v", observation, err)
	}
	t.Logf("project=%s container=%s health=healthy recovered=true discovered=true postStop=%s", identity.ProjectName, observation.Containers[0].Name, observation.State)
}

func TestInstalledControlledComposeBuildLifecycle(t *testing.T) {
	if os.Getenv("STACKPILOT_COMPOSE_BUILD_INTEGRATION") != "1" {
		t.Skip("set STACKPILOT_COMPOSE_BUILD_INTEGRATION=1 for the real controlled build Gate")
	}
	docker, err := exec.LookPath("docker.exe")
	if err != nil {
		t.Fatalf("resolve docker.exe: %v", err)
	}
	root, dataDir := t.TempDir(), t.TempDir()
	image := "stackpilot-controlled-build-gate:" + strconv.Itoa(os.Getpid())
	buildLinuxFixture(t, root)
	writeControlledBuildComposeFixture(t, root, image)
	request := lifecycleIntegrationRequest(t, root, dataDir)
	request.BuildPolicy = "always"
	request.Readiness = map[string]string{"database": "healthy"}
	project, _ := ProjectName(request.SystemID, request.WorkspaceID, request.InstanceID)
	cleanupComposeGate(t, docker, root, image, project, request)

	lifecycle, err := NewLifecycle(LifecycleConfig{DockerExecutable: docker})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := lifecycle.Start(context.Background(), request)
	if err != nil {
		t.Fatalf("Start(controlled build) error = %v", err)
	}
	if identity.BuildPolicy != "always" || !reflect.DeepEqual(identity.BuildServices, []string{"database"}) {
		t.Fatalf("controlled build identity = %#v", identity)
	}
	runGateCommand(t, root, docker, "image", "inspect", image)
	if err := lifecycle.Stop(context.Background(), identity); err != nil {
		t.Fatalf("Stop(controlled build) error = %v", err)
	}

	invalidDockerfile := []byte("THIS IS NOT A VALID DOCKERFILE\n")
	if err := os.WriteFile(filepath.Join(root, "Dockerfile"), invalidDockerfile, 0o600); err != nil {
		t.Fatal(err)
	}
	restarted, err := lifecycle.StartWithoutBuild(context.Background(), request)
	if err != nil {
		t.Fatalf("StartWithoutBuild(existing image) error = %v", err)
	}
	if observation, err := lifecycle.Inspect(context.Background(), restarted); err != nil || observation.State != "running" {
		t.Fatalf("Inspect(StartWithoutBuild) = (%#v, %v)", observation, err)
	}
	if err := lifecycle.Stop(context.Background(), restarted); err != nil {
		t.Fatalf("Stop(StartWithoutBuild) error = %v", err)
	}
	if _, err := lifecycle.Start(context.Background(), request); !errors.Is(err, ErrComposeBuildFailed) {
		t.Fatalf("Start(invalid Dockerfile) error = %v, want %v", err, ErrComposeBuildFailed)
	}
	if observation, err := lifecycle.Inspect(context.Background(), restarted); err != nil || observation.State != "stopped" {
		t.Fatalf("Inspect(after failed build) = (%#v, %v)", observation, err)
	}
	t.Logf("project=%s image=%s built=true restart_without_build=true failed_build_kept_stopped=true", project, image)
}

func TestInstalledComposeLogsAndHealth(t *testing.T) {
	if os.Getenv("STACKPILOT_COMPOSE_LOG_HEALTH_INTEGRATION") != "1" {
		t.Skip("set STACKPILOT_COMPOSE_LOG_HEALTH_INTEGRATION=1 for the real Compose logs/health Gate")
	}
	docker, err := exec.LookPath("docker.exe")
	if err != nil {
		t.Fatal(err)
	}
	root, dataDir := t.TempDir(), t.TempDir()
	image := "stackpilot-p2b05-gate:" + strconv.Itoa(os.Getpid())
	buildLinuxFixture(t, root)
	writeLogHealthComposeFixture(t, root, image)
	runGateCommand(t, root, docker, "build", "--tag", image, "--file", filepath.Join(root, "Dockerfile"), root)
	request := lifecycleIntegrationRequest(t, root, dataDir)
	project, _ := ProjectName(request.SystemID, request.WorkspaceID, request.InstanceID)
	cleanupComposeGate(t, docker, root, image, project, request)
	lifecycle, _ := NewLifecycle(LifecycleConfig{DockerExecutable: docker})
	identity, err := lifecycle.Start(context.Background(), request)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if health := lifecycle.CheckHealth(context.Background(), identity); !health.Ready {
		t.Fatalf("CheckHealth() = %#v", health)
	}
	entry := captureRealComposeLog(t, lifecycle, identity, dataDir)
	if !strings.Contains(entry.Message, "p2b05-compose-log") || entry.Sequence != 1 {
		t.Fatalf("unexpected unified log entry: %#v", entry)
	}
	if err := lifecycle.Stop(context.Background(), identity); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	t.Logf("project=%s health=healthy sequence=%d message=%q", project, entry.Sequence, entry.Message)
}

func captureRealComposeLog(t *testing.T, lifecycle *Lifecycle, identity ProjectIdentity, dataDir string) stacklogs.Entry {
	t.Helper()
	spoolDir := filepath.Join(dataDir, "compose-spools")
	if err := os.Mkdir(spoolDir, 0o700); err != nil {
		t.Fatal(err)
	}
	stdoutPath, stderrPath := filepath.Join(spoolDir, "stdout.log"), filepath.Join(spoolDir, "stderr.log")
	follow, err := lifecycle.FollowLogs(context.Background(), LogFollowRequest{Identity: identity, StdoutPath: stdoutPath, StderrPath: stderrPath})
	if err != nil {
		t.Fatal(err)
	}
	defer follow.Close()
	index := &gateSegmentIndex{}
	manager, err := stacklogs.NewManager(stacklogs.Config{DataDir: dataDir, Index: index, PollInterval: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	capture, err := manager.Start(context.Background(), stacklogs.CaptureRequest{
		Scope: stacklogs.Scope{
			SystemID: identity.SystemID, InstanceID: identity.InstanceID, ServiceID: domain.ServiceID("infrastructure"),
			ServiceInstanceID: domain.ServiceInstanceID("svi_01ARZ3NDEKTSV4RRFFQ69G5FAV"), OperationID: testOperationID,
		},
		Spools: map[stacklogs.Stream]string{stacklogs.StreamStdout: stdoutPath, stacklogs.StreamStderr: stderrPath},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer capture.Close()
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case entry := <-capture.Events():
			if strings.Contains(entry.Message, "p2b05-compose-log") {
				return entry
			}
		case <-deadline.C:
			t.Fatal("timed out waiting for unified Compose log entry")
		}
	}
}

func writeLogHealthComposeFixture(t *testing.T, root, image string) {
	t.Helper()
	dockerfile := "FROM scratch\nCOPY fixture /fixture\nENTRYPOINT [\"/fixture\",\"--mode\",\"secret-log\",\"--environment\",\"GATE_MESSAGE\"]\n"
	composeFile := fmt.Sprintf("services:\n  database:\n    image: %s\n    environment: {GATE_MESSAGE: p2b05-compose-log}\n    healthcheck:\n      test: [\"CMD\", \"/fixture\", \"-version\"]\n      interval: 1s\n      timeout: 1s\n      retries: 10\n", image)
	if err := os.WriteFile(filepath.Join(root, "Dockerfile"), []byte(dockerfile), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "compose.yaml"), []byte(composeFile), 0o600); err != nil {
		t.Fatal(err)
	}
}

func cleanupComposeGate(t *testing.T, docker, root, image, project string, request LifecycleRequest) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		environment := normalizedEnvironment(currentEnvironment())
		base := []string{"compose", "--project-name", project, "--file", request.ComposeFile, "--file", request.OverrideFile}
		if _, err := runCommand(ctx, docker, append(base, "down", "--volumes", "--remove-orphans"), root, environment); err != nil {
			t.Errorf("clean Compose Gate project: %v", err)
		}
		if _, err := runCommand(ctx, docker, []string{"image", "rm", "--force", image}, root, environment); err != nil {
			t.Errorf("clean Compose Gate image: %v", err)
		}
	})
}

type gateSegmentIndex struct {
	mutex    sync.Mutex
	segments []stacklogs.Segment
}

func (index *gateSegmentIndex) RegisterClosed(_ context.Context, segment stacklogs.Segment) error {
	index.mutex.Lock()
	defer index.mutex.Unlock()
	index.segments = append(index.segments, segment)
	return nil
}

func (*gateSegmentIndex) ListAfter(context.Context, domain.ServiceInstanceID, int64) ([]stacklogs.Segment, error) {
	return nil, nil
}

func (*gateSegmentIndex) SequenceBounds(context.Context, domain.ServiceInstanceID) (int64, int64, bool, error) {
	return 0, 0, false, nil
}

func buildLinuxFixture(t *testing.T, root string) {
	t.Helper()
	goExecutable, err := exec.LookPath("go.exe")
	if err != nil {
		t.Fatalf("resolve go.exe: %v", err)
	}
	repositoryRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(goExecutable, "build", "-o", filepath.Join(root, "fixture"), filepath.Join(repositoryRoot, "test", "fixtures", "process-fixture"))
	command.Dir = repositoryRoot
	command.Env = append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build Linux Compose fixture: %v (%s)", err, output)
	}
}

func writeLifecycleComposeFixture(t *testing.T, root, image string) {
	t.Helper()
	dockerfile := "FROM scratch\nCOPY fixture /fixture\nENTRYPOINT [\"/fixture\",\"--mode\",\"hold-port\",\"--port\",\"5432\"]\n"
	service := "    image: %s\n    healthcheck:\n      test: [\"CMD\", \"/fixture\", \"-version\"]\n      interval: 1s\n      timeout: 1s\n      retries: 10\n"
	composeFile := fmt.Sprintf("services:\n  database:\n"+service+"    volumes: [gate-data:/data]\n  web:\n"+service+"volumes:\n  gate-data: {}\n", image, image)
	if err := os.WriteFile(filepath.Join(root, "Dockerfile"), []byte(dockerfile), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "compose.yaml"), []byte(composeFile), 0o600); err != nil {
		t.Fatal(err)
	}
}

func resourceServices(observation ResourceObservation, expected ...string) bool {
	if len(observation.Containers) != len(expected) {
		return false
	}
	actual := make([]string, 0, len(observation.Containers))
	for _, container := range observation.Containers {
		actual = append(actual, container.ComposeService)
	}
	sort.Strings(actual)
	sort.Strings(expected)
	return reflect.DeepEqual(actual, expected)
}

func writeControlledBuildComposeFixture(t *testing.T, root, image string) {
	t.Helper()
	dockerfile := "FROM scratch\nCOPY fixture /fixture\nENTRYPOINT [\"/fixture\",\"--mode\",\"hold-port\",\"--port\",\"5432\"]\n"
	composeFile := fmt.Sprintf("services:\n  database:\n    image: %s\n    build:\n      context: .\n      dockerfile: Dockerfile\n    healthcheck:\n      test: [\"CMD\", \"/fixture\", \"-version\"]\n      interval: 1s\n      timeout: 1s\n      retries: 10\n", image)
	if err := os.WriteFile(filepath.Join(root, "Dockerfile"), []byte(dockerfile), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "compose.yaml"), []byte(composeFile), 0o600); err != nil {
		t.Fatal(err)
	}
}

func lifecycleIntegrationRequest(t *testing.T, root, dataDir string) LifecycleRequest {
	return lifecycleIntegrationRequestForServices(t, root, dataDir, []string{"database"})
}

func lifecycleIntegrationRequestForServices(t *testing.T, root, dataDir string, services []string) LifecycleRequest {
	t.Helper()
	port := availableLoopbackPort(t)
	overrideRequest := validOverrideRequest()
	overrideRequest.Services = append([]string(nil), services...)
	overrideRequest.Ports = map[string]PortOverride{"database": {Service: "database", Target: 5432, Published: port}}
	overrideRequest.Environment = map[string]map[string]string{"database": {"DATABASE_PORT": strconv.Itoa(port)}}
	generator, err := NewOverrideGenerator(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	override, err := generator.Generate(overrideRequest)
	if err != nil {
		t.Fatal(err)
	}
	return LifecycleRequest{
		WorkspaceRoot: root, DataDir: dataDir, ComposeFile: filepath.Join(root, "compose.yaml"), OverrideFile: override.Path,
		SystemID: overrideRequest.SystemID, WorkspaceID: overrideRequest.WorkspaceID, InstanceID: overrideRequest.InstanceID,
		Services: append([]string(nil), services...), StartTimeout: 45 * time.Second, StopTimeout: 5 * time.Second,
	}
}

func availableLoopbackPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func runGateCommand(t *testing.T, directory, executable string, arguments ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, executable, arguments...)
	command.Dir = directory
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("Gate command %q failed: %v (%s)", arguments, err, output)
	}
}

func lifecycleFixture(t *testing.T) (LifecycleRequest, string) {
	t.Helper()
	workspace, dataDir := t.TempDir(), t.TempDir()
	composeFile := filepath.Join(workspace, "compose.yaml")
	if err := os.WriteFile(composeFile, []byte("services:\n  database: {image: scratch}\n  web: {image: scratch}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	generator, err := NewOverrideGenerator(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	request := validOverrideRequest()
	override, err := generator.Generate(request)
	if err != nil {
		t.Fatal(err)
	}
	docker := filepath.Join(t.TempDir(), "docker.exe")
	if err := os.WriteFile(docker, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	return LifecycleRequest{
		WorkspaceRoot: workspace, DataDir: dataDir, ComposeFile: composeFile, OverrideFile: override.Path,
		SystemID: request.SystemID, WorkspaceID: request.WorkspaceID, InstanceID: request.InstanceID,
		Services: []string{"web", "database"}, Readiness: map[string]string{"database": "healthy", "web": "running"}, StartTimeout: 45 * time.Second, StopTimeout: 7 * time.Second,
	}, docker
}

func lifecyclePSOutput(t *testing.T, request LifecycleRequest) []byte {
	t.Helper()
	project, err := ProjectName(request.SystemID, request.WorkspaceID, request.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	value := `[{"ID":"db-id","Name":"` + project + `-database-1","Project":"` + project + `","Service":"database","State":"running","Health":"healthy","ExitCode":0},{"ID":"web-id","Name":"` + project + `-web-1","Project":"` + project + `","Service":"web","State":"running","Health":"","ExitCode":0}]`
	return []byte(strings.ReplaceAll(value, "\\\"", "\""))
}

func assertLifecycleCommands(t *testing.T, identity ProjectIdentity, commands [][]string) {
	t.Helper()
	base := []string{"compose", "--project-name", identity.ProjectName, "--file", identity.ComposeFile, "--file", identity.OverrideFile}
	want := [][]string{
		append(append([]string(nil), base...), "up", "-d", "--wait", "--no-deps", "--no-build", "--wait-timeout", "45", "database", "web"),
		append(append([]string(nil), base...), "ps", "--all", "--format", "json", "--no-trunc", "database", "web"),
		append(append([]string(nil), base...), "ps", "--all", "--format", "json", "--no-trunc", "database", "web"),
		append(append([]string(nil), base...), "stop", "--timeout", "7", "database", "web"),
	}
	if !reflect.DeepEqual(commands, want) {
		t.Fatalf("lifecycle commands = %#v, want %#v", commands, want)
	}
	for _, command := range commands {
		if containsArgument(command, "down") || containsArgument(command, "-v") || containsArgument(command, "--volumes") {
			t.Fatalf("destructive lifecycle command: %#v", command)
		}
	}
}

func containsArgument(arguments []string, value string) bool {
	for _, argument := range arguments {
		if argument == value {
			return true
		}
	}
	return false
}
