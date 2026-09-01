// Package incident builds bounded, traceable diagnostic contexts and results.
package incident

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"stackpilot/internal/domain"
)

// Kind identifies a stable failure classification.
type Kind string

const (
	KindPortConflict     Kind = "port-conflict"
	KindProcessExit      Kind = "process-exit"
	KindReadinessTimeout Kind = "readiness-timeout"
	KindLivenessFailure  Kind = "liveness-failure"
	KindRestartLimit     Kind = "restart-limit"
	KindIdentityMismatch Kind = "identity-mismatch"
	KindKnownLogError    Kind = "known-log-error"
	KindVerification     Kind = "verification-failure"
)

// Severity is the operator impact of an incident.
type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

// State is the incident lifecycle state.
type State string

const (
	StateOpen     State = "open"
	StateResolved State = "resolved"
)

// EvidenceRef points to durable evidence without copying unbounded content.
type EvidenceRef struct {
	Type              string                   `json:"type"`
	EventID           domain.EventID           `json:"eventId,omitempty"`
	HealthResultID    int64                    `json:"healthResultId,omitempty"`
	ServiceInstanceID domain.ServiceInstanceID `json:"serviceInstanceId,omitempty"`
	LogSequence       int64                    `json:"logSequence,omitempty"`
}

// LogLine is a redacted, bounded log excerpt.
type LogLine struct {
	ServiceInstanceID domain.ServiceInstanceID `json:"serviceInstanceId"`
	Sequence          int64                    `json:"sequence"`
	Timestamp         time.Time                `json:"timestamp"`
	Stream            string                   `json:"stream"`
	Message           string                   `json:"message"`
	RepeatCount       int                      `json:"repeatCount,omitempty"`
}

// Context is the deterministic input to every diagnostic engine.
type Context struct {
	SchemaVersion     string                         `json:"schemaVersion"`
	WorkspaceID       domain.WorkspaceID             `json:"workspaceId"`
	SystemInstanceID  domain.SystemInstanceID        `json:"systemInstanceId,omitempty"`
	ServiceInstanceID domain.ServiceInstanceID       `json:"serviceInstanceId,omitempty"`
	ServiceID         domain.ServiceID               `json:"serviceId,omitempty"`
	OperationID       domain.OperationID             `json:"operationId,omitempty"`
	ChangePlanID      domain.ChangePlanID            `json:"changePlanId,omitempty"`
	RevisionID        domain.RevisionID              `json:"revisionId,omitempty"`
	Kind              Kind                           `json:"kind"`
	TriggerCode       string                         `json:"triggerCode"`
	WindowStart       time.Time                      `json:"windowStart"`
	WindowEnd         time.Time                      `json:"windowEnd"`
	Dependencies      map[string]domain.ServiceState `json:"dependencies"`
	Ports             map[string]int                 `json:"ports"`
	Evidence          []EvidenceRef                  `json:"evidence"`
	Logs              []LogLine                      `json:"logs"`
}

// Record is one persisted, deduplicated incident.
type Record struct {
	ID                    domain.IncidentID
	Context               Context
	Severity              Severity
	State                 State
	Fingerprint           string
	OccurrenceCount       int
	TriggerEventID        domain.EventID
	TriggerHealthResultID int64
	FirstSeenAt           time.Time
	LastSeenAt            time.Time
	ResolvedAt            *time.Time
}

// Suggestion is a bounded recovery recommendation. Automatic is false in Phase 2E.
type Suggestion struct {
	Action      string `json:"action"`
	Description string `json:"description"`
	Automatic   bool   `json:"automatic"`
}

// RuleResult is one structured deterministic diagnosis.
type RuleResult struct {
	RuleID      string        `json:"ruleId"`
	Title       string        `json:"title"`
	Cause       string        `json:"cause"`
	Confidence  int           `json:"confidence"`
	Evidence    []EvidenceRef `json:"evidence"`
	Suggestions []Suggestion  `json:"suggestions"`
}

// Analysis is one versioned persisted engine result.
type Analysis struct {
	ID            int64
	IncidentID    domain.IncidentID
	Engine        string
	SchemaVersion string
	Result        json.RawMessage
	CreatedAt     time.Time
}

// Fingerprint returns the merge key for one service, rule kind, and trigger code.
func Fingerprint(workspaceID domain.WorkspaceID, serviceID domain.ServiceID, kind Kind, triggerCode string) string {
	digest := sha256.Sum256([]byte(workspaceID.String() + "\x00" + serviceID.String() + "\x00" + string(kind) + "\x00" + triggerCode))
	return hex.EncodeToString(digest[:])
}

// ValidateRecord rejects unsafe or structurally incomplete persistence input.
func ValidateRecord(record Record) error {
	if _, err := domain.ParseIncidentID(record.ID.String()); err != nil {
		return err
	}
	if _, err := domain.ParseWorkspaceID(record.Context.WorkspaceID.String()); err != nil {
		return err
	}
	if len(record.Fingerprint) != 64 || record.State != StateOpen || record.OccurrenceCount != 1 ||
		record.FirstSeenAt.IsZero() || record.LastSeenAt.Before(record.FirstSeenAt) || !record.FirstSeenAt.Equal(record.FirstSeenAt.UTC()) ||
		!record.LastSeenAt.Equal(record.LastSeenAt.UTC()) || !record.Severity.valid() || !record.Kind().valid() {
		return fmt.Errorf("invalid incident record")
	}
	return nil
}

// Kind returns the context classification.
func (record Record) Kind() Kind { return record.Context.Kind }

func (value Kind) valid() bool {
	switch value {
	case KindPortConflict, KindProcessExit, KindReadinessTimeout, KindLivenessFailure, KindRestartLimit, KindIdentityMismatch, KindKnownLogError, KindVerification:
		return true
	default:
		return false
	}
}

func (value Severity) valid() bool {
	return value == SeverityInfo || value == SeverityWarning || value == SeverityCritical
}
