package domain

import "time"

// ProcessIdentity contains the values required to verify a managed process.
// PlatformToken is opaque outside the platform adapter.
type ProcessIdentity struct {
	PID            int
	StartedAt      time.Time
	ExecutablePath string
	CommandDigest  string
	PlatformToken  string
}
