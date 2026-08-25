//go:build windows

package compose

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"

	"stackpilot/internal/security"
)

// FollowLogs starts fixed Compose log streaming into existing unified-log spools.
func (lifecycle *Lifecycle) FollowLogs(ctx context.Context, request LogFollowRequest) (*LogSession, error) {
	docker, _, err := lifecycle.validateIdentity(request.Identity)
	if err != nil {
		return nil, err
	}
	stdout, err := openComposeSpool(request.Identity.DataDir, request.StdoutPath)
	if err != nil {
		return nil, err
	}
	stderr, err := openComposeSpool(request.Identity.DataDir, request.StderrPath)
	if err != nil {
		_ = stdout.Close()
		return nil, err
	}
	followContext, cancel := context.WithCancel(ctx)
	arguments := append(projectArguments(request.Identity), "logs", "--no-color", "--timestamps", "--follow")
	if !request.Since.IsZero() {
		if request.Since.Location() != time.UTC {
			_ = stdout.Close()
			_ = stderr.Close()
			cancel()
			return nil, ErrLifecycleInvalid
		}
		arguments = append(arguments, "--since", request.Since.Format(time.RFC3339Nano))
	}
	arguments = append(arguments, request.Identity.Services...)
	process, err := lifecycle.startLog(followContext, docker, arguments, filepath.Dir(request.Identity.ComposeFile), lifecycle.environment, stdout, stderr)
	if err != nil {
		cancel()
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, ErrLogFollowFailed
	}
	session := &LogSession{cancel: cancel, done: make(chan struct{})}
	go waitForLogProcess(followContext, session, process, stdout, stderr)
	return session, nil
}

func openComposeSpool(dataDir, path string) (*os.File, error) {
	if !filepath.IsAbs(path) {
		return nil, ErrLifecycleInvalid
	}
	parent, err := security.CanonicalExistingPath(filepath.Dir(path))
	if err != nil {
		return nil, ErrLifecycleInvalid
	}
	inside, err := security.PathWithinRoot(dataDir, parent)
	if err != nil || !inside {
		return nil, ErrLifecycleInvalid
	}
	if info, statErr := os.Lstat(path); statErr == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return nil, ErrLifecycleInvalid
		}
		if _, err := canonicalContainedFile(dataDir, path); err != nil {
			return nil, err
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, ErrLifecycleInvalid
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, ErrLogFollowFailed
	}
	canonical, err := security.CanonicalExistingPath(path)
	if err != nil || !strings.EqualFold(filepath.Dir(canonical), parent) {
		_ = file.Close()
		return nil, ErrLifecycleInvalid
	}
	return file, nil
}

func waitForLogProcess(ctx context.Context, session *LogSession, process LogProcess, stdout, stderr *os.File) {
	err := process.Wait()
	err = errors.Join(err, stdout.Sync(), stderr.Sync(), stdout.Close(), stderr.Close())
	if ctx.Err() != nil {
		err = nil
	}
	session.err = err
	close(session.done)
}

// Close stops the owned follow command and flushes both spools.
func (session *LogSession) Close() error {
	if session == nil || session.cancel == nil {
		return nil
	}
	session.cancel()
	<-session.done
	return session.err
}

type commandLogProcess struct{ command *exec.Cmd }

func (process commandLogProcess) Wait() error { return process.command.Wait() }

func startLogCommand(ctx context.Context, executable string, arguments []string, directory string, environment map[string]string, stdout, stderr io.Writer) (LogProcess, error) {
	command := exec.CommandContext(ctx, executable, arguments...)
	command.Dir, command.Env = directory, environmentList(environment)
	command.Stdout, command.Stderr = stdout, stderr
	command.SysProcAttr = &windows.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	if err := command.Start(); err != nil {
		return nil, err
	}
	return commandLogProcess{command: command}, nil
}
