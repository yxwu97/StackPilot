//go:build !windows

// Package platform contains operating-system-specific defaults and adapters.
package platform

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// DefaultDataDir returns a conventional per-user data directory on build-only platforms.
func DefaultDataDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home directory: %w", err)
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Application Support", "StackPilot"), nil
	}
	if root := os.Getenv("XDG_DATA_HOME"); root != "" {
		return filepath.Join(root, "stackpilot"), nil
	}
	return filepath.Join(home, ".local", "share", "stackpilot"), nil
}
