// Package revision builds immutable, sensitivity-bounded system revision snapshots.
package revision

import (
	"errors"
	"time"

	"stackpilot/internal/domain"
)

const (
	// SchemaVersion identifies the canonical revision snapshot contract.
	SchemaVersion = "revision/v1"
	// MaxSnapshotBytes bounds one persisted canonical snapshot.
	MaxSnapshotBytes = 4 << 20
)

var (
	ErrInvalidInput      = errors.New("revision input is invalid")
	ErrSourceChanged     = errors.New("revision source changed during collection")
	ErrSourceUnsafe      = errors.New("revision source is unsafe")
	ErrSourceTooLarge    = errors.New("revision source exceeds its limit")
	ErrSourceUnavailable = errors.New("revision source is unavailable")
	ErrDigestCollision   = errors.New("revision digest collision")
	ErrNotFound          = errors.New("revision was not found")
)

// SourceStatus describes whether a revision fact was collected safely.
type SourceStatus string

const (
	SourceAvailable   SourceStatus = "available"
	SourceUnavailable SourceStatus = "unavailable"
	SourceNotRepo     SourceStatus = "not-repository"
	SourceUnsafe      SourceStatus = "unsafe"
)

// GitFact is the bounded Git identity projection for a workspace.
type GitFact struct {
	Status   SourceStatus `json:"status"`
	Revision string       `json:"revision,omitempty"`
	Branch   string       `json:"branch,omitempty"`
	Dirty    bool         `json:"dirty,omitempty"`
	Reason   string       `json:"reason,omitempty"`
}

// FileFact identifies one allowlisted workspace file without storing content.
type FileFact struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

// RunnerFact records a safe resolved runner identity.
type RunnerFact struct {
	ServiceID        domain.ServiceID `json:"serviceId"`
	Kind             string           `json:"kind"`
	Version          string           `json:"version,omitempty"`
	ResolutionKind   string           `json:"resolutionKind,omitempty"`
	ExecutableDigest string           `json:"executableDigest,omitempty"`
	Status           SourceStatus     `json:"status"`
	Reason           string           `json:"reason,omitempty"`
}

// SecretFact records Secret metadata without exposing its value.
type SecretFact struct {
	ServiceID       domain.ServiceID `json:"serviceId"`
	EnvironmentName string           `json:"environmentName"`
	SystemID        domain.SystemID  `json:"systemId"`
	Name            string           `json:"name"`
	Provider        string           `json:"provider"`
	Version         int64            `json:"version"`
}

// ServiceFact is the comparison-safe identity of one service.
type ServiceFact struct {
	ServiceID        domain.ServiceID      `json:"serviceId"`
	Driver           domain.DriverKind     `json:"driver"`
	Mode             domain.ProcessMode    `json:"mode"`
	Required         bool                  `json:"required"`
	State            domain.ServiceState   `json:"state,omitempty"`
	DefinitionDigest string                `json:"definitionDigest,omitempty"`
	CommandDigest    string                `json:"commandDigest,omitempty"`
	ComposeDigest    string                `json:"composeDigest,omitempty"`
	Dependencies     []DependencyFact      `json:"dependencies,omitempty"`
	HealthCoverage   domain.HealthCoverage `json:"healthCoverage"`
	RestartPolicy    string                `json:"restartPolicy"`
	Images           []ComposeImageFact    `json:"images,omitempty"`
}

// ComposeImageFact records an available or explicitly unavailable image identity.
type ComposeImageFact struct {
	ComposeService  string       `json:"composeService"`
	ReferenceDigest string       `json:"referenceDigest,omitempty"`
	ImageDigest     string       `json:"imageDigest,omitempty"`
	Status          SourceStatus `json:"status"`
	Reason          string       `json:"reason,omitempty"`
}

// DependencyFact records one declared service dependency.
type DependencyFact struct {
	ServiceID domain.ServiceID           `json:"serviceId"`
	Condition domain.DependencyCondition `json:"condition"`
}

// PortFact records one normalized logical port declaration without allocating a port.
type PortFact struct {
	Name           string `json:"name"`
	Protocol       string `json:"protocol"`
	Preferred      *int   `json:"preferred,omitempty"`
	FallbackRange  string `json:"fallbackRange,omitempty"`
	ConflictPolicy string `json:"conflictPolicy"`
	Exposure       string `json:"exposure"`
}

// Snapshot contains the canonical facts used to identify one system revision.
type Snapshot struct {
	SchemaVersion      string                   `json:"schemaVersion"`
	WorkspaceID        domain.WorkspaceID       `json:"workspaceId"`
	SystemID           domain.SystemID          `json:"systemId"`
	Kind               domain.RevisionKind      `json:"kind"`
	SystemInstanceID   *domain.SystemInstanceID `json:"systemInstanceId,omitempty"`
	ManifestDigest     string                   `json:"manifestDigest"`
	ResolvedSpecDigest string                   `json:"resolvedSpecDigest,omitempty"`
	Git                GitFact                  `json:"git"`
	Files              []FileFact               `json:"files"`
	Ports              []PortFact               `json:"ports"`
	Services           []ServiceFact            `json:"services"`
	Runners            []RunnerFact             `json:"runners"`
	Secrets            []SecretFact             `json:"secrets"`
}

// Record is one immutable persisted revision snapshot.
type Record struct {
	ID               domain.RevisionID
	WorkspaceID      domain.WorkspaceID
	SystemID         domain.SystemID
	SystemInstanceID *domain.SystemInstanceID
	Kind             domain.RevisionKind
	SchemaVersion    string
	Digest           string
	JSON             []byte
	CreatedAt        time.Time
}
