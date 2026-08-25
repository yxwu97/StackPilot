//go:build windows

// Package platform contains operating-system-specific defaults and adapters.
package platform

import (
	"fmt"
	"os"
	"path/filepath"
)

// DefaultDataDir returns the Windows single-user data directory.
func DefaultDataDir() (string, error) {
	root := os.Getenv("LOCALAPPDATA")
	if root == "" {
		return "", fmt.Errorf("LOCALAPPDATA is not set")
	}
	return filepath.Join(root, "StackPilot"), nil
}
