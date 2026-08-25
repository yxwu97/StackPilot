//go:build windows

package compose

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPreflightVerifiesVersionsDaemonConfigAndServices(t *testing.T) {
	request, docker := preflightFixture(t)
	commands := make([][]string, 0, 4)
	preflight, err := NewPreflighter(Config{
		DockerExecutable: docker,
		Environment:      map[string]string{"PATH": `C:\trusted`},
		Run: func(_ context.Context, executable string, arguments []string, directory string, environment map[string]string) (CommandOutput, error) {
			if executable != docker || directory != filepath.Dir(request.ComposeFile) || environment["PATH"] != `C:\trusted` {
				t.Fatalf("unsafe command inputs: %q %q %#v", executable, directory, environment)
			}
			commands = append(commands, append([]string(nil), arguments...))
			return successfulOutput(arguments), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := preflight.Preflight(context.Background(), request)
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	if result.DockerClientVersion != "29.5.3" || result.DockerServerVersion != "29.5.3" ||
		result.ComposeVersion != "5.1.4" || !reflect.DeepEqual(result.Services, []string{"cache", "database"}) {
		t.Fatalf("Preflight() = %#v", result)
	}
	wantConfig := []string{"compose", "--file", request.ComposeFile, "config", "--format", "json", "--no-interpolate"}
	if len(commands) != 4 || !reflect.DeepEqual(commands[3], wantConfig) {
		t.Fatalf("commands = %#v", commands)
	}
}

func TestPreflightClassifiesVersionFailures(t *testing.T) {
	tests := []struct {
		name   string
		mutate func([]string, CommandOutput) CommandOutput
		want   error
	}{
		{name: "Docker", want: ErrDockerVersionUnsupported, mutate: func(arguments []string, output CommandOutput) CommandOutput {
			if reflect.DeepEqual(arguments, []string{"--version"}) {
				output.Stdout = []byte("Docker version 23.0.9, build old")
			}
			return output
		}},
		{name: "Compose", want: ErrComposeVersionUnsupported, mutate: func(arguments []string, output CommandOutput) CommandOutput {
			if len(arguments) > 1 && arguments[0] == "compose" && arguments[1] == "version" {
				output.Stdout = []byte(`{"version":"v2.19.9"}`)
			}
			return output
		}},
		{name: "server", want: ErrDockerVersionUnsupported, mutate: func(arguments []string, output CommandOutput) CommandOutput {
			if len(arguments) > 0 && arguments[0] == "version" {
				output.Stdout = []byte(`{"Server":{"Version":"23.0.9"}}`)
			}
			return output
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, docker := preflightFixture(t)
			preflight, _ := NewPreflighter(Config{DockerExecutable: docker, Run: func(_ context.Context, _ string, arguments []string, _ string, _ map[string]string) (CommandOutput, error) {
				return test.mutate(arguments, successfulOutput(arguments)), nil
			}})
			if _, err := preflight.Preflight(context.Background(), request); !errors.Is(err, test.want) {
				t.Fatalf("Preflight() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestPreflightClassifiesUnavailableDaemonAndCompose(t *testing.T) {
	tests := []struct {
		name string
		fail func([]string) bool
		out  CommandOutput
		want error
	}{
		{name: "Compose plugin", fail: func(arguments []string) bool { return len(arguments) > 1 && arguments[1] == "version" }, want: ErrComposeNotFound},
		{name: "daemon", fail: func(arguments []string) bool { return len(arguments) > 0 && arguments[0] == "version" }, out: CommandOutput{Stdout: []byte(`{"Server":null}`)}, want: ErrDaemonUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, docker := preflightFixture(t)
			preflight, _ := NewPreflighter(Config{DockerExecutable: docker, StartDockerDesktop: func(context.Context) error { return ErrDaemonUnavailable }, Run: func(_ context.Context, _ string, arguments []string, _ string, _ map[string]string) (CommandOutput, error) {
				if test.fail(arguments) {
					return test.out, errors.New("safe fixture failure")
				}
				return successfulOutput(arguments), nil
			}})
			if _, err := preflight.Preflight(context.Background(), request); !errors.Is(err, test.want) {
				t.Fatalf("Preflight() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestPreflightStartsDockerDesktopAndWaitsForDaemon(t *testing.T) {
	request, docker := preflightFixture(t)
	started := false
	preflight, _ := NewPreflighter(Config{
		DockerExecutable: docker, DaemonPollInterval: time.Millisecond,
		StartDockerDesktop: func(context.Context) error { started = true; return nil },
		Run: func(_ context.Context, _ string, arguments []string, _ string, _ map[string]string) (CommandOutput, error) {
			if len(arguments) > 0 && arguments[0] == "version" && !started {
				return CommandOutput{Stdout: []byte(`{"Server":null}`)}, errors.New("daemon unavailable")
			}
			return successfulOutput(arguments), nil
		},
	})
	result, err := preflight.Preflight(context.Background(), request)
	if err != nil || !started || result.DockerServerVersion != "29.5.3" {
		t.Fatalf("Preflight() result = %#v, started = %t, error = %v", result, started, err)
	}
}

func TestConcurrentPreflightStartsDockerDesktopOnce(t *testing.T) {
	request, docker := preflightFixture(t)
	var daemonReady atomic.Bool
	var daemonChecks atomic.Int32
	var startCalls atomic.Int32
	var startSignalOnce sync.Once
	startEntered := make(chan struct{})
	secondRequestCheckedDaemon := make(chan struct{})
	preflight, _ := NewPreflighter(Config{
		DockerExecutable: docker, DaemonPollInterval: time.Millisecond,
		StartDockerDesktop: func(context.Context) error {
			startCalls.Add(1)
			startSignalOnce.Do(func() { close(startEntered) })
			<-secondRequestCheckedDaemon
			daemonReady.Store(true)
			return nil
		},
		Run: func(_ context.Context, _ string, arguments []string, _ string, _ map[string]string) (CommandOutput, error) {
			if len(arguments) > 0 && arguments[0] == "version" && !daemonReady.Load() {
				if daemonChecks.Add(1) == 3 {
					close(secondRequestCheckedDaemon)
				}
				return CommandOutput{Stdout: []byte(`{"Server":null}`)}, errors.New("daemon unavailable")
			}
			return successfulOutput(arguments), nil
		},
	})
	errorsByRequest := make(chan error, 2)
	go func() {
		_, err := preflight.Preflight(context.Background(), request)
		errorsByRequest <- err
	}()
	<-startEntered
	go func() {
		_, err := preflight.Preflight(context.Background(), request)
		errorsByRequest <- err
	}()
	for range 2 {
		if err := <-errorsByRequest; err != nil {
			t.Fatalf("Preflight() error = %v", err)
		}
	}
	if calls := startCalls.Load(); calls != 1 {
		t.Fatalf("StartDockerDesktop() calls = %d, want 1", calls)
	}
}

func TestResolveDockerDesktopUsesOnlyTrustedInstallRoots(t *testing.T) {
	tests := []struct {
		name         string
		environment  func(string) map[string]string
		relativePath string
	}{
		{
			name: "Program Files",
			environment: func(root string) map[string]string {
				return map[string]string{"PROGRAMFILES": root}
			},
			relativePath: filepath.Join("Docker", "Docker", "Docker Desktop.exe"),
		},
		{
			name: "local app data",
			environment: func(root string) map[string]string {
				return map[string]string{"LOCALAPPDATA": root}
			},
			relativePath: filepath.Join("Programs", "Docker", "Docker", "Docker Desktop.exe"),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			executable := filepath.Join(root, test.relativePath)
			if err := os.MkdirAll(filepath.Dir(executable), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(executable, []byte("fixture"), 0o600); err != nil {
				t.Fatal(err)
			}
			resolved, err := resolveDockerDesktop(test.environment(root))
			if err != nil || !strings.EqualFold(resolved, executable) {
				t.Fatalf("resolveDockerDesktop() = %q, %v; want %q", resolved, err, executable)
			}
		})
	}
	if _, err := resolveDockerDesktop(map[string]string{"PROGRAMFILES": t.TempDir()}); !errors.Is(err, ErrDaemonUnavailable) {
		t.Fatalf("resolveDockerDesktop(missing) error = %v", err)
	}
}

func TestPreflightReturnsUnavailableWhenDockerDesktopCannotStart(t *testing.T) {
	request, docker := preflightFixture(t)
	preflight, _ := NewPreflighter(Config{
		DockerExecutable:   docker,
		StartDockerDesktop: func(context.Context) error { return errors.New("safe fixture failure") },
		Run: func(_ context.Context, _ string, arguments []string, _ string, _ map[string]string) (CommandOutput, error) {
			if len(arguments) > 0 && arguments[0] == "version" {
				return CommandOutput{Stdout: []byte(`{"Server":null}`)}, errors.New("daemon unavailable")
			}
			return successfulOutput(arguments), nil
		},
	})
	if _, err := preflight.Preflight(context.Background(), request); !errors.Is(err, ErrDaemonUnavailable) {
		t.Fatalf("Preflight() error = %v, want %v", err, ErrDaemonUnavailable)
	}
}

func TestPreflightRejectsInvalidConfigServiceAndPath(t *testing.T) {
	request, docker := preflightFixture(t)
	preflight, _ := NewPreflighter(Config{DockerExecutable: docker, Run: func(_ context.Context, _ string, arguments []string, _ string, _ map[string]string) (CommandOutput, error) {
		output := successfulOutput(arguments)
		if len(arguments) > 2 && arguments[0] == "compose" && arguments[1] == "--file" {
			output.Stdout = []byte(`{"services":{"other":{}}}`)
		}
		return output, nil
	}})
	if _, err := preflight.Preflight(context.Background(), request); !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("Preflight(missing service) error = %v", err)
	}

	request.ComposeFile = filepath.Join(t.TempDir(), "compose.yaml")
	if err := os.WriteFile(request.ComposeFile, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := preflight.Preflight(context.Background(), request); !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("Preflight(escaped config) error = %v", err)
	}
}

func TestPreflightHonorsTimeoutWithoutExposingOutput(t *testing.T) {
	request, docker := preflightFixture(t)
	preflight, _ := NewPreflighter(Config{DockerExecutable: docker, Timeout: 10 * time.Millisecond, Run: func(ctx context.Context, _ string, _ []string, _ string, _ map[string]string) (CommandOutput, error) {
		<-ctx.Done()
		return CommandOutput{Stderr: []byte("sensitive daemon detail")}, ctx.Err()
	}})
	_, err := preflight.Preflight(context.Background(), request)
	if !errors.Is(err, ErrPreflightTimeout) || strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("Preflight(timeout) error = %v", err)
	}
}

func TestPreflightRejectsUnsafeManagedServiceConfiguration(t *testing.T) {
	tests := []struct {
		name   string
		config string
		want   error
	}{
		{name: "privileged", config: `{"services":{"database":{"privileged":true},"cache":{}}}`, want: ErrConfigInvalid},
		{name: "entrypoint", config: `{"services":{"database":{"entrypoint":["unsafe"]},"cache":{}}}`, want: ErrConfigInvalid},
		{name: "build", config: `{"services":{"database":{"build":{"context":"."}},"cache":{}}}`, want: ErrBuildConfigInvalid},
		{name: "host root", config: `{"services":{"database":{"volumes":[{"type":"bind","source":"C:\\\\","target":"/host"}]},"cache":{}}}`, want: ErrConfigInvalid},
		{name: "unmanaged dependency", config: `{"services":{"database":{"depends_on":{"hidden":{"condition":"service_started"}}},"cache":{},"hidden":{}}}`, want: ErrConfigInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, docker := preflightFixture(t)
			preflight, _ := NewPreflighter(Config{DockerExecutable: docker, Run: func(_ context.Context, _ string, arguments []string, _ string, _ map[string]string) (CommandOutput, error) {
				output := successfulOutput(arguments)
				if len(arguments) > 2 && arguments[0] == "compose" && arguments[1] == "--file" {
					output.Stdout = []byte(test.config)
				}
				return output, nil
			}})
			if _, err := preflight.Preflight(context.Background(), request); !errors.Is(err, test.want) {
				t.Fatalf("Preflight() error = %v", err)
			}
		})
	}
}

func TestPreflightAllowsFixedBaseComposeCommand(t *testing.T) {
	request, docker := preflightFixture(t)
	preflight, _ := NewPreflighter(Config{DockerExecutable: docker, Run: func(_ context.Context, _ string, arguments []string, _ string, _ map[string]string) (CommandOutput, error) {
		output := successfulOutput(arguments)
		if len(arguments) > 2 && arguments[0] == "compose" && arguments[1] == "--file" {
			output.Stdout = []byte(`{"services":{"database":{"command":["server","/data"]},"cache":{}}}`)
		}
		return output, nil
	}})
	if _, err := preflight.Preflight(context.Background(), request); err != nil {
		t.Fatalf("Preflight(base command) error = %v", err)
	}
}

func TestPreflightAcceptsOnlyControlledLocalBuildAndReadiness(t *testing.T) {
	request, docker := preflightFixture(t)
	contextDir := filepath.Join(request.WorkspaceRoot, "database")
	if err := os.Mkdir(contextDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(contextDir, "Dockerfile"), []byte("FROM scratch\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	request.BuildPolicy = "always"
	request.Readiness = map[string]string{"database": "healthy", "cache": "running"}
	config := `{"services":{"database":{"build":{"context":"` + strings.ReplaceAll(contextDir, `\`, `\\`) + `","dockerfile":"Dockerfile"},"healthcheck":{"test":["CMD","true"]}},"cache":{}}}`
	preflight, _ := NewPreflighter(Config{DockerExecutable: docker, Run: func(_ context.Context, _ string, arguments []string, _ string, _ map[string]string) (CommandOutput, error) {
		output := successfulOutput(arguments)
		if len(arguments) > 2 && arguments[0] == "compose" && arguments[1] == "--file" {
			output.Stdout = []byte(config)
		}
		return output, nil
	}})
	result, err := preflight.Preflight(context.Background(), request)
	if err != nil || !reflect.DeepEqual(result.BuildServices, []string{"database"}) {
		t.Fatalf("Preflight(controlled build) = %#v, %v", result, err)
	}
	unsafe := strings.Replace(config, `"dockerfile":"Dockerfile"`, `"dockerfile":"Dockerfile","args":{"TOKEN":"value"}`, 1)
	preflight.run = func(_ context.Context, _ string, arguments []string, _ string, _ map[string]string) (CommandOutput, error) {
		output := successfulOutput(arguments)
		if len(arguments) > 2 && arguments[0] == "compose" && arguments[1] == "--file" {
			output.Stdout = []byte(unsafe)
		}
		return output, nil
	}
	if _, err := preflight.Preflight(context.Background(), request); !errors.Is(err, ErrBuildConfigInvalid) {
		t.Fatalf("Preflight(build args) error = %v", err)
	}
}

func TestInstalledComposePreflight(t *testing.T) {
	if os.Getenv("STACKPILOT_COMPOSE_INTEGRATION") != "1" {
		t.Skip("set STACKPILOT_COMPOSE_INTEGRATION=1 for the installed Docker toolchain")
	}
	request, _ := preflightFixture(t)
	config := Config{}
	if os.Getenv("STACKPILOT_COMPOSE_EXPECT_DAEMON_UNAVAILABLE") == "1" {
		config.StartDockerDesktop = func(context.Context) error { return ErrDaemonUnavailable }
	}
	preflight, err := NewPreflighter(config)
	if err != nil {
		t.Fatal(err)
	}
	_, err = preflight.Preflight(context.Background(), request)
	if os.Getenv("STACKPILOT_COMPOSE_EXPECT_DAEMON_UNAVAILABLE") == "1" {
		if !errors.Is(err, ErrDaemonUnavailable) {
			t.Fatalf("Preflight(installed daemon denial) error = %v", err)
		}
		return
	}
	if err != nil {
		t.Fatalf("Preflight(installed) error = %v", err)
	}
}

func preflightFixture(t *testing.T) (PreflightRequest, string) {
	t.Helper()
	root := t.TempDir()
	composeFile := filepath.Join(root, "compose.yaml")
	if err := os.WriteFile(composeFile, []byte("services:\n  database:\n    image: scratch\n  cache:\n    image: scratch\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	docker := filepath.Join(t.TempDir(), "docker.exe")
	if err := os.WriteFile(docker, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	return PreflightRequest{WorkspaceRoot: root, ComposeFile: composeFile, Services: []string{"database", "cache"}, Readiness: map[string]string{"database": "running", "cache": "running"}}, docker
}

func successfulOutput(arguments []string) CommandOutput {
	switch {
	case reflect.DeepEqual(arguments, []string{"--version"}):
		return CommandOutput{Stdout: []byte("Docker version 29.5.3, build fixture")}
	case len(arguments) > 1 && arguments[0] == "compose" && arguments[1] == "version":
		return CommandOutput{Stdout: []byte(`{"version":"v5.1.4"}`)}
	case len(arguments) > 0 && arguments[0] == "version":
		return CommandOutput{Stdout: []byte(`{"Server":{"Version":"29.5.3"}}`)}
	default:
		return CommandOutput{Stdout: []byte(`{"services":{"database":{},"cache":{}}}`)}
	}
}
