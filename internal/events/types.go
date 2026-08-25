// Package events defines persisted domain events and bounded live notifications.
package events

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"time"

	"stackpilot/internal/domain"
)

// MaximumPageSize bounds every database catch-up batch.
const MaximumPageSize = 200

var eventTypePattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:\.[a-z][a-z0-9]*)+$`)

// Event is one persisted low-frequency domain event.
type Event struct {
	ID                domain.EventID
	Type              string
	OccurredAt        time.Time
	WorkspaceID       domain.WorkspaceID
	SystemID          domain.SystemID
	InstanceID        domain.SystemInstanceID
	ServiceInstanceID domain.ServiceInstanceID
	OperationID       domain.OperationID
	Data              json.RawMessage
}

// Store is the database-backed source of truth used by SSE recovery.
type Store interface {
	Bounds(context.Context) (domain.EventID, domain.EventID, bool, error)
	ListRange(context.Context, domain.EventID, domain.EventID, int) ([]Event, error)
}

// Notifier publishes a committed event identifier without blocking the writer.
type Notifier interface {
	Notify(domain.EventID)
}

var (
	// ErrInvalidEvent indicates malformed scope, payload, or event metadata.
	ErrInvalidEvent = errors.New("domain event is invalid")
	// ErrInvalidCursor indicates a cursor outside the current event history.
	ErrInvalidCursor = errors.New("event cursor is invalid")
	// ErrCursorExpired indicates a cursor older than retained history.
	ErrCursorExpired = errors.New("event cursor is expired")
)

// Validate checks identifiers, UTC time, event naming, and object-shaped JSON data.
func Validate(event Event) error {
	if !validType(event.Type) || event.OccurredAt.IsZero() || event.OccurredAt.Location() != time.UTC {
		return ErrInvalidEvent
	}
	if _, err := domain.ParseWorkspaceID(event.WorkspaceID.String()); err != nil {
		return ErrInvalidEvent
	}
	if _, err := domain.ParseSystemID(event.SystemID.String()); err != nil {
		return ErrInvalidEvent
	}
	if !validOptionalScopes(event) || !validJSONData(event.Data) {
		return ErrInvalidEvent
	}
	return nil
}

func validOptionalScopes(event Event) bool {
	if event.InstanceID != "" {
		if _, err := domain.ParseSystemInstanceID(event.InstanceID.String()); err != nil {
			return false
		}
	}
	if event.ServiceInstanceID != "" {
		if _, err := domain.ParseServiceInstanceID(event.ServiceInstanceID.String()); err != nil {
			return false
		}
	}
	if event.OperationID != "" {
		if _, err := domain.ParseOperationID(event.OperationID.String()); err != nil {
			return false
		}
	}
	return true
}

func validJSONData(data json.RawMessage) bool {
	if len(data) == 0 || len(data) > 64*1024 || !json.Valid(data) {
		return false
	}
	var object map[string]any
	return json.Unmarshal(data, &object) == nil && object != nil
}

func validType(value string) bool { return len(value) <= 128 && eventTypePattern.MatchString(value) }
