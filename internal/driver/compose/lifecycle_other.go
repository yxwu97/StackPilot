//go:build !windows

package compose

import (
	"context"
	"fmt"
)

// NewLifecycle constructs a platform-gated Compose lifecycle adapter.
func NewLifecycle(config LifecycleConfig) (*Lifecycle, error) {
	return &Lifecycle{docker: config.DockerExecutable, environment: config.Environment, run: config.Run, preflight: config.Preflight, startLog: config.StartLog}, nil
}

// Preflight rejects non-Windows execution until Phase 3.
func (*Lifecycle) Preflight(context.Context, PreflightRequest) (*PreflightResult, error) {
	return nil, fmt.Errorf("%w: Phase 2 Compose requires Windows", ErrPlatformUnsupported)
}

// Start rejects non-Windows execution until Phase 3.
func (*Lifecycle) Start(context.Context, LifecycleRequest) (ProjectIdentity, error) {
	return ProjectIdentity{}, fmt.Errorf("%w: Phase 2 Compose requires Windows", ErrPlatformUnsupported)
}

// StartWithoutBuild rejects non-Windows execution until Phase 3.
func (*Lifecycle) StartWithoutBuild(context.Context, LifecycleRequest) (ProjectIdentity, error) {
	return ProjectIdentity{}, fmt.Errorf("%w: Phase 2 Compose requires Windows", ErrPlatformUnsupported)
}

// Prepare rejects non-Windows execution until Phase 3.
func (*Lifecycle) Prepare(context.Context, LifecycleRequest) (ProjectIdentity, error) {
	return ProjectIdentity{}, fmt.Errorf("%w: Phase 2 Compose requires Windows", ErrPlatformUnsupported)
}

// Build rejects non-Windows execution until Phase 3.
func (*Lifecycle) Build(context.Context, ProjectIdentity) error {
	return fmt.Errorf("%w: Phase 2 Compose requires Windows", ErrPlatformUnsupported)
}

// Up rejects non-Windows execution until Phase 3.
func (*Lifecycle) Up(context.Context, ProjectIdentity) error {
	return fmt.Errorf("%w: Phase 2 Compose requires Windows", ErrPlatformUnsupported)
}

// Inspect rejects non-Windows execution until Phase 3.
func (*Lifecycle) Inspect(context.Context, ProjectIdentity) (ProjectObservation, error) {
	return ProjectObservation{}, fmt.Errorf("%w: Phase 2 Compose requires Windows", ErrPlatformUnsupported)
}

// ObserveResources reports the explicit non-Windows capability gate.
func (*Lifecycle) ObserveResources(context.Context, string) (ResourceObservation, error) {
	return ResourceObservation{}, ErrPlatformUnsupported
}

// Stop rejects non-Windows execution until Phase 3.
func (*Lifecycle) Stop(context.Context, ProjectIdentity) error {
	return fmt.Errorf("%w: Phase 2 Compose requires Windows", ErrPlatformUnsupported)
}

// FollowLogs rejects non-Windows execution until Phase 3.
func (*Lifecycle) FollowLogs(context.Context, LogFollowRequest) (*LogSession, error) {
	return nil, fmt.Errorf("%w: Phase 2 Compose requires Windows", ErrPlatformUnsupported)
}

// Recover rejects non-Windows execution until Phase 3.
func (*Lifecycle) Recover(context.Context, string) (ProjectIdentity, ProjectObservation, error) {
	return ProjectIdentity{}, ProjectObservation{}, fmt.Errorf("%w: Phase 2 Compose requires Windows", ErrPlatformUnsupported)
}

// Discover rejects non-Windows execution until Phase 3.
func (*Lifecycle) Discover(context.Context, LifecycleRequest) (ProjectIdentity, ProjectObservation, error) {
	return ProjectIdentity{}, ProjectObservation{}, fmt.Errorf("%w: Phase 2 Compose requires Windows", ErrPlatformUnsupported)
}

// Close is idempotent for an unstarted platform-gated session.
func (session *LogSession) Close() error {
	if session == nil || session.cancel == nil {
		return nil
	}
	session.cancel()
	<-session.done
	return session.err
}
