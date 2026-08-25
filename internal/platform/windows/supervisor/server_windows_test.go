//go:build windows

package supervisor

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

const supervisorFixtureArgument = "--stackpilot-supervisor-runtime-fixture"

func TestSupervisorRuntimeFixture(t *testing.T) {
	index := argumentIndex(os.Args, supervisorFixtureArgument)
	if index < 0 {
		return
	}
	arguments := os.Args[index+1:]
	if len(arguments) != 2 {
		os.Exit(30)
	}
	switch arguments[0] {
	case "serve":
		if err := Serve(context.Background(), Config{InstanceDir: arguments[1]}); err != nil {
			os.Exit(31)
		}
	case "launch":
		executable, err := os.Executable()
		if err != nil {
			os.Exit(32)
		}
		process, err := startDetachedProcess(arguments[1], executable,
			[]string{"-test.run=TestSupervisorRuntimeFixture", "--", supervisorFixtureArgument, "serve", arguments[1]})
		if err != nil || process.Release() != nil {
			os.Exit(33)
		}
	default:
		os.Exit(34)
	}
	os.Exit(0)
}

func TestSupervisorProtocolLifecycleAndCleanShutdown(t *testing.T) {
	instanceDir := newSupervisorTestInstance(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- Serve(ctx, Config{InstanceDir: instanceDir}) }()
	client, identity := waitForSupervisorClient(t, instanceDir)
	assertHelloRequiresPeerPID(t, identity)
	request, childPIDPath := supervisorFixtureRequest(t, instanceDir)
	var started ServiceStatus
	exchange(t, client, MessageStartService, request, &started)
	assertSupervisorNotEmpty(t, client)
	_ = waitForFixturePID(t, childPIDPath)
	var inspected ServiceStatus
	exchange(t, client, MessageInspectService, ServiceRequest{ServiceID: request.ServiceID}, &inspected)
	if inspected.State != "running" || inspected.Identity == nil || inspected.Identity.PID != started.Identity.PID {
		t.Fatalf("inspect response = %#v, want running started identity", inspected)
	}
	var stopped ServiceStatus
	exchange(t, client, MessageStopService, StopServiceRequest{ServiceID: request.ServiceID}, &stopped)
	if stopped.State != "exited" || !stopped.Forced {
		t.Fatalf("stop response = %#v, want forced exited", stopped)
	}
	exchange(t, client, MessageShutdownIfEmpty, struct{}{}, &struct{}{})
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Serve() error = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Supervisor did not exit after shutdown-if-empty response")
	}
	if _, err := os.Stat(filepath.Join(instanceDir, "supervisor.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("clean Supervisor identity still exists: %v", err)
	}
}

func TestDetachedSupervisorGracefulStopEndsWholeJob(t *testing.T) {
	instanceDir := newSupervisorTestInstance(t)
	client, _ := launchSupervisorFixture(t, instanceDir)
	request, childPIDPath := supervisorFixtureRequest(t, instanceDir)
	var started ServiceStatus
	exchange(t, client, MessageStartService, request, &started)
	childPID := waitForFixturePID(t, childPIDPath)
	rootHandle := openFixtureProcess(t, started.Identity.PID)
	defer windows.CloseHandle(rootHandle)
	childHandle := openFixtureProcess(t, childPID)
	defer windows.CloseHandle(childHandle)
	var stopped ServiceStatus
	exchange(t, client, MessageStopService,
		StopServiceRequest{ServiceID: request.ServiceID, GracefulTimeoutMillis: 5_000}, &stopped)
	if stopped.State != "exited" || stopped.Forced {
		t.Fatalf("graceful stop response = %#v, want non-forced exited", stopped)
	}
	assertFixtureSpool(t, childPIDPath+".graceful", "received")
	assertFixtureSpool(t, childPIDPath+".visible", "false")
	assertProcessExited(t, rootHandle)
	assertProcessExited(t, childHandle)
	exchange(t, client, MessageShutdownIfEmpty, struct{}{}, &struct{}{})
}

func TestDetachedSupervisorForcesIgnoredGracefulStopAfterTimeout(t *testing.T) {
	instanceDir := newSupervisorTestInstance(t)
	client, _ := launchSupervisorFixture(t, instanceDir)
	request, childPIDPath := supervisorFixtureRequest(t, instanceDir)
	for index, argument := range request.Arguments {
		if argument == "tree" {
			request.Arguments[index] = "ignore"
		}
	}
	var started ServiceStatus
	exchange(t, client, MessageStartService, request, &started)
	childPID := waitForFixturePID(t, childPIDPath)
	rootHandle := openFixtureProcess(t, started.Identity.PID)
	defer windows.CloseHandle(rootHandle)
	childHandle := openFixtureProcess(t, childPID)
	defer windows.CloseHandle(childHandle)
	startedAt := time.Now()
	var stopped ServiceStatus
	exchange(t, client, MessageStopService,
		StopServiceRequest{ServiceID: request.ServiceID, GracefulTimeoutMillis: 250}, &stopped)
	if stopped.State != "exited" || !stopped.Forced {
		t.Fatalf("ignored graceful stop response = %#v, want forced exited", stopped)
	}
	if elapsed := time.Since(startedAt); elapsed < 200*time.Millisecond {
		t.Fatalf("forced stop elapsed = %v, want graceful timeout first", elapsed)
	}
	assertProcessExited(t, rootHandle)
	assertProcessExited(t, childHandle)
	exchange(t, client, MessageShutdownIfEmpty, struct{}{}, &struct{}{})
}

func TestSupervisorRejectsSecondRuntimeForInstance(t *testing.T) {
	instanceDir := newSupervisorTestInstance(t)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- Serve(ctx, Config{InstanceDir: instanceDir}) }()
	client, _ := waitForSupervisorClient(t, instanceDir)
	if err := Serve(context.Background(), Config{InstanceDir: instanceDir}); err == nil {
		t.Fatal("second Supervisor unexpectedly acquired the instance")
	}
	exchange(t, client, MessageShutdownIfEmpty, struct{}{}, &struct{}{})
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("first Supervisor exit error = %v", err)
		}
	case <-time.After(10 * time.Second):
		cancel()
		t.Fatal("first Supervisor did not exit")
	}
	cancel()
}

func TestDetachedSupervisorReconnectAndCrashKillsTree(t *testing.T) {
	instanceDir := newSupervisorTestInstance(t)
	client, identity := launchSupervisorFixture(t, instanceDir)
	var err error
	client, err = Connect(testContext(t), identity)
	if err != nil {
		t.Fatalf("second Supervisor reconnect failed: %v", err)
	}
	request, childPIDPath := supervisorFixtureRequest(t, instanceDir)
	var started ServiceStatus
	exchange(t, client, MessageStartService, request, &started)
	childPID := waitForFixturePID(t, childPIDPath)
	rootHandle := openFixtureProcess(t, started.Identity.PID)
	defer windows.CloseHandle(rootHandle)
	childHandle := openFixtureProcess(t, childPID)
	defer windows.CloseHandle(childHandle)
	supervisorHandle, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, identity.PID)
	if err != nil {
		t.Fatalf("open detached Supervisor: %v", err)
	}
	defer windows.CloseHandle(supervisorHandle)
	if err := windows.TerminateProcess(supervisorHandle, 99); err != nil {
		t.Fatalf("terminate detached Supervisor: %v", err)
	}
	assertProcessExited(t, supervisorHandle)
	assertProcessExited(t, rootHandle)
	assertProcessExited(t, childHandle)
}

func launchSupervisorFixture(t *testing.T, instanceDir string) (*Client, SupervisorIdentity) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	launcher := exec.Command(executable, "-test.run=TestSupervisorRuntimeFixture", "--",
		supervisorFixtureArgument, "launch", instanceDir)
	if output, err := launcher.CombinedOutput(); err != nil {
		t.Fatalf("detached launcher failed: %v (%s)", err, output)
	}
	client, identity := waitForSupervisorClient(t, instanceDir)
	t.Cleanup(func() { terminateFixtureSupervisor(identity.PID) })
	return client, identity
}

func terminateFixtureSupervisor(pid uint32) {
	handle, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, pid)
	if err != nil {
		return
	}
	defer windows.CloseHandle(handle)
	if result, _ := windows.WaitForSingleObject(handle, 0); result == uint32(windows.WAIT_TIMEOUT) {
		_ = windows.TerminateProcess(handle, 98)
		_, _ = windows.WaitForSingleObject(handle, 10_000)
	}
}

func newSupervisorTestInstance(t *testing.T) string {
	t.Helper()
	instanceDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(instanceDir, "services", "backend"), 0o700); err != nil {
		t.Fatalf("create Supervisor test instance: %v", err)
	}
	return instanceDir
}

func waitForSupervisorClient(t *testing.T, instanceDir string) (*Client, SupervisorIdentity) {
	t.Helper()
	ctx := testContext(t)
	identityPath := filepath.Join(instanceDir, "supervisor.json")
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		identity, err := ReadSupervisorIdentity(identityPath)
		if err == nil {
			client, connectErr := Connect(ctx, identity)
			if connectErr == nil {
				return client, identity
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("Supervisor did not become connectable: %v", ctx.Err())
		case <-ticker.C:
		}
	}
}

func supervisorFixtureRequest(t *testing.T, instanceDir string) (StartServiceRequest, string) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve fixture executable: %v", err)
	}
	childPIDPath := filepath.Join(instanceDir, "services", "backend", "child.pid")
	return fixtureStartRequest(executable, instanceDir, childPIDPath), childPIDPath
}

func assertHelloRequiresPeerPID(t *testing.T, identity SupervisorIdentity) {
	t.Helper()
	client, err := NewClient(identity)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	var response HelloResponse
	err = client.Exchange(testContext(t), MessageHello, HelloRequest{ClientPID: uint32(os.Getpid() + 1)}, &response)
	var remote *RemoteError
	if !errors.As(err, &remote) || remote.Code != ErrorIdentityMismatch {
		t.Fatalf("forged hello error = %v, want identity mismatch", err)
	}
}

func assertSupervisorNotEmpty(t *testing.T, client *Client) {
	t.Helper()
	err := client.Exchange(testContext(t), MessageShutdownIfEmpty, struct{}{}, &struct{}{})
	var remote *RemoteError
	if !errors.As(err, &remote) || remote.Code != ErrorSupervisorNotEmpty {
		t.Fatalf("non-empty shutdown error = %v, want supervisor-not-empty", err)
	}
}

func exchange(t *testing.T, client *Client, message MessageType, request, response any) {
	t.Helper()
	if err := client.Exchange(testContext(t), message, request, response); err != nil {
		t.Fatalf("Exchange(%s) error = %v", message, err)
	}
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func argumentIndex(arguments []string, value string) int {
	for index, argument := range arguments {
		if argument == value {
			return index
		}
	}
	return -1
}
