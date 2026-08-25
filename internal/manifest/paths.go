package manifest

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"stackpilot/internal/security"
)

// CanonicalWorkspaceRoot resolves links and verifies that path is an existing directory.
func CanonicalWorkspaceRoot(path string) (string, error) {
	if path == "" {
		return "", newValidationError("$.workspace", "path", ErrSemanticInvalid)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve workspace root: %w", err)
	}
	canonical, err := canonicalizeExistingPath(absolute)
	if err != nil {
		return "", newValidationError("$.workspace", "path", ErrSemanticInvalid)
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.IsDir() {
		return "", newValidationError("$.workspace", "path", ErrSemanticInvalid)
	}
	return filepath.Clean(canonical), nil
}

// ResolveWorkspaceFile resolves a fixed relative file and rejects link escapes.
func ResolveWorkspaceFile(root, relative, logicalPath string) (string, error) {
	if relative == "" || filepath.IsAbs(relative) || containsTemplateSyntax(relative) {
		return "", newValidationError(logicalPath, "", ErrPathOutsideWorkspace)
	}
	canonical, err := canonicalizeExistingPath(filepath.Join(root, relative))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("workspace file does not exist: %w", os.ErrNotExist)
		}
		if errors.Is(err, os.ErrPermission) {
			return "", fmt.Errorf("workspace file is not readable: %w", os.ErrPermission)
		}
		return "", newValidationError(logicalPath, "", ErrSemanticInvalid)
	}
	inside, err := security.PathWithinRoot(root, filepath.Clean(canonical))
	if err != nil {
		return "", fmt.Errorf("compare workspace file path: %w", err)
	}
	if !inside {
		return "", newValidationError(logicalPath, "", ErrPathOutsideWorkspace)
	}
	return filepath.Clean(canonical), nil
}

func resolveWorkspaceDirectory(root, relative, logicalPath string) (string, error) {
	if relative == "" || filepath.IsAbs(relative) || containsTemplateSyntax(relative) {
		return "", newValidationError(logicalPath, "", ErrPathOutsideWorkspace)
	}
	joined := filepath.Join(root, relative)
	canonical, err := canonicalizeExistingPath(joined)
	if err != nil {
		return "", newValidationError(logicalPath, "", ErrSemanticInvalid)
	}
	canonical = filepath.Clean(canonical)
	inside, err := security.PathWithinRoot(root, canonical)
	if err != nil {
		return "", fmt.Errorf("compare workspace path: %w", err)
	}
	if !inside {
		return "", newValidationError(logicalPath, "", ErrPathOutsideWorkspace)
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.IsDir() {
		return "", newValidationError(logicalPath, "", ErrSemanticInvalid)
	}
	return canonical, nil
}
