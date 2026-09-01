//go:build windows

package usertask

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

var scheduler taskScheduler = windowsTaskScheduler{}

var (
	errControlStop = errors.New("user-task control stop")
	errUpgradeStop = errors.New("user-task upgrade stop")
)

// Install creates or idempotently confirms one current-user background process installation.
func Install(ctx context.Context, options InstallOptions) (Status, error) {
	options, err := normalizeInstallOptions(options)
	if err != nil {
		return Status{}, err
	}
	installRoot, dataRoot, err := prepareRoots(options.InstallDir, options.DataDir)
	if err != nil {
		return Status{}, err
	}
	options.InstallDir, options.DataDir = installRoot, dataRoot
	if existing, err := loadRecord(installRoot); err == nil {
		return installExisting(ctx, existing, options)
	} else if !errors.Is(err, ErrNotInstalled) {
		return Status{}, err
	}
	record, err := createInstallRecord(options, installRoot, dataRoot)
	if err != nil {
		return Status{}, err
	}
	if err := writeRecord(record); err != nil {
		return Status{}, err
	}
	if err := scheduler.Register(ctx, record); err != nil {
		_ = os.Remove(filepath.Join(record.InstallDir, markerName))
		return Status{}, err
	}
	if options.Start {
		return startRecord(ctx, record)
	}
	return statusFromRecord(record, false, 0), nil
}

func createInstallRecord(options InstallOptions, installRoot, dataRoot string) (installRecord, error) {
	checksum, err := hashFile(options.SourceExecutable)
	if err != nil {
		return installRecord{}, err
	}
	executable, err := stageExecutable(options.SourceExecutable, installRoot, checksum)
	if err != nil {
		return installRecord{}, err
	}
	installationID, err := newInstallationID()
	if err != nil {
		return installRecord{}, err
	}
	return newRecord(options, installRoot, dataRoot, installationID, executable, checksum, time.Now()), nil
}

func installExisting(ctx context.Context, record installRecord, options InstallOptions) (Status, error) {
	checksum, err := hashFile(options.SourceExecutable)
	if err != nil {
		return Status{}, err
	}
	if !strings.EqualFold(checksum, record.SHA256) || !strings.EqualFold(record.DataDir, options.DataDir) ||
		record.TaskName != options.TaskName || record.Port != options.Port {
		return Status{}, ErrInstalled
	}
	if err := scheduler.Register(ctx, record); err != nil {
		return Status{}, err
	}
	if options.Start {
		return startRecord(ctx, record)
	}
	return inspectRecord(ctx, record)
}

// Upgrade atomically changes the registered task to the invoking candidate binary.
func Upgrade(ctx context.Context, installDir, sourceExecutable, version string) (Status, error) {
	record, err := loadRecord(installDir)
	if err != nil {
		return Status{}, err
	}
	checksum, err := hashFile(sourceExecutable)
	if err != nil {
		return Status{}, err
	}
	if strings.EqualFold(checksum, record.SHA256) {
		if err := scheduler.Register(ctx, record); err != nil {
			return Status{}, err
		}
		return inspectRecord(ctx, record)
	}
	wasRunning := isRunning(ctx, record)
	if wasRunning {
		if _, err := stopRecordWithAction(ctx, record, "upgrade"); err != nil {
			return Status{}, err
		}
	}
	candidate, err := stageExecutable(sourceExecutable, record.InstallDir, checksum)
	if err != nil {
		return restoreRunning(ctx, record, wasRunning, err)
	}
	updated := record
	updated.ExecutablePath, updated.SHA256, updated.Version, updated.UpdatedAt = candidate, checksum, version, time.Now().UTC()
	return publishUpgrade(ctx, record, updated, wasRunning)
}

func publishUpgrade(ctx context.Context, prior, updated installRecord, wasRunning bool) (Status, error) {
	if err := writeRecord(updated); err != nil {
		return restoreRunning(ctx, prior, wasRunning, err)
	}
	if err := scheduler.Register(ctx, updated); err != nil {
		return rollbackUpgrade(ctx, prior, wasRunning, err)
	}
	if !wasRunning {
		return statusFromRecord(updated, false, 0), nil
	}
	status, err := startRecord(ctx, updated)
	if err != nil {
		return rollbackUpgrade(ctx, prior, true, err)
	}
	return status, nil
}

func rollbackUpgrade(ctx context.Context, prior installRecord, wasRunning bool, cause error) (Status, error) {
	restoreError := errors.Join(writeRecord(prior), scheduler.Register(ctx, prior))
	if wasRunning && restoreError == nil {
		_, restoreError = startRecord(ctx, prior)
	}
	return Status{}, errors.Join(fmt.Errorf("upgrade failed: %w", cause), restoreError)
}

func restoreRunning(ctx context.Context, record installRecord, wasRunning bool, cause error) (Status, error) {
	if !wasRunning {
		return Status{}, cause
	}
	_, restoreError := startRecord(ctx, record)
	return Status{}, errors.Join(cause, restoreError)
}

// Start starts the installed task and waits for its authenticated local control channel.
func Start(ctx context.Context, installDir string) (Status, error) {
	record, err := loadRecord(installDir)
	if err != nil {
		return Status{}, err
	}
	return startRecord(ctx, record)
}

func startRecord(ctx context.Context, record installRecord) (Status, error) {
	if status, err := controlStatus(ctx, record); err == nil {
		return status, nil
	}
	if err := scheduler.Run(ctx, record); err != nil {
		return Status{}, err
	}
	waitCtx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	return waitUntilRunning(waitCtx, record)
}

func waitUntilRunning(ctx context.Context, record installRecord) (Status, error) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if status, err := controlStatus(ctx, record); err == nil && httpReady(ctx, record.Port) {
			return status, nil
		}
		select {
		case <-ctx.Done():
			return Status{}, fmt.Errorf("wait for user task to start: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func httpReady(ctx context.Context, port int) bool {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/health/ready", port), nil)
	if err != nil {
		return false
	}
	client := &http.Client{Timeout: 500 * time.Millisecond}
	response, err := client.Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
	return response.StatusCode == http.StatusOK
}

// Stop gracefully stops the installed process and refuses termination if control cannot be proven.
func Stop(ctx context.Context, installDir string) (Status, error) {
	record, err := loadRecord(installDir)
	if err != nil {
		return Status{}, err
	}
	return stopRecord(ctx, record)
}

func stopRecord(ctx context.Context, record installRecord) (Status, error) {
	return stopRecordWithAction(ctx, record, "stop")
}

func stopRecordWithAction(ctx context.Context, record installRecord, action string) (Status, error) {
	status, err := controlStatus(ctx, record)
	if err != nil {
		return statusFromRecord(record, false, 0), nil
	}
	process, err := windows.OpenProcess(windows.SYNCHRONIZE, false, status.PID)
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return statusFromRecord(record, false, 0), nil
	}
	if err != nil {
		return forceStop(ctx, record, fmt.Errorf("open user-task process %d: %w", status.PID, err))
	}
	defer windows.CloseHandle(process)
	requestCtx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	response, err := exchangeControl(requestCtx, record, action)
	if err != nil {
		return forceStop(ctx, record, err)
	}
	if response.PID != status.PID {
		return forceStop(ctx, record, fmt.Errorf("user-task process changed from %d to %d", status.PID, response.PID))
	}
	if err := waitForProcessExit(requestCtx, process); err != nil {
		return forceStop(ctx, record, err)
	}
	return statusFromRecord(record, false, 0), nil
}

func forceStop(ctx context.Context, record installRecord, cause error) (Status, error) {
	return Status{}, fmt.Errorf("graceful user-task stop failed; refusing unverified process termination: %w", cause)
}

func waitForProcessExit(ctx context.Context, process windows.Handle) error {
	for {
		result, err := windows.WaitForSingleObject(process, 100)
		if err != nil {
			return fmt.Errorf("wait for user-task process: %w", err)
		}
		if result == windows.WAIT_OBJECT_0 {
			return nil
		}
		if result != uint32(windows.WAIT_TIMEOUT) {
			return fmt.Errorf("wait for user-task process returned %d", result)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for user task to stop: %w", ctx.Err())
		default:
		}
	}
}

// Inspect returns installed, broken, stopped, or running state without changing it.
func Inspect(ctx context.Context, installDir string) (Status, error) {
	record, err := loadRecord(installDir)
	if errors.Is(err, ErrNotInstalled) {
		return Status{Mode: Mode, State: "not-installed"}, nil
	}
	if err != nil {
		return Status{}, err
	}
	return inspectRecord(ctx, record)
}

func inspectRecord(ctx context.Context, record installRecord) (Status, error) {
	exists, err := scheduler.Exists(ctx, record)
	if err != nil {
		return Status{}, err
	}
	if !exists {
		status := statusFromRecord(record, false, 0)
		status.State = "broken"
		return status, nil
	}
	if status, err := controlStatus(ctx, record); err == nil {
		return status, nil
	}
	return statusFromRecord(record, false, 0), nil
}

func controlStatus(ctx context.Context, record installRecord) (Status, error) {
	requestCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	response, err := exchangeControl(requestCtx, record, "status")
	if err != nil {
		return Status{}, err
	}
	return statusFromRecord(record, true, response.PID), nil
}

func isRunning(ctx context.Context, record installRecord) bool {
	_, err := controlStatus(ctx, record)
	return err == nil
}

// Uninstall unregisters installation files while always preserving the separate data root.
func Uninstall(ctx context.Context, installDir string) (Status, error) {
	record, err := loadRecord(installDir)
	if err != nil {
		return Status{}, err
	}
	if _, err := stopRecord(ctx, record); err != nil {
		return Status{}, err
	}
	if err := scheduler.Delete(ctx, record); err != nil {
		return Status{}, err
	}
	if err := removeInstallation(ctx, record); err != nil {
		return Status{}, err
	}
	return Status{Mode: Mode, State: "not-installed", DataDir: record.DataDir}, nil
}

// RunInstalled runs the registered Server and hosts the graceful-stop control channel.
func RunInstalled(ctx context.Context, installDir string, stdout, stderr io.Writer, serve ServerFunc) int {
	startupLogger := newLifecycleLogger(stderr)
	record, err := loadRecord(installDir)
	if err != nil {
		startupLogger.Error("user task lifecycle", "event", "exit", "reason", "startup_error", "error_code", "INSTALLATION_LOAD_FAILED", "exit_code", 1)
		return 1
	}
	logOutput, err := openServerLog(record.DataDir)
	if err != nil {
		startupLogger.Error("user task lifecycle", "event", "exit", "reason", "startup_error", "error_code", "SERVER_LOG_OPEN_FAILED", "exit_code", 1)
		return 1
	}
	logger := newLifecycleLogger(logOutput)
	runCtx, cancel := context.WithCancelCause(ctx)
	control, err := startControlServer(runCtx, record, cancel)
	if err != nil {
		logger.Error("user task lifecycle", "event", "exit", "reason", "startup_error", "error_code", "CONTROL_CHANNEL_START_FAILED", "exit_code", 1)
		cancel(nil)
		_ = logOutput.Close()
		return 1
	}
	logger.Info("user task lifecycle", "event", "startup", "reason", "startup", "pid", os.Getpid(), "version", record.Version)
	code := serve(runCtx, []string{"--data-dir", record.DataDir, "--port", fmt.Sprint(record.Port)}, logOutput, logOutput)
	reason := lifecycleExitReason(context.Cause(runCtx), code)
	logger.Info("user task lifecycle", "event", "exit", "reason", reason, "exit_code", code)
	cancel(nil)
	if err := errors.Join(control.closeAndWait(), logOutput.Close()); err != nil {
		startupLogger.Error("user task lifecycle", "event", "cleanup_error", "reason", reason, "error_code", "RUNTIME_CLOSE_FAILED", "exit_code", 1)
		return 1
	}
	return code
}

func lifecycleExitReason(cause error, exitCode int) string {
	switch {
	case errors.Is(cause, errControlStop):
		return "control_stop"
	case errors.Is(cause, errUpgradeStop):
		return "upgrade"
	case cause != nil:
		return "signal"
	case exitCode != 0:
		return "serve_error"
	default:
		return "normal_exit"
	}
}

func newLifecycleLogger(output io.Writer) *slog.Logger {
	options := &slog.HandlerOptions{ReplaceAttr: func(_ []string, attribute slog.Attr) slog.Attr {
		if attribute.Key == slog.TimeKey {
			attribute.Value = slog.TimeValue(attribute.Value.Time().UTC())
		}
		return attribute
	}}
	return slog.New(slog.NewJSONHandler(output, options))
}

func openServerLog(dataDir string) (*os.File, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(dataDir, "stackpilot-server.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	return file, nil
}
