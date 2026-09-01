//go:build windows

package usertask

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestInstallUpgradeAndUninstallPreserveData(t *testing.T) {
	root := t.TempDir()
	installDir, dataDir := filepath.Join(root, "installed"), filepath.Join(root, "durable-data")
	source := copyTestExecutable(t, filepath.Join(root, "candidate-v1.exe"), false)
	candidate := copyTestExecutable(t, filepath.Join(root, "candidate-v2.exe"), true)
	fake := useFakeScheduler(t)
	options := InstallOptions{
		InstallDir: installDir, DataDir: dataDir, TaskName: "StackPilot-Test",
		SourceExecutable: source, Version: "v1", Port: 32991,
	}
	status, err := Install(context.Background(), options)
	if err != nil || status.State != "stopped" || len(fake.registered) != 1 {
		t.Fatalf("Install() = (%+v, %v), registrations = %d", status, err, len(fake.registered))
	}
	if _, err := Install(context.Background(), options); err != nil {
		t.Fatalf("idempotent Install() error = %v", err)
	}
	sentinel := filepath.Join(dataDir, "preserved.txt")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	status, err = Upgrade(context.Background(), installDir, candidate, "v2")
	if err != nil || status.Version != "v2" || len(fake.registered) != 3 {
		t.Fatalf("Upgrade() = (%+v, %v), registrations = %d", status, err, len(fake.registered))
	}
	status, err = Uninstall(context.Background(), installDir)
	if err != nil || status.State != "not-installed" || fake.deleted != "StackPilot-Test" {
		t.Fatalf("Uninstall() = (%+v, %v), deleted = %q", status, err, fake.deleted)
	}
	if _, err := os.Stat(installDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("installation directory still exists: %v", err)
	}
	if payload, err := os.ReadFile(sentinel); err != nil || string(payload) != "keep" {
		t.Fatalf("preserved data = (%q, %v)", payload, err)
	}
}

func TestUpgradeRepairsRegistrationWhenCandidateIsUnchanged(t *testing.T) {
	root := t.TempDir()
	installDir, dataDir := filepath.Join(root, "installed"), filepath.Join(root, "durable-data")
	source := copyTestExecutable(t, filepath.Join(root, "candidate.exe"), false)
	fake := useFakeScheduler(t)
	options := InstallOptions{
		InstallDir: installDir, DataDir: dataDir, TaskName: "StackPilot-Test",
		SourceExecutable: source, Version: "v1", Port: 32991,
	}
	if _, err := Install(context.Background(), options); err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	fake.registered = nil

	status, err := Upgrade(context.Background(), installDir, source, "v1")
	if err != nil || status.State != "stopped" || len(fake.registered) != 1 {
		t.Fatalf("Upgrade() = (%+v, %v), registrations = %d", status, err, len(fake.registered))
	}
	if fake.registered[0].ExecutablePath != status.ExecutablePath {
		t.Fatalf("registered executable = %q, want %q", fake.registered[0].ExecutablePath, status.ExecutablePath)
	}
}

func TestInstallRejectsOverlappingDataRoot(t *testing.T) {
	root := t.TempDir()
	source := copyTestExecutable(t, filepath.Join(root, "candidate.exe"), false)
	_, err := Install(context.Background(), InstallOptions{
		InstallDir: filepath.Join(root, "install"), DataDir: filepath.Join(root, "install", "data"),
		TaskName: "StackPilot-Test", SourceExecutable: source, Version: "v1", Port: 32991,
	})
	if err == nil || !strings.Contains(err.Error(), "must not overlap") {
		t.Fatalf("Install() error = %v, want overlap rejection", err)
	}
}

func TestRecordRejectsExecutableTampering(t *testing.T) {
	root := t.TempDir()
	fake := useFakeScheduler(t)
	source := copyTestExecutable(t, filepath.Join(root, "candidate.exe"), false)
	status, err := Install(context.Background(), InstallOptions{
		InstallDir: filepath.Join(root, "install"), DataDir: filepath.Join(root, "data"),
		TaskName: "StackPilot-Test", SourceExecutable: source, Version: "v1", Port: 32991,
	})
	if err != nil || len(fake.registered) != 1 {
		t.Fatal(err)
	}
	file, err := os.OpenFile(status.ExecutablePath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := file.Write([]byte("tampered"))
	if err := errors.Join(writeErr, file.Close()); err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(context.Background(), status.InstallDir); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("Inspect() error = %v, want checksum rejection", err)
	}
}

func TestScheduledTaskActionPreservesWindowsArguments(t *testing.T) {
	record := installRecord{
		ExecutablePath: `C:\Users\Test User\Programs\StackPilot\versions\abc\stackpilot.exe`,
		InstallDir:     `C:\Users\Test User\Programs\StackPilot`,
	}
	arguments, err := windows.DecomposeCommandLine(startupCommand(record))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{record.ExecutablePath, "internal-user-task-run", "--install-dir", record.InstallDir}
	if strings.Join(arguments, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("scheduled arguments = %#v, want %#v", arguments, want)
	}
}

func TestControlPipeReportsStatusAndCancels(t *testing.T) {
	record := installRecord{InstallationID: strings.Repeat("a", 32), Version: "test"}
	ctx, cancel := context.WithCancelCause(context.Background())
	server, err := startControlServer(ctx, record, cancel)
	if err != nil {
		t.Skipf("named pipes unavailable in this environment: %v", err)
	}
	defer func() {
		cancel(nil)
		_ = server.closeAndWait()
	}()
	response, err := exchangeControl(context.Background(), record, "status")
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) || os.IsPermission(err) {
		t.Skipf("named pipe connections unavailable in this sandbox: %v", err)
	}
	if err != nil || response.PID == 0 || response.Version != "test" {
		t.Fatalf("status exchange = (%+v, %v)", response, err)
	}
	if _, err := exchangeControl(context.Background(), record, "stop"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
		if !errors.Is(context.Cause(ctx), errControlStop) {
			t.Fatalf("control cancellation cause = %v", context.Cause(ctx))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stop request did not cancel the runtime context")
	}
}

func TestLifecycleExitReasonClassification(t *testing.T) {
	for _, test := range []struct {
		name  string
		cause error
		code  int
		want  string
	}{
		{name: "control stop", cause: errControlStop, want: "control_stop"},
		{name: "upgrade", cause: errUpgradeStop, want: "upgrade"},
		{name: "signal", cause: context.Canceled, want: "signal"},
		{name: "serve error", code: 1, want: "serve_error"},
		{name: "normal exit", want: "normal_exit"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := lifecycleExitReason(test.cause, test.code); got != test.want {
				t.Fatalf("lifecycleExitReason() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestHTTPReadyRequiresSuccessfulReadinessResponse(t *testing.T) {
	status := http.StatusServiceUnavailable
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(status)
	}))
	defer server.Close()
	port := server.Listener.Addr().(*net.TCPAddr).Port
	if httpReady(context.Background(), port) {
		t.Fatal("503 readiness response was accepted")
	}
	status = http.StatusOK
	if !httpReady(context.Background(), port) {
		t.Fatal("200 readiness response was rejected")
	}
}

func TestWaitForProcessExitHonorsContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := waitForProcessExit(ctx, windows.CurrentProcess())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waitForProcessExit() error = %v, want deadline exceeded", err)
	}
}

func copyTestExecutable(t *testing.T, target string, mutate bool) string {
	t.Helper()
	source, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if mutate {
		payload = append(payload, 0)
	}
	if err := os.WriteFile(target, payload, 0o700); err != nil {
		t.Fatal(err)
	}
	return target
}

type fakeTaskScheduler struct {
	registered []installRecord
	deleted    string
}

func useFakeScheduler(t *testing.T) *fakeTaskScheduler {
	t.Helper()
	prior := scheduler
	fake := &fakeTaskScheduler{}
	scheduler = fake
	t.Cleanup(func() { scheduler = prior })
	return fake
}

func (fake *fakeTaskScheduler) Register(_ context.Context, record installRecord) error {
	fake.registered = append(fake.registered, record)
	return nil
}

func (*fakeTaskScheduler) Run(context.Context, installRecord) error { return nil }
func (fake *fakeTaskScheduler) Delete(_ context.Context, record installRecord) error {
	fake.deleted = record.TaskName
	return nil
}
func (*fakeTaskScheduler) Exists(context.Context, installRecord) (bool, error) { return true, nil }
