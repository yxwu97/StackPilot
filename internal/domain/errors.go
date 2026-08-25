package domain

import (
	"errors"
	"fmt"
)

var (
	// ErrInvalidIdentifier classifies malformed domain identifiers.
	ErrInvalidIdentifier = errors.New("invalid identifier")
	// ErrInvalidTimestamp classifies invalid persistent timestamps.
	ErrInvalidTimestamp = errors.New("invalid timestamp")
	// ErrInvalidEnumValue classifies values outside a domain enumeration.
	ErrInvalidEnumValue = errors.New("invalid enum value")
)

// InvalidValueError describes a rejected domain value without assigning an API error code.
type InvalidValueError struct {
	Field string
	Value string
	cause error
}

func newInvalidValue(field, value string, cause error) *InvalidValueError {
	return &InvalidValueError{Field: field, Value: value, cause: cause}
}

// Error implements error.
func (e *InvalidValueError) Error() string {
	return fmt.Sprintf("%s %q: %v", e.Field, e.Value, e.cause)
}

// Unwrap exposes the stable error category to errors.Is.
func (e *InvalidValueError) Unwrap() error {
	return e.cause
}
