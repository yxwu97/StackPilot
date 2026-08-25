// Package orchestrator owns persisted Operation lifecycle and execution coordination.
package orchestrator

import "errors"

var (
	// ErrOperationNotFound identifies an unknown Operation ID.
	ErrOperationNotFound = errors.New("operation not found")
	// ErrOperationAlreadyActive identifies another active Operation for the workspace.
	ErrOperationAlreadyActive = errors.New("workspace already has an active operation")
	// ErrIdempotencyKeyReused identifies the same key with a different request digest.
	ErrIdempotencyKeyReused = errors.New("idempotency key was reused for another request")
	// ErrInvalidTransition identifies a state change outside the centralized state machine.
	ErrInvalidTransition = errors.New("operation state transition is invalid")
	// ErrNotCancellable identifies an Operation that cannot accept cancellation.
	ErrNotCancellable = errors.New("operation is not cancellable")
	// ErrStepNotFound identifies an unknown structured step.
	ErrStepNotFound = errors.New("operation step not found")
	// ErrInvalidInput identifies malformed Operation creation input.
	ErrInvalidInput = errors.New("operation input is invalid")
	// ErrInvalidDependencyGraph identifies an invalid runtime dependency graph.
	ErrInvalidDependencyGraph = errors.New("service dependency graph is invalid")
	// ErrManifestChanged prevents service restart across two manifest snapshots.
	ErrManifestChanged = errors.New("running instance manifest has changed")
	// ErrSystemAlreadyActive prevents a second start over a non-running active instance.
	ErrSystemAlreadyActive = errors.New("system instance is already active")
)
