//go:build windows

package runner

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestResolvePrefersWorkspaceMavenWrapperAndHashesIt(t *testing.T) {
	root := t.TempDir()
	working := filepath.Join(root, "backend")
	if err := os.MkdirAll(working, 0o700); err != nil {
		t.Fatalf("create working directory: %v", err)
	}
	contents := []byte("@echo off\r\necho Apache Maven 9.9.9\r\n")
	wrapper := filepath.Join(working, "mvnw.cmd")
	if err := os.WriteFile(wrapper, contents, 0o600); err != nil {
		t.Fatalf("write Maven Wrapper: %v", err)
	}
	fallback := writeRunnerFixture(t, filepath.Join(t.TempDir(), "mvn.cmd"), "fallback")
	resolver := newFakeProbeResolver(t, Config{Environment: map[string]string{
		"PATH": filepath.Dir(fallback), "COMSPEC": os.Getenv("COMSPEC"),
	}}, "Apache Maven 9.9.9")

	resolved, err := resolver.Resolve(context.Background(), ResolveRequest{
		Runner: Maven, WorkspaceRoot: root, WorkingDirectory: working,
	})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	wantDigest := fmt.Sprintf("%x", sha256.Sum256(contents))
	if resolved.Executable != wrapper || resolved.ResolutionKind != ResolutionWorkspace ||
		resolved.Version != "9.9.9" || resolved.ExecutableDigest != wantDigest {
		t.Fatalf("Resolve() = %#v", resolved)
	}
}

func TestResolveUsesJavaHomeBeforePath(t *testing.T) {
	root, working := runnerDirectories(t)
	javaHome := t.TempDir()
	homeJava := writeRunnerFixture(t, filepath.Join(javaHome, "bin", "java.exe"), "home")
	pathJava := writeRunnerFixture(t, filepath.Join(t.TempDir(), "java.exe"), "path")
	resolver := newFakeProbeResolver(t, Config{Environment: map[string]string{
		"JAVA_HOME": javaHome, "PATH": filepath.Dir(pathJava),
	}}, `openjdk version "21.0.10" 2026-01-01`)

	resolved, err := resolver.Resolve(context.Background(), ResolveRequest{
		Runner: Java, WorkspaceRoot: root, WorkingDirectory: working,
	})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolved.Executable != homeJava || resolved.ResolutionKind != ResolutionPath || resolved.Version != "21.0.10" {
		t.Fatalf("Resolve() = %#v", resolved)
	}
}

func TestResolveNPMUsesPathAndIgnoresNodeModulesBin(t *testing.T) {
	root, working := runnerDirectories(t)
	writeRunnerFixture(t, filepath.Join(working, "node_modules", ".bin", "npm.cmd"), "workspace")
	pathNPM := writeRunnerFixture(t, filepath.Join(t.TempDir(), "npm.cmd"), "path")
	resolver := newFakeProbeResolver(t, Config{Environment: map[string]string{
		"PATH": filepath.Dir(pathNPM), "COMSPEC": os.Getenv("COMSPEC"),
	}}, "11.12.1")

	resolved, err := resolver.Resolve(context.Background(), ResolveRequest{
		Runner: NPM, WorkspaceRoot: root, WorkingDirectory: working,
	})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolved.Executable != pathNPM || resolved.ResolutionKind != ResolutionPath {
		t.Fatalf("Resolve() = %#v", resolved)
	}
}

func TestResolveNodeUsesTrustedPathWithoutShell(t *testing.T) {
	root, working := runnerDirectories(t)
	pathNode := writeRunnerFixture(t, filepath.Join(t.TempDir(), "node.exe"), "node fixture")
	resolver := newFakeProbeResolver(t, Config{Environment: map[string]string{"PATH": filepath.Dir(pathNode)}}, "v24.8.0")
	resolved, err := resolver.Resolve(context.Background(), ResolveRequest{Runner: Node, WorkspaceRoot: root, WorkingDirectory: working})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolved.Executable != pathNode || resolved.ResolutionKind != ResolutionPath || resolved.Version != "24.8.0" || len(resolved.ArgsPrefix) != 0 {
		t.Fatalf("Resolve() = %#v", resolved)
	}
}

func TestResolveGoUsesTrustedPathWithoutShell(t *testing.T) {
	root, working := runnerDirectories(t)
	pathGo := writeRunnerFixture(t, filepath.Join(t.TempDir(), "go.exe"), "go fixture")
	resolver := newFakeProbeResolver(t, Config{Environment: map[string]string{"PATH": filepath.Dir(pathGo)}}, "go version go1.26.6 windows/amd64")
	resolved, err := resolver.Resolve(context.Background(), ResolveRequest{Runner: Go, WorkspaceRoot: root, WorkingDirectory: working})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolved.Executable != pathGo || resolved.ResolutionKind != ResolutionPath || resolved.Version != "1.26.6" || len(resolved.ArgsPrefix) != 0 {
		t.Fatalf("Resolve() = %#v", resolved)
	}
}

func TestResolvePythonVenvUsesWorkspaceInterpreter(t *testing.T) {
	root, working := runnerDirectories(t)
	venv := filepath.Join(root, ".venv")
	contents := "python fixture"
	python := writeRunnerFixture(t, filepath.Join(venv, "Scripts", "python.exe"), contents)
	resolver := newFakeProbeResolver(t, Config{Environment: map[string]string{}}, "Python 3.14.3")

	resolved, err := resolver.Resolve(context.Background(), ResolveRequest{
		Runner: PythonVenv, WorkspaceRoot: root, WorkingDirectory: working, VirtualEnvironment: venv,
	})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	wantDigest := fmt.Sprintf("%x", sha256.Sum256([]byte(contents)))
	if resolved.Executable != python || resolved.ResolutionKind != ResolutionVenv ||
		resolved.Version != "3.14.3" || resolved.ExecutableDigest != wantDigest {
		t.Fatalf("Resolve() = %#v", resolved)
	}
}

func TestResolveExplicitExecutableRequiresTrustedRoot(t *testing.T) {
	root, working := runnerDirectories(t)
	outsideRoot := t.TempDir()
	executable := writeRunnerFixture(t, filepath.Join(outsideRoot, "java.exe"), "explicit")
	request := ResolveRequest{Runner: Java, WorkspaceRoot: root, WorkingDirectory: working}
	unsafeResolver := newFakeProbeResolver(t, Config{
		Environment: map[string]string{}, ExplicitExecutables: map[Kind]string{Java: executable},
	}, `openjdk version "21"`)
	if _, err := unsafeResolver.Resolve(context.Background(), request); !errors.Is(err, ErrRunnerPathUnsafe) {
		t.Fatalf("unsafe Resolve() error = %v, want ErrRunnerPathUnsafe", err)
	}

	trustedResolver := newFakeProbeResolver(t, Config{
		Environment: map[string]string{}, ExplicitExecutables: map[Kind]string{Java: executable},
		AllowedToolRoots: []string{outsideRoot},
	}, `openjdk version "21"`)
	resolved, err := trustedResolver.Resolve(context.Background(), request)
	if err != nil || resolved.ResolutionKind != ResolutionExplicit || resolved.Executable != executable {
		t.Fatalf("trusted Resolve() = (%#v, %v)", resolved, err)
	}
}

func TestResolveRejectsUnsupportedAndUnsafeRequests(t *testing.T) {
	root, working := runnerDirectories(t)
	resolver := newFakeProbeResolver(t, Config{Environment: map[string]string{}}, "")
	tests := []struct {
		request ResolveRequest
		want    error
	}{
		{request: ResolveRequest{Runner: PythonVenv, WorkspaceRoot: root, WorkingDirectory: working}, want: ErrRunnerPathUnsafe},
		{request: ResolveRequest{Runner: Java, WorkspaceRoot: root, WorkingDirectory: working, VirtualEnvironment: root}, want: ErrRunnerPathUnsafe},
		{request: ResolveRequest{Runner: Kind("shell"), WorkspaceRoot: root, WorkingDirectory: working}, want: ErrRunnerUnsupported},
		{request: ResolveRequest{Runner: Java, WorkspaceRoot: root, WorkingDirectory: t.TempDir()}, want: ErrRunnerPathUnsafe},
	}
	for _, test := range tests {
		if _, err := resolver.Resolve(context.Background(), test.request); !errors.Is(err, test.want) {
			t.Errorf("Resolve(%s) error = %v, want %v", test.request.Runner, err, test.want)
		}
	}
}

func TestResolvePythonVenvRejectsMissingAndEscapedInterpreter(t *testing.T) {
	root, working := runnerDirectories(t)
	resolver := newFakeProbeResolver(t, Config{Environment: map[string]string{}}, "Python 3.14.3")
	missing := filepath.Join(root, ".missing")
	_, err := resolver.Resolve(context.Background(), ResolveRequest{
		Runner: PythonVenv, WorkspaceRoot: root, WorkingDirectory: working, VirtualEnvironment: missing,
	})
	if !errors.Is(err, ErrRunnerNotFound) {
		t.Fatalf("Resolve(missing venv) error = %v, want ErrRunnerNotFound", err)
	}

	outside := t.TempDir()
	writeRunnerFixture(t, filepath.Join(outside, "Scripts", "python.exe"), "outside")
	_, err = resolver.Resolve(context.Background(), ResolveRequest{
		Runner: PythonVenv, WorkspaceRoot: root, WorkingDirectory: working, VirtualEnvironment: outside,
	})
	if !errors.Is(err, ErrRunnerPathUnsafe) {
		t.Fatalf("Resolve(escaped venv) error = %v, want ErrRunnerPathUnsafe", err)
	}
}

func TestResolvePythonVenvRejectsJunctionEscapes(t *testing.T) {
	root, working := runnerDirectories(t)
	resolver := newFakeProbeResolver(t, Config{Environment: map[string]string{}}, "Python 3.14.3")
	outsideVenv := t.TempDir()
	writeRunnerFixture(t, filepath.Join(outsideVenv, "Scripts", "python.exe"), "outside")
	linkedVenv := filepath.Join(root, ".linked-venv")
	createJunction(t, linkedVenv, outsideVenv)
	_, err := resolver.Resolve(context.Background(), ResolveRequest{
		Runner: PythonVenv, WorkspaceRoot: root, WorkingDirectory: working, VirtualEnvironment: linkedVenv,
	})
	if !errors.Is(err, ErrRunnerPathUnsafe) {
		t.Fatalf("Resolve(venv junction) error = %v, want ErrRunnerPathUnsafe", err)
	}

	venv := filepath.Join(root, ".venv")
	if err := os.Mkdir(venv, 0o700); err != nil {
		t.Fatalf("create virtual environment root: %v", err)
	}
	outsideScripts := filepath.Join(t.TempDir(), "Scripts")
	writeRunnerFixture(t, filepath.Join(outsideScripts, "python.exe"), "outside")
	createJunction(t, filepath.Join(venv, "Scripts"), outsideScripts)
	_, err = resolver.Resolve(context.Background(), ResolveRequest{
		Runner: PythonVenv, WorkspaceRoot: root, WorkingDirectory: working, VirtualEnvironment: venv,
	})
	if !errors.Is(err, ErrRunnerPathUnsafe) {
		t.Fatalf("Resolve(Scripts junction) error = %v, want ErrRunnerPathUnsafe", err)
	}
}

func createJunction(t *testing.T, link, target string) {
	t.Helper()
	command := exec.Command("cmd.exe", "/d", "/s", "/c", "mklink", "/J", link, target)
	if output, err := command.CombinedOutput(); err != nil {
		t.Skipf("directory junctions are unavailable: %v: %s", err, output)
	}
}

func TestResolveClassifiesProbeTimeout(t *testing.T) {
	root, working := runnerDirectories(t)
	java := writeRunnerFixture(t, filepath.Join(t.TempDir(), "java.exe"), "java")
	resolver, err := NewResolver(Config{
		Environment: map[string]string{"PATH": filepath.Dir(java)}, ProbeTimeout: 20 * time.Millisecond,
		Probe: func(ctx context.Context, _ Kind, _ string, _ map[string]string) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		},
	})
	if err != nil {
		t.Fatalf("NewResolver() error = %v", err)
	}
	_, err = resolver.Resolve(context.Background(), ResolveRequest{Runner: Java, WorkspaceRoot: root, WorkingDirectory: working})
	if !errors.Is(err, ErrVersionProbeTimeout) {
		t.Fatalf("Resolve() error = %v, want ErrVersionProbeTimeout", err)
	}
}

func TestResolveRejectsMavenWrapperJunctionEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeRunnerFixture(t, filepath.Join(outside, "mvnw.cmd"), "outside")
	linked := filepath.Join(root, "backend")
	command := exec.Command("cmd.exe", "/d", "/s", "/c", "mklink", "/J", linked, outside)
	if output, err := command.CombinedOutput(); err != nil {
		t.Skipf("directory junctions are unavailable: %v: %s", err, output)
	}
	resolver := newFakeProbeResolver(t, Config{Environment: map[string]string{}}, "Apache Maven 1.0.0")
	_, err := resolver.Resolve(context.Background(), ResolveRequest{Runner: Maven, WorkspaceRoot: root, WorkingDirectory: linked})
	if !errors.Is(err, ErrRunnerPathUnsafe) {
		t.Fatalf("Resolve(junction escape) error = %v, want ErrRunnerPathUnsafe", err)
	}
}

func TestDefaultProbeRunsCmdWrapperInUnicodePath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspace with spaces 中文")
	working := filepath.Join(root, "backend service")
	if err := os.MkdirAll(working, 0o700); err != nil {
		t.Fatalf("create Unicode working directory: %v", err)
	}
	writeRunnerFixture(t, filepath.Join(working, "mvnw.cmd"), "@echo Apache Maven 9.8.7")
	resolver, err := NewResolver(Config{Environment: currentEnvironment()})
	if err != nil {
		t.Fatalf("NewResolver() error = %v", err)
	}
	resolved, err := resolver.Resolve(context.Background(), ResolveRequest{
		Runner: Maven, WorkspaceRoot: root, WorkingDirectory: working,
	})
	if err != nil || resolved.Version != "9.8.7" {
		t.Fatalf("Resolve(real cmd probe) = (%#v, %v)", resolved, err)
	}
}

func TestCmdInvocationPreservesArguments(t *testing.T) {
	if os.Getenv("STACKPILOT_CMD_ARGUMENT_HELPER") == "1" {
		writeArgumentHelperResult()
		return
	}
	directory := filepath.Join(t.TempDir(), "wrapper path 中文")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatalf("create wrapper directory: %v", err)
	}
	wrapper := filepath.Join(directory, "forward.cmd")
	contents := "@echo off\r\n\"%STACKPILOT_TEST_EXE%\" -test.run=TestCmdInvocationPreservesArguments -- %*\r\n"
	if err := os.WriteFile(wrapper, []byte(contents), 0o600); err != nil {
		t.Fatalf("write forwarding wrapper: %v", err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("find test executable: %v", err)
	}
	want := []string{"plain", "with space", "中文目录", `has"quote`, `trailing\`, `two\\`, `%PATH%`, `meta&value`}
	environment := normalizedEnvironment(currentEnvironment())
	environment["STACKPILOT_CMD_ARGUMENT_HELPER"] = "1"
	environment["STACKPILOT_TEST_EXE"] = executable
	command, err := versionCommand(context.Background(), wrapper, want, environment)
	if err != nil {
		t.Fatalf("versionCommand() error = %v", err)
	}
	command.Env = environmentList(environment)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("cmd invocation error = %v: %s", err, output)
	}
	var got []string
	if err := json.Unmarshal(bytes.TrimSpace(output), &got); err != nil {
		t.Fatalf("decode helper output %q: %v", output, err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cmd arguments = %#v, want %#v", got, want)
	}
}

func TestBuildCmdCommandLineRejectsRelativePaths(t *testing.T) {
	if _, err := BuildCmdCommandLine("cmd.exe", `C:\tools\mvn.cmd`, nil); err == nil {
		t.Fatal("relative COMSPEC unexpectedly accepted")
	}
	if _, err := BuildCmdCommandLine(`C:\Windows\System32\cmd.exe`, `tools\mvn.cmd`, nil); err == nil {
		t.Fatal("relative runner unexpectedly accepted")
	}
}

func writeArgumentHelperResult() {
	separator := -1
	for index, argument := range os.Args {
		if argument == "--" {
			separator = index
			break
		}
	}
	if separator < 0 {
		os.Exit(2)
	}
	_ = json.NewEncoder(os.Stdout).Encode(os.Args[separator+1:])
	os.Exit(0)
}

func newFakeProbeResolver(t *testing.T, config Config, output string) *Resolver {
	t.Helper()
	config.Probe = func(_ context.Context, _ Kind, _ string, _ map[string]string) (string, error) {
		return output, nil
	}
	resolver, err := NewResolver(config)
	if err != nil {
		t.Fatalf("NewResolver() error = %v", err)
	}
	return resolver
}

func runnerDirectories(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	working := filepath.Join(root, "service")
	if err := os.MkdirAll(working, 0o700); err != nil {
		t.Fatalf("create runner working directory: %v", err)
	}
	return root, working
}

func writeRunnerFixture(t *testing.T, path, contents string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create runner fixture directory: %v", err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write runner fixture: %v", err)
	}
	canonical, err := filepath.Abs(path)
	if err != nil {
		t.Fatalf("resolve runner fixture: %v", err)
	}
	return canonical
}

func TestParseVersionOutput(t *testing.T) {
	tests := []struct {
		kind   Kind
		output string
		want   string
	}{
		{kind: Maven, output: "Apache Maven 3.9.14 (revision)\r\nMaven home: hidden", want: "3.9.14"},
		{kind: NPM, output: "11.12.1\r\n", want: "11.12.1"},
		{kind: Java, output: `openjdk version "21.0.10" 2026-01-01`, want: "21.0.10"},
		{kind: Go, output: "go version go1.26.6 windows/amd64\r\n", want: "1.26.6"},
		{kind: PythonVenv, output: "Python 3.14.3\r\n", want: "3.14.3"},
	}
	for _, test := range tests {
		if got, err := parseVersion(test.kind, test.output); err != nil || got != test.want {
			t.Errorf("parseVersion(%s) = (%q, %v), want %q", test.kind, got, err, test.want)
		}
	}
	if _, err := parseVersion(Maven, strings.Repeat("unrecognized", 3)); !errors.Is(err, ErrVersionProbeFailed) {
		t.Fatalf("parseVersion(invalid) error = %v", err)
	}
}
