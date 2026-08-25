// Package ports owns deterministic whole-system port planning and lease lifecycles.
package ports

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"stackpilot/internal/domain"
)

var (
	// ErrInvalidInput identifies an invalid port requirement or override.
	ErrInvalidInput = errors.New("port plan input is invalid")
	// ErrExhausted indicates that no permitted candidate can be leased and probed.
	ErrExhausted = errors.New("port candidates exhausted")
	// ErrLeaseConflict identifies a database-enforced active endpoint conflict.
	ErrLeaseConflict = errors.New("active port lease conflicts with another plan")
	// ErrLeaseState identifies an invalid persisted lease state transition.
	ErrLeaseState = errors.New("port lease state transition is invalid")
)

// Range is one validated inclusive fallback range.
type Range struct {
	Start int
	End   int
}

// Requirement declares one logical TCP endpoint to plan.
type Requirement struct {
	LogicalName    string
	Protocol       string
	Host           string
	Preferred      *int
	Fallback       *Range
	ConflictPolicy string
}

// Input contains immutable sources for one whole-system planning attempt.
type Input struct {
	WorkspaceID       domain.WorkspaceID
	OperationID       domain.OperationID
	ManifestDigest    string
	Requirements      map[string]Requirement
	RequestOverrides  map[string]int
	WorkspaceOverride map[string]int
	Sticky            map[string]int
}

// Preferences contains trusted workspace and last-successful planning inputs.
type Preferences struct {
	Workspace map[string]int
	Sticky    map[string]int
}

// Assignment records the chosen endpoint and why it was selected.
type Assignment struct {
	LogicalName  string
	Protocol     string
	Host         string
	Port         int
	Source       string
	Replaced     bool
	ConflictPort *int
	LeaseID      domain.PortLeaseID
}

// Plan is an immutable assignment snapshot with owned probe listeners.
type Plan struct {
	ID          domain.PortPlanID
	WorkspaceID domain.WorkspaceID
	Assignments map[string]Assignment
	ExpiresAt   time.Time
	mutex       sync.Mutex
	probes      map[string]io.Closer
}

// LeaseState is the durable endpoint ownership lifecycle.
type LeaseState string

const (
	LeaseReserved LeaseState = "reserved"
	LeaseBound    LeaseState = "bound"
	LeaseReleased LeaseState = "released"
	LeaseExpired  LeaseState = "expired"
)

// Lease is one persisted logical endpoint reservation.
type Lease struct {
	ID             domain.PortLeaseID
	PlanID         domain.PortPlanID
	WorkspaceID    domain.WorkspaceID
	InstanceID     *domain.SystemInstanceID
	OperationID    domain.OperationID
	ManifestDigest string
	LogicalName    string
	Protocol       string
	Host           string
	Port           int
	State          LeaseState
	ExpiresAt      time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Reservation identifies the transaction scope shared by all leases in a plan.
type Reservation struct {
	PlanID         domain.PortPlanID
	WorkspaceID    domain.WorkspaceID
	OperationID    domain.OperationID
	ManifestDigest string
	Now            time.Time
	ExpiresAt      time.Time
}

// SelectLeases chooses leases while the store holds its immediate transaction.
type SelectLeases func(active []Lease) ([]Lease, error)

// ReservationStore atomically reads active leases and inserts a complete plan.
type ReservationStore interface {
	Reserve(context.Context, Reservation, SelectLeases) error
}
