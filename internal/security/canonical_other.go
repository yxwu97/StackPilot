//go:build !windows

package security

import "path/filepath"

func canonicalizeExistingPath(path string) (string, error) {
	return filepath.EvalSymlinks(path)
}
