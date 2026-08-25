// Package logs captures process spools, redacts messages, and persists NDJSON segments.
package logs

import (
	"context"
	"errors"
	"time"

	"stackpilot/internal/domain"
)

// Stream identifies one process output stream.
type Stream string

const (
	StreamStdout Stream = "stdout"
	StreamStderr Stream = "stderr"
)

// Entry is the stable redacted log event persisted as NDJSON.
type Entry struct {
	Timestamp   time.Time               `json:"timestamp"`
	SystemID    domain.SystemID         `json:"systemId"`
	InstanceID  domain.SystemInstanceID `json:"instanceId"`
	ServiceID   domain.ServiceID        `json:"serviceId"`
	Stream      Stream                  `json:"stream"`
	Level       string                  `json:"level"`
	Message     string                  `json:"message"`
	OperationID domain.OperationID      `json:"operationId,omitempty"`
	Sequence    int64                   `json:"sequence"`
	Truncated   bool                    `json:"truncated"`
}

// Scope identifies one immutable service runtime log sequence.
type Scope struct {
	SystemID          domain.SystemID
	InstanceID        domain.SystemInstanceID
	ServiceID         domain.ServiceID
	ServiceInstanceID domain.ServiceInstanceID
	OperationID       domain.OperationID
}

// ResolvedScope adds the workspace needed to authorize a public log query.
type ResolvedScope struct {
	Scope       Scope
	WorkspaceID domain.WorkspaceID
}

// ScopeResolver maps public runtime/service identifiers to the internal service instance.
type ScopeResolver interface {
	Resolve(context.Context, domain.SystemInstanceID, domain.ServiceID) (ResolvedScope, error)
}

// Segment is the SQLite metadata for one closed NDJSON file.
type Segment struct {
	ID                int64
	ServiceInstanceID domain.ServiceInstanceID
	Stream            Stream
	Path              string
	FirstSequence     int64
	LastSequence      int64
	FirstTimestamp    time.Time
	LastTimestamp     time.Time
	SizeBytes         int64
	ClosedAt          time.Time
}

// SegmentIndex persists and locates closed segment metadata.
type SegmentIndex interface {
	RegisterClosed(context.Context, Segment) error
	ListAfter(context.Context, domain.ServiceInstanceID, int64) ([]Segment, error)
	SequenceBounds(context.Context, domain.ServiceInstanceID) (int64, int64, bool, error)
}

// Publisher receives already-redacted entries only after segment persistence succeeds.
type Publisher interface {
	Publish(domain.ServiceInstanceID, Entry)
}

// Redactor removes sensitive values before final persistence or publication.
type Redactor interface {
	Redact(string) (string, error)
}

var (
	// ErrInvalidConfig indicates an unsafe or incomplete Log Manager configuration.
	ErrInvalidConfig = errors.New("log manager configuration is invalid")
	// ErrInvalidScope indicates malformed runtime identifiers or spool paths.
	ErrInvalidScope = errors.New("log capture scope is invalid")
	// ErrCursorExpired indicates that the requested sequence predates retained segments.
	ErrCursorExpired = errors.New("log cursor is expired")
	// ErrScopeNotFound indicates that a requested runtime log scope does not exist.
	ErrScopeNotFound = errors.New("log runtime scope was not found")
)
