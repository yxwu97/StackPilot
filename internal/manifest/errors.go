// Package manifest loads and validates the declarative system manifest boundary.
package manifest

import (
	"errors"
	"fmt"
)

var (
	// ErrFileTooLarge classifies manifests larger than the fixed input limit.
	ErrFileTooLarge = errors.New("manifest file is too large")
	// ErrNotRegularFile classifies manifest paths that are not regular files.
	ErrNotRegularFile = errors.New("manifest is not a regular file")
	// ErrMalformedYAML classifies YAML syntax and scalar decoding failures.
	ErrMalformedYAML = errors.New("manifest YAML is malformed")
	// ErrDuplicateKey classifies repeated keys in any YAML mapping.
	ErrDuplicateKey = errors.New("manifest contains a duplicate key")
	// ErrUnknownField classifies fields outside the typed manifest contract.
	ErrUnknownField = errors.New("manifest contains an unknown field")
	// ErrMultipleDocuments classifies YAML streams containing more than one document.
	ErrMultipleDocuments = errors.New("manifest contains multiple YAML documents")
	// ErrSchemaInvalid classifies JSON Schema validation failures.
	ErrSchemaInvalid = errors.New("manifest does not match its schema")
	// ErrSemanticInvalid classifies inconsistent values across manifest fields.
	ErrSemanticInvalid = errors.New("manifest semantics are invalid")
	// ErrPathOutsideWorkspace classifies paths that escape the canonical workspace.
	ErrPathOutsideWorkspace = errors.New("manifest path is outside the workspace")
	// ErrDependencyCycle classifies cyclic service dependencies.
	ErrDependencyCycle = errors.New("manifest service dependency cycle")
	// ErrReferenceNotFound classifies references to undeclared services or ports.
	ErrReferenceNotFound = errors.New("manifest reference was not found")
	// ErrTemplateInvalid classifies values outside the restricted template language.
	ErrTemplateInvalid = errors.New("manifest template is invalid")
	// ErrDurationInvalid classifies unsafe or inconsistent duration values.
	ErrDurationInvalid = errors.New("manifest duration is invalid")
	// ErrPortRangeInvalid classifies invalid logical port candidate ranges.
	ErrPortRangeInvalid = errors.New("manifest port range is invalid")
	// ErrHealthTargetUnsafe classifies readiness targets outside loopback.
	ErrHealthTargetUnsafe = errors.New("manifest health target is unsafe")
	// ErrFeatureNotEnabled classifies recognized capabilities unavailable in this phase.
	ErrFeatureNotEnabled = errors.New("manifest feature is not enabled")
)

// ValidationError identifies a safe logical manifest location and error category.
type ValidationError struct {
	Path  string
	Field string
	cause error
}

func newValidationError(path, field string, cause error) *ValidationError {
	return &ValidationError{Path: path, Field: field, cause: cause}
}

// Error implements error without exposing manifest values or host paths.
func (e *ValidationError) Error() string {
	if e.Field != "" {
		return fmt.Sprintf("%s: field %q: %v", e.Path, e.Field, e.cause)
	}
	return fmt.Sprintf("%s: %v", e.Path, e.cause)
}

// Unwrap exposes the stable validation category.
func (e *ValidationError) Unwrap() error { return e.cause }

// FeatureError identifies a recognized but disabled manifest capability.
type FeatureError struct {
	Path    string
	Feature string
}

// Error implements error using only contract-level field and capability names.
func (e *FeatureError) Error() string {
	return fmt.Sprintf("%s: feature %q: %v", e.Path, e.Feature, ErrFeatureNotEnabled)
}

// Unwrap exposes ErrFeatureNotEnabled.
func (e *FeatureError) Unwrap() error { return ErrFeatureNotEnabled }
