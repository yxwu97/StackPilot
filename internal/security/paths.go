// Package security owns shared trust-boundary validation.
package security

import (
	"fmt"
	"path/filepath"
	"strings"
)

// CanonicalExistingPath resolves links and junctions for an existing path.
func CanonicalExistingPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("make path absolute: %w", err)
	}
	canonical, err := canonicalizeExistingPath(absolute)
	if err != nil {
		return "", fmt.Errorf("canonicalize existing path: %w", err)
	}
	return filepath.Clean(canonical), nil
}

// PathWithinRoot reports whether candidate is root or one of its descendants.
func PathWithinRoot(root, candidate string) (bool, error) {
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return false, fmt.Errorf("compare path with root: %w", err)
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return false, nil
	}
	return true, nil
}
