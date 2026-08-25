package domain

import "time"

// SystemInstance is one immutable manifest execution and its aggregate state.
type SystemInstance struct {
	ID                 SystemInstanceID
	WorkspaceID        WorkspaceID
	SystemID           SystemID
	ManifestDigest     string
	ResolvedSpecDigest string
	State              SystemState
	StartedAt          time.Time
	StoppedAt          *time.Time
	LastReconciledAt   *time.Time
}

// ServiceInstance is one concrete service runtime with optimistic state versioning.
type ServiceInstance struct {
	ID               ServiceInstanceID
	SystemInstanceID SystemInstanceID
	ServiceID        ServiceID
	Driver           DriverKind
	ProcessMode      ProcessMode
	State            ServiceState
	Identity         *ProcessIdentity
	ComposeIdentity  string
	ExitCode         *uint32
	GracefulTimeout  time.Duration
	StateVersion     int64
	CreatedAt        time.Time
	UpdatedAt        time.Time
}
