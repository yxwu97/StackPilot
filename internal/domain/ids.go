package domain

import (
	"regexp"
	"strconv"
)

const (
	workspaceIDPrefix       = "ws_"
	systemInstanceIDPrefix  = "si_"
	serviceInstanceIDPrefix = "svi_"
	operationIDPrefix       = "op_"
	portPlanIDPrefix        = "pp_"
	portLeaseIDPrefix       = "pl_"
	incidentIDPrefix        = "inc_"
	revisionIDPrefix        = "rev_"
	changePlanIDPrefix      = "plan_"
)

var (
	nameIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	ulidPattern   = regexp.MustCompile(`^[0-7][0-9A-HJKMNP-TV-Z]{25}$`)
)

// SystemID identifies a system definition within a manifest.
type SystemID string

// ParseSystemID validates and returns a SystemID.
func ParseSystemID(value string) (SystemID, error) {
	if !nameIDPattern.MatchString(value) {
		return "", newInvalidValue("systemId", value, ErrInvalidIdentifier)
	}
	return SystemID(value), nil
}

// String returns the canonical manifest identifier.
func (id SystemID) String() string { return string(id) }

// ServiceID identifies a service definition within a system.
type ServiceID string

// ParseServiceID validates and returns a ServiceID.
func ParseServiceID(value string) (ServiceID, error) {
	if !nameIDPattern.MatchString(value) {
		return "", newInvalidValue("serviceId", value, ErrInvalidIdentifier)
	}
	return ServiceID(value), nil
}

// String returns the canonical manifest identifier.
func (id ServiceID) String() string { return string(id) }

// WorkspaceID identifies a registered workspace.
type WorkspaceID string

// ParseWorkspaceID validates and returns a prefixed ULID workspace identifier.
func ParseWorkspaceID(value string) (WorkspaceID, error) {
	if err := validatePrefixedULID("workspaceId", value, workspaceIDPrefix); err != nil {
		return "", err
	}
	return WorkspaceID(value), nil
}

// String returns the canonical identifier.
func (id WorkspaceID) String() string { return string(id) }

// SystemInstanceID identifies one system runtime instance.
type SystemInstanceID string

// ParseSystemInstanceID validates and returns a prefixed ULID instance identifier.
func ParseSystemInstanceID(value string) (SystemInstanceID, error) {
	if err := validatePrefixedULID("instanceId", value, systemInstanceIDPrefix); err != nil {
		return "", err
	}
	return SystemInstanceID(value), nil
}

// String returns the canonical identifier.
func (id SystemInstanceID) String() string { return string(id) }

// ServiceInstanceID identifies one concrete service runtime.
type ServiceInstanceID string

// ParseServiceInstanceID validates and returns a prefixed ULID service-instance identifier.
func ParseServiceInstanceID(value string) (ServiceInstanceID, error) {
	if err := validatePrefixedULID("serviceInstanceId", value, serviceInstanceIDPrefix); err != nil {
		return "", err
	}
	return ServiceInstanceID(value), nil
}

// String returns the canonical identifier.
func (id ServiceInstanceID) String() string { return string(id) }

// OperationID identifies a persisted operation.
type OperationID string

// ParseOperationID validates and returns a prefixed ULID operation identifier.
func ParseOperationID(value string) (OperationID, error) {
	if err := validatePrefixedULID("operationId", value, operationIDPrefix); err != nil {
		return "", err
	}
	return OperationID(value), nil
}

// String returns the canonical identifier.
func (id OperationID) String() string { return string(id) }

// PortPlanID identifies one immutable whole-system port plan.
type PortPlanID string

// ParsePortPlanID validates and returns a prefixed ULID port-plan identifier.
func ParsePortPlanID(value string) (PortPlanID, error) {
	if err := validatePrefixedULID("portPlanId", value, portPlanIDPrefix); err != nil {
		return "", err
	}
	return PortPlanID(value), nil
}

// String returns the canonical identifier.
func (id PortPlanID) String() string { return string(id) }

// PortLeaseID identifies one persisted logical-port lease.
type PortLeaseID string

// ParsePortLeaseID validates and returns a prefixed ULID port-lease identifier.
func ParsePortLeaseID(value string) (PortLeaseID, error) {
	if err := validatePrefixedULID("portLeaseId", value, portLeaseIDPrefix); err != nil {
		return "", err
	}
	return PortLeaseID(value), nil
}

// String returns the canonical identifier.
func (id PortLeaseID) String() string { return string(id) }

// IncidentID identifies a Phase 2 incident without enabling incident behavior.
type IncidentID string

// ParseIncidentID validates and returns a prefixed ULID incident identifier.
func ParseIncidentID(value string) (IncidentID, error) {
	if err := validatePrefixedULID("incidentId", value, incidentIDPrefix); err != nil {
		return "", err
	}
	return IncidentID(value), nil
}

// String returns the canonical identifier.
func (id IncidentID) String() string { return string(id) }

// RevisionID identifies one immutable system revision snapshot.
type RevisionID string

// ParseRevisionID validates and returns a prefixed ULID revision identifier.
func ParseRevisionID(value string) (RevisionID, error) {
	if err := validatePrefixedULID("revisionId", value, revisionIDPrefix); err != nil {
		return "", err
	}
	return RevisionID(value), nil
}

// String returns the canonical identifier.
func (id RevisionID) String() string { return string(id) }

// ChangePlanID identifies one immutable running-to-workspace comparison.
type ChangePlanID string

// ParseChangePlanID validates and returns a prefixed ULID change-plan identifier.
func ParseChangePlanID(value string) (ChangePlanID, error) {
	if err := validatePrefixedULID("changePlanId", value, changePlanIDPrefix); err != nil {
		return "", err
	}
	return ChangePlanID(value), nil
}

// String returns the canonical identifier.
func (id ChangePlanID) String() string { return string(id) }

// EventID is a positive SQLite event sequence used as an SSE cursor.
type EventID int64

// NewEventID validates and returns an EventID.
func NewEventID(value int64) (EventID, error) {
	if value <= 0 {
		return 0, newInvalidValue("eventId", strconv.FormatInt(value, 10), ErrInvalidIdentifier)
	}
	return EventID(value), nil
}

func validatePrefixedULID(field, value, prefix string) error {
	if len(value) <= len(prefix) || value[:len(prefix)] != prefix || !ulidPattern.MatchString(value[len(prefix):]) {
		return newInvalidValue(field, value, ErrInvalidIdentifier)
	}
	return nil
}
