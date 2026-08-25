package main

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestProcessFixtureModes(t *testing.T) {
	binary := buildFixture(t)
	t.Run("slow-ready", func(t *testing.T) { testSlowReady(t, binary) })
	t.Run("immediate-exit", func(t *testing.T) { testImmediateExit(t, binary) })
	t.Run("child-tree", func(t *testing.T) { testChildTree(t, binary) })
	t.Run("ignore-terminate", func(t *testing.T) { testIgnoreTerminate(t, binary) })
	t.Run("large-log", func(t *testing.T) { testLargeLog(t, binary) })
	t.Run("secret-log", func(t *testing.T) { testSecretLog(t, binary) })
	t.Run("port-competition", func(t *testing.T) { testPortCompetition(t, binary) })
}

func testSecretLog(t *testing.T, binary string) {
	command := exec.Command(binary, "--mode", "secret-log", "--environment", "STACKPILOT_FIXTURE_SECRET")
	command.Env = append(os.Environ(), "STACKPILOT_FIXTURE_SECRET=fixture-secret-value")
	output, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { killAndWait(command) })
	reader := bufio.NewReader(output)
	line, err := reader.ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "fixture-secret-value" {
		t.Fatalf("secret-log output = (%q, %v)", line, err)
	}
}

func buildFixture(t *testing.T) string {
	t.Helper()
	name := "process-fixture"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	binary := filepath.Join(t.TempDir(), name)
	goBinary := filepath.Join(runtime.GOROOT(), "bin", "go")
	if runtime.GOOS == "windows" {
		goBinary += ".exe"
	}
	command := exec.Command(goBinary, "build", "-o", binary, ".")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build fixture: %v\n%s", err, output)
	}
	return binary
}

func testSlowReady(t *testing.T, binary string) {
	port := fixturePort(t)
	command := exec.Command(binary, "--mode", "slow-ready", "--port", strconv.Itoa(port), "--delay", "300ms")
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { killAndWait(command) })
	if connection, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 50*time.Millisecond); err == nil {
		_ = connection.Close()
		t.Fatal("slow-ready listened before its delay")
	}
	waitForFixturePort(t, port)
}

func testImmediateExit(t *testing.T, binary string) {
	command := exec.Command(binary, "--mode", "immediate-exit", "--exit-code", "23")
	err := command.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 23 {
		t.Fatalf("immediate exit error = %v", err)
	}
}

func testChildTree(t *testing.T, binary string) {
	marker := filepath.Join(t.TempDir(), "child.pid")
	command := exec.Command(binary, "--mode", "child-tree", "--marker", marker)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { killAndWait(command) })
	waitForFixtureFile(t, marker)
	contents, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(contents)))
	if err != nil || pid <= 0 {
		t.Fatalf("child PID = %q", contents)
	}
	child, _ := os.FindProcess(pid)
	t.Cleanup(func() { _ = child.Kill() })
}

func testIgnoreTerminate(t *testing.T, binary string) {
	command := exec.Command(binary, "--mode", "ignore-terminate")
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { killAndWait(command) })
	time.Sleep(100 * time.Millisecond)
	if command.ProcessState != nil {
		t.Fatal("ignore-terminate exited unexpectedly")
	}
}

func testLargeLog(t *testing.T, binary string) {
	stdout, err := os.Create(filepath.Join(t.TempDir(), "stdout.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer stdout.Close()
	stderr, err := os.Create(filepath.Join(t.TempDir(), "stderr.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer stderr.Close()
	command := exec.Command(binary, "--mode", "large-log", "--bytes", "131072")
	command.Stdout, command.Stderr = stdout, stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { killAndWait(command) })
	deadline := time.Now().Add(3 * time.Second)
	var stdoutSize, stderrSize int64
	for time.Now().Before(deadline) {
		stdoutInfo, _ := stdout.Stat()
		stderrInfo, _ := stderr.Stat()
		stdoutSize, stderrSize = stdoutInfo.Size(), stderrInfo.Size()
		if stdoutSize >= 131072 && stderrSize >= 131072 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if stdoutSize < 131072 || stderrSize < 131072 {
		t.Fatalf("large log sizes = stdout %d, stderr %d", stdoutSize, stderrSize)
	}
}

func testPortCompetition(t *testing.T, binary string) {
	port := fixturePort(t)
	owner := exec.Command(binary, "--mode", "hold-port", "--port", strconv.Itoa(port))
	if err := owner.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { killAndWait(owner) })
	waitForFixturePort(t, port)
	competitor := exec.Command(binary, "--mode", "hold-port", "--port", strconv.Itoa(port))
	output, err := competitor.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "listen fixture port") {
		t.Fatalf("port competitor = (%v, %q)", err, output)
	}
}

func fixturePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	return port
}

func waitForFixturePort(t *testing.T, port int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		connection, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 50*time.Millisecond)
		if err == nil {
			_ = connection.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("fixture port %d did not become ready", port)
}

func waitForFixtureFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("fixture marker %s was not created", path)
}

func killAndWait(command *exec.Cmd) {
	if command.Process != nil {
		_ = command.Process.Kill()
	}
	_ = command.Wait()
}
