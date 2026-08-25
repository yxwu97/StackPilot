// Package driver defines single-service runtime adapter contracts.
package driver

import (
	"context"
	"time"

	"stackpilot/internal/domain"
	"stackpilot/internal/runner"
)

// RuntimeIdentity is the domain identity returned after process creation.
type RuntimeIdentity = domain.ProcessIdentity

// ResolvedServiceSpec is one immutable, server-resolved process specification.
type ResolvedServiceSpec struct {
	ServiceID        domain.ServiceID
	Driver           domain.DriverKind
	Mode             domain.ProcessMode
	WorkspaceRoot    string
	InstanceDir      string
	WorkingDirectory string
	Command          runner.ResolvedCommand
	Arguments        []string
	Environment      map[string]string
	SecretReferences map[string]string
	StdoutPath       string
	StderrPath       string
	GracefulTimeout  time.Duration
}

// StartRequest contains the immutable service specification to create.
type StartRequest struct {
	Spec ResolvedServiceSpec
}

// StopRequest identifies one runtime and its bounded graceful-stop policy.
type StopRequest struct {
	Identity        RuntimeIdentity
	GracefulTimeout time.Duration
}

// RuntimeObservation is the latest single-service platform observation.
type RuntimeObservation struct {
	State    string
	Identity RuntimeIdentity
	ExitCode *uint32
	Forced   bool
}

// RecoveredRuntime is a verified runtime reattached after control-plane restart.
type RecoveredRuntime struct {
	Identity    RuntimeIdentity
	Observation RuntimeObservation
}

// DiscoveryRequest identifies the fixed instance location used for crash-window recovery.
type DiscoveryRequest struct {
	InstanceDir string
	ServiceID   domain.ServiceID
}

// RuntimeDiscoverer proves a runtime from Supervisor and service identity files.
type RuntimeDiscoverer interface {
	Discover(context.Context, DiscoveryRequest) (RecoveredRuntime, error)
}

// Driver controls one service and never mutates aggregate system state.
type Driver interface {
	Preflight(context.Context, ResolvedServiceSpec) error
	Start(context.Context, StartRequest) (RuntimeIdentity, error)
	Stop(context.Context, StopRequest) error
	Inspect(context.Context, RuntimeIdentity) (RuntimeObservation, error)
	Recover(context.Context, RuntimeIdentity) (RecoveredRuntime, error)
}
