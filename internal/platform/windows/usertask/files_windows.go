//go:build windows

package usertask

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"stackpilot/internal/security"
)

func stageExecutable(source, installRoot, checksum string) (string, error) {
	sourcePath, err := security.CanonicalExistingPath(source)
	if err != nil {
		return "", fmt.Errorf("canonicalize source executable: %w", err)
	}
	info, err := os.Stat(sourcePath)
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("source executable is not a regular file")
	}
	versionDir := filepath.Join(installRoot, "versions", checksum)
	if err := os.MkdirAll(versionDir, 0o700); err != nil {
		return "", fmt.Errorf("create version directory: %w", err)
	}
	target := filepath.Join(versionDir, "stackpilot.exe")
	if existing, err := hashFile(target); err == nil && strings.EqualFold(existing, checksum) {
		return security.CanonicalExistingPath(target)
	}
	if err := copyFileAtomic(sourcePath, target); err != nil {
		return "", err
	}
	actual, err := hashFile(target)
	if err != nil || !strings.EqualFold(actual, checksum) {
		return "", fmt.Errorf("staged executable checksum mismatch")
	}
	return security.CanonicalExistingPath(target)
}

func copyFileAtomic(source, target string) error {
	input, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open source executable: %w", err)
	}
	defer input.Close()
	temporary, err := os.CreateTemp(filepath.Dir(target), ".stackpilot-*.tmp")
	if err != nil {
		return fmt.Errorf("create staged executable: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := io.Copy(temporary, input); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("copy staged executable: %w", err)
	}
	if err := errors.Join(temporary.Sync(), temporary.Close()); err != nil {
		return fmt.Errorf("flush staged executable: %w", err)
	}
	return replaceFile(temporaryPath, target)
}
