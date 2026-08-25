package workspace

import (
	"time"

	"stackpilot/internal/domain"
)

const (
	// ManifestValid means the fixed manifest passed structural and semantic validation.
	ManifestValid = "valid"
	// ManifestInvalid means refresh failed while the last valid snapshot was retained.
	ManifestInvalid = "invalid"
)

// Record is the persisted registration view used by application and API layers.
type Record struct {
	ID              domain.WorkspaceID
	SystemID        domain.SystemID
	SystemName      string
	RootPath        string
	CanonicalPath   string
	ManifestStatus  string
	LastValidDigest string
	LastErrorCode   string
	ServiceCount    int
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// ServiceDefinition is a safe persisted service summary.
type ServiceDefinition struct {
	ID               domain.ServiceID
	Driver           domain.DriverKind
	Mode             domain.ProcessMode
	Required         bool
	DefinitionDigest string
}

// Snapshot is one normalized, validated manifest version.
type Snapshot struct {
	SystemID       domain.SystemID
	SystemName     string
	APIVersion     string
	Digest         string
	NormalizedYAML string
	ParsedJSON     string
	Services       []ServiceDefinition
	CreatedAt      time.Time
}

// Definition contains one workspace and its last valid read-only system definition.
type Definition struct {
	Workspace Record
	Manifest  ManifestView
	Services  []ServiceDefinition
}

// ManifestView contains immutable stored snapshot content for boundary-specific redaction.
type ManifestView struct {
	Digest         string
	APIVersion     string
	NormalizedYAML string
	ParsedJSON     string
	CreatedAt      time.Time
}

// Registration atomically creates a workspace and its first valid snapshot.
type Registration struct {
	ID            domain.WorkspaceID
	RootPath      string
	CanonicalPath string
	Snapshot      Snapshot
}

// Relink moves an existing catalog registration to another validated root.
type Relink struct {
	ID            domain.WorkspaceID
	RootPath      string
	CanonicalPath string
	Snapshot      Snapshot
}
