package security

import (
	"context"
	"errors"
	"regexp"
	"time"
)

const MaximumAuditPageSize = 200

var auditNamePattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$`)

var ErrInvalidAuditEvent = errors.New("audit event is invalid")

// AuditEvent is a safe, immutable security or mutation audit record.
type AuditEvent struct {
	ID          int64
	SubjectType string
	Action      string
	TargetType  string
	TargetID    string
	Result      string
	TraceID     string
	OperationID string
	ClientType  string
	ErrorCode   string
	OccurredAt  time.Time
}

// AuditStore persists and queries bounded audit records.
type AuditStore interface {
	AppendAudit(context.Context, AuditEvent) (AuditEvent, error)
	ListAudit(context.Context, int64, int) ([]AuditEvent, error)
}

// ValidateAuditEvent enforces bounded, non-secret audit vocabulary.
func ValidateAuditEvent(event AuditEvent) error {
	if event.ID < 0 || !oneOf(event.SubjectType, "local_token", "browser_session", "system") ||
		!validAuditName(event.Action, 128) || !validAuditName(event.TargetType, 64) ||
		!oneOf(event.Result, "accepted", "succeeded", "failed", "denied") ||
		!oneOf(event.ClientType, "cli", "web", "internal") || event.OccurredAt.IsZero() || event.OccurredAt.Location() != time.UTC {
		return ErrInvalidAuditEvent
	}
	if len(event.TargetID) > 128 || len(event.TraceID) < 3 || len(event.TraceID) > 64 || len(event.OperationID) > 64 || len(event.ErrorCode) > 128 {
		return ErrInvalidAuditEvent
	}
	return nil
}

func validAuditName(value string, maximum int) bool {
	return len(value) >= 2 && len(value) <= maximum && auditNamePattern.MatchString(value)
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}
