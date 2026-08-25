//go:build !windows

package process

import (
	"context"
	"fmt"

	base "stackpilot/internal/driver"
)

// Config is retained for cross-platform construction while Phase 3 is disabled.
type Config struct {
	BaselineEnvironment map[string]string
}

// Driver is the explicit non-Windows capability gate.
type Driver struct{}

var _ base.Driver = (*Driver)(nil)

// New constructs a platform-gated Process Driver.
func New(Config) *Driver { return &Driver{} }

// Preflight rejects non-Windows execution until Phase 3.
func (*Driver) Preflight(context.Context, base.ResolvedServiceSpec) error { return unsupported() }

// Start rejects non-Windows execution until Phase 3.
func (*Driver) Start(context.Context, base.StartRequest) (base.RuntimeIdentity, error) {
	return base.RuntimeIdentity{}, unsupported()
}

// Stop rejects non-Windows execution until Phase 3.
func (*Driver) Stop(context.Context, base.StopRequest) error { return unsupported() }

// Inspect rejects non-Windows execution until Phase 3.
func (*Driver) Inspect(context.Context, base.RuntimeIdentity) (base.RuntimeObservation, error) {
	return base.RuntimeObservation{}, unsupported()
}

// Recover rejects non-Windows execution until Phase 3.
func (*Driver) Recover(context.Context, base.RuntimeIdentity) (base.RecoveredRuntime, error) {
	return base.RecoveredRuntime{}, unsupported()
}

// Discover rejects non-Windows recovery until Phase 3.
func (*Driver) Discover(context.Context, base.DiscoveryRequest) (base.RecoveredRuntime, error) {
	return base.RecoveredRuntime{}, unsupported()
}

func unsupported() error {
	return fmt.Errorf("%w: Phase 1 process Driver requires Windows", ErrPlatformUnsupported)
}
