//go:build !windows

package security

import "fmt"

// NewOSTokenStore is gated until Phase 3 defines a non-Windows secure store.
func NewOSTokenStore(string) (TokenStore, error) {
	return nil, fmt.Errorf("OS token storage is not enabled on this platform")
}
