//go:build !windows

package runner

import (
	"context"
	"fmt"
)

// Resolver is disabled until formal Phase 3 platform support.
type Resolver struct{}

// NewResolver constructs a platform-gated resolver.
func NewResolver(Config) (*Resolver, error) { return &Resolver{}, nil }

// Resolve rejects unsupported platform execution.
func (*Resolver) Resolve(context.Context, ResolveRequest) (*ResolvedCommand, error) {
	return nil, fmt.Errorf("%w: Phase 1 runner resolution requires Windows", ErrPlatformUnsupported)
}
