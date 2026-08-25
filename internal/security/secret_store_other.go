//go:build !windows

package security

import (
	"fmt"
	"time"
)

// NewOSSecretProvider remains gated until Phase 3 defines each platform store.
func NewOSSecretProvider(string, SecretMetadataStore, func() time.Time) (SecretProvider, error) {
	return nil, fmt.Errorf("OS Secret storage is not enabled on this platform")
}
