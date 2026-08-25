//go:build windows

package usertask

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/windows"

	"stackpilot/internal/security"
)

func removeInstallation(ctx context.Context, record installRecord) error {
	verified, err := loadRecord(record.InstallDir)
	if err != nil || verified.SHA256 != record.SHA256 || verified.InstallationID != record.InstallationID {
		return fmt.Errorf("installation marker changed before removal")
	}
	if !safeInstallRoot(record.InstallDir) {
		return fmt.Errorf("registered installation root is unsafe to remove")
	}
	current, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve current executable for uninstall: %w", err)
	}
	current, err = security.CanonicalExistingPath(current)
	if err != nil {
		return err
	}
	inside, err := security.PathWithinRoot(record.InstallDir, current)
	if err != nil {
		return err
	}
	if inside {
		return scheduleRemoval(record.InstallDir, record.DataDir, record.InstallationID)
	}
	tombstone := filepath.Join(filepath.Dir(record.InstallDir), "."+filepath.Base(record.InstallDir)+".uninstall-"+record.InstallationID)
	if err := os.Rename(record.InstallDir, tombstone); err != nil {
		return fmt.Errorf("isolate verified installation for removal: %w", err)
	}
	return removeAllWithRetry(ctx, tombstone)
}

func removeAllWithRetry(ctx context.Context, path string) error {
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := os.RemoveAll(path); err == nil {
			return nil
		} else if waitCtx.Err() != nil {
			return fmt.Errorf("remove verified installation: %w", err)
		}
		select {
		case <-waitCtx.Done():
		case <-ticker.C:
		}
	}
}

func scheduleRemoval(installRoot, dataRoot, installationID string) error {
	logPath := filepath.Join(dataRoot, "uninstall-cleanup.log")
	if err := os.Remove(logPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("reset uninstall cleanup handshake: %w", err)
	}
	scriptPath := filepath.Join(dataRoot, ".stackpilot-uninstall-"+installationID+".ps1")
	if err := writeCleanupScript(scriptPath, cleanupScript(installRoot, logPath)); err != nil {
		return err
	}
	if err := shellExecutePowerShell(scriptPath); err != nil {
		_ = os.Remove(scriptPath)
		return err
	}
	if err := waitForCleanupHandshake(logPath); err != nil {
		_ = os.Remove(scriptPath)
		return err
	}
	return nil
}

func cleanupScript(installRoot, logPath string) string {
	encodedPath := base64.StdEncoding.EncodeToString([]byte(installRoot))
	encodedLog := base64.StdEncoding.EncodeToString([]byte(logPath))
	return strings.Join([]string{
		"$ErrorActionPreference='Stop'",
		"$p=[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('" + encodedPath + "'))",
		"$log=[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('" + encodedLog + "'))",
		"$self=$MyInvocation.MyCommand.Path",
		"try{Set-Content -LiteralPath $log -Value 'started';Wait-Process -Id " + strconv.Itoa(os.Getpid()) + " -ErrorAction SilentlyContinue;Set-Content -LiteralPath $log -Value 'parent-exited';Remove-Item -LiteralPath $p -Recurse -Force;Set-Content -LiteralPath $log -Value 'completed';Remove-Item -LiteralPath $self -Force}catch{Set-Content -LiteralPath $log -Value ('failed: '+$_.Exception.Message);exit 1}",
	}, ";")
}

func writeCleanupScript(path, script string) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create uninstall cleanup script: %w", err)
	}
	_, writeErr := file.WriteString(script)
	if err := file.Close(); err != nil && writeErr == nil {
		writeErr = err
	}
	if writeErr != nil {
		_ = os.Remove(path)
		return fmt.Errorf("write uninstall cleanup script: %w", writeErr)
	}
	return nil
}

func shellExecutePowerShell(scriptPath string) error {
	windowsRoot, err := windows.GetSystemWindowsDirectory()
	if err != nil {
		return fmt.Errorf("resolve Windows directory for uninstall cleanup: %w", err)
	}
	powershell := filepath.Join(windowsRoot, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	commandLine := windows.ComposeCommandLine([]string{"placeholder", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-File", scriptPath})
	parameters := strings.TrimPrefix(commandLine, "placeholder ")
	verb, _ := windows.UTF16PtrFromString("open")
	file, _ := windows.UTF16PtrFromString(powershell)
	args, _ := windows.UTF16PtrFromString(parameters)
	if err := windows.ShellExecute(0, verb, file, args, nil, windows.SW_HIDE); err != nil {
		return fmt.Errorf("start verified uninstall cleanup: %w", err)
	}
	return nil
}

func waitForCleanupHandshake(logPath string) error {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		payload, err := os.ReadFile(logPath)
		if err == nil && strings.TrimSpace(string(payload)) == "started" {
			return nil
		}
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("read uninstall cleanup handshake: %w", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("uninstall cleanup did not acknowledge startup")
}

func safeInstallRoot(path string) bool {
	cleaned := filepath.Clean(path)
	volume := filepath.VolumeName(cleaned)
	if cleaned == volume+string(filepath.Separator) || filepath.Dir(cleaned) == cleaned {
		return false
	}
	return filepath.Base(cleaned) != "." && filepath.Base(cleaned) != string(filepath.Separator)
}
