package driver

import "errors"

var (
	// ErrRuntimeNotFound indicates that the managed runtime has exited or is no longer owned.
	ErrRuntimeNotFound = errors.New("managed runtime was not found")
	// ErrIdentityMismatch indicates that the supplied runtime identity could not be proved.
	ErrIdentityMismatch = errors.New("managed runtime identity mismatch")
)
