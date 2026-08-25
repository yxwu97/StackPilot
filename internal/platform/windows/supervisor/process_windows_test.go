//go:build windows

package supervisor

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const fixtureArgument = "--stackpilot-supervisor-fixture"

func TestManagedServiceFixture(t *testing.T) {
	index := fixtureArgumentIndex(os.Args)
	if index < 0 {
		return
	}
	if err := runManagedServiceFixture(os.Args[index+1:]); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "fixture error: %v\n", err)
		os.Exit(20)
	}
	os.Exit(0)
}

func TestManagedServiceLifecycleAndTreeTermination(t *testing.T) {
	instanceDir := t.TempDir()
	serviceDir := filepath.Join(instanceDir, "services", "backend")
	if err := os.MkdirAll(serviceDir, 0o700); err != nil {
		t.Fatalf("create service directory: %v", err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	childPIDPath := filepath.Join(serviceDir, "child.pid")
	request := fixtureStartRequest(executable, instanceDir, childPIDPath)
	service, err := startManagedService(instanceDir, request)
	if err != nil {
		t.Fatalf("startManagedService() error = %v", err)
	}
	closed := false
	defer func() {
		if !closed {
			_, _ = service.stop(0, nil)
			_ = service.close()
		}
	}()

	childPID := waitForFixturePID(t, childPIDPath)
	assertFixtureSpool(t, childPIDPath+".visible", "false")
	childHandle := openFixtureProcess(t, childPID)
	defer windows.CloseHandle(childHandle)
	assertFixtureSpool(t, request.StdoutPath, "identity-before-resume=true")
	assertFixtureSpool(t, request.StdoutPath, "[REDACTED:secret]")
	assertFixtureSpoolExcludes(t, request.StdoutPath, "supervisor-plaintext-secret")
	assertFixtureSpool(t, request.StderrPath, "fixture-stderr")
	assertRunningService(t, service)

	originalIdentity := service.identity
	service.identity.CreatedAt = service.identity.CreatedAt.Add(time.Nanosecond)
	if _, err := service.stop(0, nil); err == nil {
		t.Fatal("identity mismatch unexpectedly allowed service termination")
	}
	service.identity = originalIdentity
	status, err := service.stop(0, nil)
	if err != nil {
		t.Fatalf("stop() error = %v", err)
	}
	if status.State != "exited" || !status.Forced {
		t.Fatalf("stop() status = %#v, want forced exited", status)
	}
	assertProcessExited(t, childHandle)
	if err := service.close(); err != nil {
		t.Fatalf("close() error = %v", err)
	}
	closed = true
}

func TestSpoolPathsRejectEscapesAndAlternateStreams(t *testing.T) {
	instanceDir := t.TempDir()
	inside := filepath.Join(instanceDir, "logs")
	if err := os.MkdirAll(inside, 0o700); err != nil {
		t.Fatalf("create spool directory: %v", err)
	}
	outside := t.TempDir()
	if _, err := openSpoolFile(instanceDir, filepath.Join(outside, "outside.log")); err == nil {
		t.Fatal("outside spool path unexpectedly accepted")
	}
	if _, err := openSpoolFile(instanceDir, filepath.Join(inside, "stdout.log:stream")); err == nil {
		t.Fatal("alternate data stream unexpectedly accepted")
	}
	junction := filepath.Join(instanceDir, "junction")
	command := exec.Command("cmd.exe", "/d", "/s", "/c", "mklink", "/J", junction, outside)
	if output, err := command.CombinedOutput(); err != nil {
		t.Skipf("directory junctions are unavailable: %v (%s)", err, output)
	}
	t.Cleanup(func() { _ = os.Remove(junction) })
	if _, err := openSpoolFile(instanceDir, filepath.Join(junction, "escaped.log")); err == nil {
		t.Fatal("junction spool escape unexpectedly accepted")
	}
}

func TestEmptyEnvironmentBlockIsDoubleTerminated(t *testing.T) {
	block, err := environmentBlock(map[string]string{})
	if err != nil {
		t.Fatalf("environmentBlock() error = %v", err)
	}
	values := unsafe.Slice(block, 2)
	if values[0] != 0 || values[1] != 0 {
		t.Fatalf("empty environment terminator = %v, want [0 0]", values)
	}
}

func fixtureStartRequest(executable, instanceDir, childPIDPath string) StartServiceRequest {
	serviceDir := filepath.Join(instanceDir, "services", "backend")
	identityPath := filepath.Join(serviceDir, "identity.json")
	return StartServiceRequest{
		ServiceID: "backend", Executable: executable,
		Arguments: []string{
			"-test.run=TestManagedServiceFixture", "--", fixtureArgument, "tree", identityPath,
			childPIDPath, childPIDPath + ".graceful", childPIDPath + ".visible",
		},
		WorkingDirectory:       instanceDir,
		Environment:            map[string]string{"STACKPILOT_FIXTURE_SECRET": "supervisor-plaintext-secret"},
		SecretEnvironmentNames: []string{"STACKPILOT_FIXTURE_SECRET"}, CommandDigest: strings.Repeat("a", 64),
		StdoutPath: filepath.Join(serviceDir, "stdout.spool"), StderrPath: filepath.Join(serviceDir, "stderr.spool"),
	}
}

func fixtureArgumentIndex(arguments []string) int {
	for index, argument := range arguments {
		if argument == fixtureArgument {
			return index
		}
	}
	return -1
}

func runManagedServiceFixture(arguments []string) error {
	if len(arguments) == 0 {
		return fmt.Errorf("missing fixture mode")
	}
	if (arguments[0] == "leaf" || arguments[0] == "leaf-ignore") && len(arguments) == 2 {
		if arguments[0] == "leaf-ignore" {
			if err := installIgnoringConsoleHandler(); err != nil {
				return err
			}
		} else {
			interrupt := make(chan os.Signal, 1)
			signal.Notify(interrupt, os.Interrupt)
			defer signal.Stop(interrupt)
			if err := os.WriteFile(arguments[1], []byte("ready"), 0o600); err != nil {
				return err
			}
			<-interrupt
			return nil
		}
		if err := os.WriteFile(arguments[1], []byte("ready"), 0o600); err != nil {
			return err
		}
		for {
			time.Sleep(time.Hour)
		}
	}
	if len(arguments) != 5 || (arguments[0] != "tree" && arguments[0] != "ignore") {
		return fmt.Errorf("invalid fixture arguments")
	}
	if _, err := os.Stat(arguments[1]); err != nil {
		return fmt.Errorf("identity was unavailable before resume: %w", err)
	}
	_, _ = fmt.Fprintln(os.Stdout, "identity-before-resume=true")
	_, _ = fmt.Fprintln(os.Stdout, os.Getenv("STACKPILOT_FIXTURE_SECRET"))
	_, _ = fmt.Fprintln(os.Stderr, "fixture-stderr")
	if err := os.WriteFile(arguments[4], []byte(strconv.FormatBool(consoleWindowVisible())), 0o600); err != nil {
		return fmt.Errorf("publish console visibility: %w", err)
	}
	leafMode := "leaf"
	var interrupt chan os.Signal
	if arguments[0] == "ignore" {
		leafMode = "leaf-ignore"
		if err := installIgnoringConsoleHandler(); err != nil {
			return err
		}
	} else {
		interrupt = make(chan os.Signal, 1)
		signal.Notify(interrupt, os.Interrupt)
		defer signal.Stop(interrupt)
	}
	childReadyPath := arguments[2] + ".child-ready"
	child := exec.Command(os.Args[0], "-test.run=TestManagedServiceFixture", "--", fixtureArgument, leafMode, childReadyPath)
	if err := child.Start(); err != nil {
		return fmt.Errorf("start fixture child: %w", err)
	}
	if err := waitForFixtureReady(childReadyPath, 5*time.Second); err != nil {
		_ = child.Process.Kill()
		return err
	}
	if err := os.WriteFile(arguments[2], []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
		_ = child.Process.Kill()
		return fmt.Errorf("publish fixture child PID: %w", err)
	}
	if arguments[0] == "ignore" {
		for {
			time.Sleep(time.Hour)
		}
	}
	waited := make(chan error, 1)
	go func() { waited <- child.Wait() }()
	select {
	case err := <-waited:
		return fmt.Errorf("fixture child exited before graceful signal: %w", err)
	case <-interrupt:
		if err := os.WriteFile(arguments[3], []byte("received"), 0o600); err != nil {
			return fmt.Errorf("publish graceful signal: %w", err)
		}
	}
	select {
	case err := <-waited:
		return err
	case <-time.After(5 * time.Second):
		return fmt.Errorf("fixture child did not handle graceful signal")
	}
}

func TestOutputRedactionHandlesSecretAcrossReadBoundaries(t *testing.T) {
	secret := []byte("boundary-secret")
	var output bytes.Buffer
	pending, err := writeRedactedOutput(&output, []byte("before boundary-"), [][]byte{secret}, false)
	if err != nil {
		t.Fatal(err)
	}
	pending = append(pending, []byte("secret after")...)
	pending, err = writeRedactedOutput(&output, pending, [][]byte{secret}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writeRedactedOutput(&output, pending, [][]byte{secret}, true); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); got != "before [REDACTED:secret] after" {
		t.Fatalf("redacted output = %q", got)
	}
}

func installIgnoringConsoleHandler() error {
	procedure := windows.NewLazySystemDLL("kernel32.dll").NewProc("SetConsoleCtrlHandler")
	callback := windows.NewCallback(func(uint32) uintptr { return 1 })
	result, _, callErr := procedure.Call(callback, 1)
	if result == 0 {
		return fmt.Errorf("install ignoring console handler: %v", callErr)
	}
	return nil
}

func waitForFixtureReady(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("fixture child did not become signal-ready")
}

func consoleWindowVisible() bool {
	user32 := windows.NewLazySystemDLL("user32.dll")
	kernel32 := windows.NewLazySystemDLL("kernel32.dll")
	window, _, _ := kernel32.NewProc("GetConsoleWindow").Call()
	if window == 0 {
		return false
	}
	visible, _, _ := user32.NewProc("IsWindowVisible").Call(window)
	return visible != 0
}

func waitForFixturePID(t *testing.T, path string) uint32 {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		contents, err := os.ReadFile(path)
		if err == nil {
			pid, parseErr := strconv.ParseUint(strings.TrimSpace(string(contents)), 10, 32)
			if parseErr == nil {
				return uint32(pid)
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("fixture child PID was not published at %s", path)
	return 0
}

func openFixtureProcess(t *testing.T, pid uint32) windows.Handle {
	t.Helper()
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		t.Fatalf("open fixture process %d: %v", pid, err)
	}
	return handle
}

func assertFixtureSpool(t *testing.T, path, expected string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		contents, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(contents), expected) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	contents, err := os.ReadFile(path)
	t.Fatalf("spool %s = %q (%v), want %q", path, contents, err, expected)
}

func assertFixtureSpoolExcludes(t *testing.T, path, forbidden string) {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture spool: %v", err)
	}
	if strings.Contains(string(contents), forbidden) {
		t.Fatalf("spool %s contains forbidden plaintext", path)
	}
}

func assertRunningService(t *testing.T, service *managedService) {
	t.Helper()
	status, err := service.status()
	if err != nil || status.State != "running" || status.Identity == nil {
		t.Fatalf("status() = (%#v, %v), want running identity", status, err)
	}
}

func assertProcessExited(t *testing.T, handle windows.Handle) {
	t.Helper()
	result, err := windows.WaitForSingleObject(handle, 10_000)
	if err != nil || result != uint32(windows.WAIT_OBJECT_0) {
		t.Fatalf("descendant exit wait = (%d, %v)", result, err)
	}
}
