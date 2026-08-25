// Package process implements the native process Driver.
package process

import (
	"errors"

	base "stackpilot/internal/driver"
)

var (
	// ErrInvalidSpec indicates a malformed or out-of-bound resolved specification.
	ErrInvalidSpec = errors.New("process Driver specification is invalid")
	// ErrFeatureNotEnabled identifies a recognized later-phase process mode.
	ErrFeatureNotEnabled = errors.New("process Driver feature is not enabled")
	// ErrPlatformUnsupported indicates that the native Driver is disabled on this platform.
	ErrPlatformUnsupported = errors.New("process Driver platform is unsupported")
	// ErrSupervisorUnavailable indicates a private Supervisor communication failure.
	ErrSupervisorUnavailable = errors.New("process Supervisor is unavailable")
	// ErrAlreadyRunning indicates that the service is already owned by the Supervisor.
	ErrAlreadyRunning = errors.New("process service is already running")
	// ErrRuntimeNotFound indicates that the Supervisor does not own the requested service.
	ErrRuntimeNotFound = base.ErrRuntimeNotFound
	// ErrIdentityMismatch indicates that runtime ownership could not be proved.
	ErrIdentityMismatch = base.ErrIdentityMismatch
)
