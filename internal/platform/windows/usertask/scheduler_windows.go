//go:build windows

package usertask

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const startupRegistryPath = `Software\Microsoft\Windows\CurrentVersion\Run`

type taskScheduler interface {
	Register(context.Context, installRecord) error
	Run(context.Context, installRecord) error
	Delete(context.Context, installRecord) error
	Exists(context.Context, installRecord) (bool, error)
}

type windowsTaskScheduler struct{}

func (windowsTaskScheduler) Register(ctx context.Context, record installRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	key, err := registry.OpenKey(registry.CURRENT_USER, startupRegistryPath, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("open current-user startup registry: %w", err)
	}
	defer key.Close()
	if err := key.SetStringValue(record.TaskName, startupCommand(record)); err != nil {
		return fmt.Errorf("register current-user startup command: %w", err)
	}
	return nil
}

func (windowsTaskScheduler) Run(ctx context.Context, record installRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	command := exec.CommandContext(ctx, record.ExecutablePath, "internal-user-task-run", "--install-dir", record.InstallDir)
	command.SysProcAttr = &syscall.SysProcAttr{
		HideWindow: true, CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS,
	}
	if err := command.Start(); err != nil {
		return fmt.Errorf("start current-user background process: %w", err)
	}
	return command.Process.Release()
}

func (windowsTaskScheduler) Delete(ctx context.Context, record installRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	key, err := registry.OpenKey(registry.CURRENT_USER, startupRegistryPath, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("open current-user startup registry: %w", err)
	}
	defer key.Close()
	if err := key.DeleteValue(record.TaskName); err != nil && err != registry.ErrNotExist {
		return fmt.Errorf("remove current-user startup command: %w", err)
	}
	return nil
}

func (windowsTaskScheduler) Exists(ctx context.Context, record installRecord) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	key, err := registry.OpenKey(registry.CURRENT_USER, startupRegistryPath, registry.QUERY_VALUE)
	if err != nil {
		return false, fmt.Errorf("open current-user startup registry: %w", err)
	}
	defer key.Close()
	value, _, err := key.GetStringValue(record.TaskName)
	if err == registry.ErrNotExist {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read current-user startup command: %w", err)
	}
	return strings.EqualFold(value, startupCommand(record)), nil
}

func startupCommand(record installRecord) string {
	return windows.ComposeCommandLine([]string{
		record.ExecutablePath, "internal-user-task-run", "--install-dir", record.InstallDir,
	})
}
