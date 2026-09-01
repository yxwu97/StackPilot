package domain

import (
	"fmt"
	"io"
	"math/big"
	"time"
)

const crockfordBase32 = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// NewWorkspaceID generates a canonical ULID-backed workspace identifier.
func NewWorkspaceID(now time.Time, entropy io.Reader) (WorkspaceID, error) {
	encoded, err := generateULID(now, entropy)
	if err != nil {
		return "", err
	}
	return ParseWorkspaceID(workspaceIDPrefix + encoded)
}

// NewOperationID generates a canonical ULID-backed Operation identifier.
func NewOperationID(now time.Time, entropy io.Reader) (OperationID, error) {
	encoded, err := generateULID(now, entropy)
	if err != nil {
		return "", err
	}
	return ParseOperationID(operationIDPrefix + encoded)
}

// NewPortPlanID generates a canonical ULID-backed port-plan identifier.
func NewPortPlanID(now time.Time, entropy io.Reader) (PortPlanID, error) {
	encoded, err := generateULID(now, entropy)
	if err != nil {
		return "", err
	}
	return ParsePortPlanID(portPlanIDPrefix + encoded)
}

// NewPortLeaseID generates a canonical ULID-backed port-lease identifier.
func NewPortLeaseID(now time.Time, entropy io.Reader) (PortLeaseID, error) {
	encoded, err := generateULID(now, entropy)
	if err != nil {
		return "", err
	}
	return ParsePortLeaseID(portLeaseIDPrefix + encoded)
}

// NewSystemInstanceID generates a canonical ULID-backed system-instance identifier.
func NewSystemInstanceID(now time.Time, entropy io.Reader) (SystemInstanceID, error) {
	encoded, err := generateULID(now, entropy)
	if err != nil {
		return "", err
	}
	return ParseSystemInstanceID(systemInstanceIDPrefix + encoded)
}

// NewServiceInstanceID generates a canonical ULID-backed service-instance identifier.
func NewServiceInstanceID(now time.Time, entropy io.Reader) (ServiceInstanceID, error) {
	encoded, err := generateULID(now, entropy)
	if err != nil {
		return "", err
	}
	return ParseServiceInstanceID(serviceInstanceIDPrefix + encoded)
}

// NewIncidentID creates a time-sortable incident identifier.
func NewIncidentID(now time.Time, entropy io.Reader) (IncidentID, error) {
	encoded, err := generateULID(now, entropy)
	if err != nil {
		return "", err
	}
	return ParseIncidentID(incidentIDPrefix + encoded)
}

// NewRevisionID creates a time-sortable immutable revision identifier.
func NewRevisionID(now time.Time, entropy io.Reader) (RevisionID, error) {
	encoded, err := generateULID(now, entropy)
	if err != nil {
		return "", err
	}
	return ParseRevisionID(revisionIDPrefix + encoded)
}

// NewChangePlanID creates a time-sortable immutable change-plan identifier.
func NewChangePlanID(now time.Time, entropy io.Reader) (ChangePlanID, error) {
	encoded, err := generateULID(now, entropy)
	if err != nil {
		return "", err
	}
	return ParseChangePlanID(changePlanIDPrefix + encoded)
}

func generateULID(now time.Time, entropy io.Reader) (string, error) {
	if entropy == nil {
		return "", fmt.Errorf("ULID entropy source is required")
	}
	milliseconds := now.UTC().UnixMilli()
	if milliseconds < 0 || milliseconds >= 1<<48 {
		return "", fmt.Errorf("ULID timestamp is outside the 48-bit range")
	}
	value := make([]byte, 16)
	for index := 5; index >= 0; index-- {
		value[index] = byte(milliseconds)
		milliseconds >>= 8
	}
	if _, err := io.ReadFull(entropy, value[6:]); err != nil {
		return "", fmt.Errorf("read ULID entropy: %w", err)
	}
	return encodeULID(value), nil
}

func encodeULID(value []byte) string {
	number := new(big.Int).SetBytes(value)
	base := big.NewInt(32)
	remainder := new(big.Int)
	encoded := make([]byte, 26)
	for index := len(encoded) - 1; index >= 0; index-- {
		number.QuoRem(number, base, remainder)
		encoded[index] = crockfordBase32[remainder.Int64()]
	}
	return string(encoded)
}
