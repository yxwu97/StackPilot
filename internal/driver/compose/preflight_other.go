//go:build !windows

package compose

import (
	"context"
	"fmt"
)

// NewPreflighter constructs a platform-gated Compose preflight service.
func NewPreflighter(config Config) (*Preflighter, error) {
	return &Preflighter{docker: config.DockerExecutable, environment: config.Environment, timeout: config.Timeout, run: config.Run}, nil
}

// Preflight rejects non-Windows execution until Phase 3.
func (*Preflighter) Preflight(context.Context, PreflightRequest) (*PreflightResult, error) {
	return nil, fmt.Errorf("%w: Phase 2 Compose requires Windows", ErrPlatformUnsupported)
}
