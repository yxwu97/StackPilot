// Package workspace implements registration and refresh use cases for trusted workspaces.
package workspace

import "errors"

var (
	// ErrNotFound identifies an unknown workspace ID.
	ErrNotFound = errors.New("workspace not found")
	// ErrAlreadyRegistered identifies a canonical path already present in the catalog.
	ErrAlreadyRegistered = errors.New("workspace is already registered")
	// ErrSystemChanged identifies a refresh that changes the workspace system identity.
	ErrSystemChanged = errors.New("workspace system identity changed")
	// ErrManifestUnavailable identifies a fixed manifest that cannot be discovered or read.
	ErrManifestUnavailable = errors.New("workspace manifest is unavailable")
	// ErrWorkspaceRequired identifies an ambiguous system registered from multiple workspaces.
	ErrWorkspaceRequired = errors.New("workspace ID is required for this system")
	// ErrPathInvalid identifies a workspace root that is missing, unreadable, or not a directory.
	ErrPathInvalid = errors.New("workspace path is invalid")
	// ErrDraftNotFound identifies an unknown or expired workspace draft.
	ErrDraftNotFound = errors.New("workspace draft not found")
	// ErrDraftExpired identifies a draft whose bounded lifetime elapsed.
	ErrDraftExpired = errors.New("workspace draft expired")
	// ErrImportAlreadyActive identifies a canonical path with an active import mutation.
	ErrImportAlreadyActive = errors.New("workspace import operation is already active")
	// ErrImportOperationNotFound identifies an unknown pre-registration Operation.
	ErrImportOperationNotFound = errors.New("workspace import operation not found")
	// ErrImportSourceChanged identifies source changes between analysis and apply.
	ErrImportSourceChanged = errors.New("workspace import source changed")
	// ErrManifestConflict identifies an unexpected fixed manifest at apply time.
	ErrManifestConflict = errors.New("workspace manifest conflicts with the draft")
	// ErrManifestWriteFailed identifies a failed atomic manifest publication.
	ErrManifestWriteFailed = errors.New("workspace manifest write failed")
	// ErrEditRuntimeActive identifies an edit apply attempted while runtime state is active.
	ErrEditRuntimeActive = errors.New("workspace edit is blocked by active runtime state")
	// ErrUnregisterRuntimeActive identifies an unregister attempted while runtime state is active.
	ErrUnregisterRuntimeActive = errors.New("workspace unregister is blocked by active runtime state")
	// ErrRelinkSystemMismatch identifies a target root with a different system identity.
	ErrRelinkSystemMismatch = errors.New("workspace relink system identity mismatch")
)

const CodeManifestUnavailable = "WORKSPACE_MANIFEST_UNAVAILABLE"
