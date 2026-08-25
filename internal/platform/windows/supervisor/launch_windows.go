//go:build windows

package supervisor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

const supervisorStartupTimeout = 15 * time.Second

// Launch starts a detached Supervisor and returns a verified client.
func Launch(ctx context.Context, instanceDir string) (*Client, SupervisorIdentity, error) {
	canonical, err := canonicalInstanceDirectory(instanceDir)
	if err != nil {
		return nil, SupervisorIdentity{}, err
	}
	identityPath := filepath.Join(canonical, "supervisor.json")
	if existing, err := ReadSupervisorIdentity(identityPath); err == nil {
		client, connectErr := Connect(ctx, existing)
		return client, existing, connectErr
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, SupervisorIdentity{}, fmt.Errorf("existing Supervisor identity is invalid: %w", err)
	}
	process, err := startDetachedSupervisor(canonical)
	if err != nil {
		return nil, SupervisorIdentity{}, err
	}
	client, identity, err := awaitSupervisor(ctx, identityPath)
	if err != nil {
		_ = process.Kill()
		_, _ = process.Wait()
		return nil, SupervisorIdentity{}, err
	}
	if err := process.Release(); err != nil {
		return nil, SupervisorIdentity{}, fmt.Errorf("release detached Supervisor: %w", err)
	}
	return client, identity, nil
}

func startDetachedSupervisor(instanceDir string) (*os.Process, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve StackPilot executable: %w", err)
	}
	return startDetachedProcess(instanceDir, executable, []string{"internal-supervisor", "--instance-dir", instanceDir})
}

func startDetachedProcess(instanceDir, executable string, arguments []string) (*os.Process, error) {
	stdin, stdout, stderr, err := openSupervisorStreams(instanceDir)
	if err != nil {
		return nil, err
	}
	defer stdin.Close()
	defer stdout.Close()
	defer stderr.Close()
	attributes := &os.ProcAttr{
		Dir: instanceDir, Files: []*os.File{stdin, stdout, stderr},
		Sys: &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW},
	}
	processArguments := append([]string{executable}, arguments...)
	process, err := os.StartProcess(executable, processArguments, attributes)
	if err != nil {
		return nil, fmt.Errorf("start detached Supervisor: %w", err)
	}
	return process, nil
}

func openSupervisorStreams(instanceDir string) (*os.File, *os.File, *os.File, error) {
	stdin, err := os.OpenFile(os.DevNull, os.O_RDONLY, 0)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("open Supervisor stdin: %w", err)
	}
	stdout, err := openSpoolFile(instanceDir, filepath.Join(instanceDir, "supervisor.stdout.log"))
	if err != nil {
		_ = stdin.Close()
		return nil, nil, nil, fmt.Errorf("open Supervisor stdout: %w", err)
	}
	stderr, err := openSpoolFile(instanceDir, filepath.Join(instanceDir, "supervisor.stderr.log"))
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, nil, nil, fmt.Errorf("open Supervisor stderr: %w", err)
	}
	return stdin, stdout, stderr, nil
}

func awaitSupervisor(ctx context.Context, identityPath string) (*Client, SupervisorIdentity, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, supervisorStartupTimeout)
		defer cancel()
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		identity, err := ReadSupervisorIdentity(identityPath)
		if err == nil {
			client, connectErr := Connect(ctx, identity)
			if connectErr == nil {
				return client, identity, nil
			}
		}
		select {
		case <-ctx.Done():
			return nil, SupervisorIdentity{}, fmt.Errorf("wait for Supervisor startup: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}
