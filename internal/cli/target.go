package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func resolveWorkspace(ctx context.Context, client *apiClient, requestedSystem string) (workspaceDTO, error) {
	var list workspaceListDTO
	if err := client.JSON(ctx, "GET", "/api/v1/workspaces", nil, &list); err != nil {
		return workspaceDTO{}, err
	}
	if root, err := currentManifestRoot(); err == nil {
		for _, workspace := range list.Items {
			if samePath(root, workspace.Path) && (requestedSystem == "" || requestedSystem == workspace.SystemID) {
				return workspace, nil
			}
		}
	}
	if requestedSystem == "" {
		return workspaceDTO{}, commandErrorf("current directory is not a registered StackPilot workspace")
	}
	matches := make([]workspaceDTO, 0, 1)
	for _, workspace := range list.Items {
		if workspace.SystemID == requestedSystem {
			matches = append(matches, workspace)
		}
	}
	if len(matches) != 1 {
		return workspaceDTO{}, commandErrorf("system %q requires one unambiguous registered workspace", requestedSystem)
	}
	return matches[0], nil
}

func currentManifestRoot() (string, error) {
	directory, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		manifest := filepath.Join(directory, ".stackpilot", "system.yaml")
		if info, statErr := os.Stat(manifest); statErr == nil && info.Mode().IsRegular() {
			return canonicalPath(directory)
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return "", fmt.Errorf("fixed StackPilot manifest was not found")
		}
		directory = parent
	}
}

func canonicalPath(value string) (string, error) {
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	return filepath.Clean(resolved), nil
}

func samePath(left, right string) bool {
	canonicalRight, err := canonicalPath(right)
	if err != nil {
		return false
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(left), canonicalRight)
	}
	return filepath.Clean(left) == canonicalRight
}
