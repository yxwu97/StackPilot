//go:build windows

package manifest

import (
	"stackpilot/internal/security"
)

func canonicalizeExistingPath(path string) (string, error) {
	return security.CanonicalExistingPath(path)
}
