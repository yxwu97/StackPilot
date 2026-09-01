// Package health executes bounded process, TCP, and HTTP readiness checks.
package health

import (
	"context"
	"errors"
	"time"

	"stackpilot/internal/domain"
	"stackpilot/internal/driver"
)

const (
	maxHTTPBodyBytes = 64 * 1024
	maxSummaryBytes  = 2 * 1024
)

// Kind identifies a supported Phase 1 health-check mechanism.
type Kind string

const (
	KindProcess Kind = "process"
	KindTCP     Kind = "tcp"
	KindHTTP    Kind = "http"
	KindCompose Kind = "compose"
)

// ErrorCode is a stable readiness failure category.
type ErrorCode string

// Purpose distinguishes startup readiness evidence from recurring liveness evidence.
type Purpose string

const (
	PurposeReadiness Purpose = "readiness"
	PurposeLiveness  Purpose = "liveness"
)

const (
	CodeProcessExited           ErrorCode = "PROCESS_EXITED"
	CodeProcessIdentityMismatch ErrorCode = "PROCESS_IDENTITY_MISMATCH"
	CodeTCPRefused              ErrorCode = "TCP_REFUSED"
	CodeTCPTimeout              ErrorCode = "TCP_TIMEOUT"
	CodeHTTPStatusMismatch      ErrorCode = "HTTP_STATUS_MISMATCH"
	CodeHTTPBodyMismatch        ErrorCode = "HTTP_BODY_MISMATCH"
	CodeHTTPTimeout             ErrorCode = "HTTP_TIMEOUT"
	CodeReadinessTimeout        ErrorCode = "HEALTH_READINESS_TIMEOUT"
	CodeContainerUnhealthy      ErrorCode = "CONTAINER_UNHEALTHY"
)

// Result is one bounded, persistence-safe health-check result.
type Result struct {
	ID         int64
	Purpose    Purpose
	Kind       Kind
	CheckedAt  time.Time
	Duration   time.Duration
	Success    bool
	ErrorCode  ErrorCode
	Summary    string
	StatusCode *int
}

// ResolvedSpec contains only fully expanded and validated runtime inputs.
type ResolvedSpec struct {
	Kind             Kind
	Identity         driver.RuntimeIdentity
	Host             string
	Port             int
	URL              string
	ComposeIdentity  string
	CheckTimeout     time.Duration
	ReadinessTimeout time.Duration
	Interval         time.Duration
	SuccessThreshold int
	FailureThreshold int
}

// Request binds a resolved readiness specification to one persisted service instance.
type Request struct {
	ServiceInstanceID domain.ServiceInstanceID
	Spec              ResolvedSpec
}

// Outcome summarizes a completed readiness wait without hiding the last check.
type Outcome struct {
	Ready      bool
	Attempts   int
	ErrorCode  ErrorCode
	LastResult Result
}

// Checker performs exactly one health check.
type Checker interface {
	Check(context.Context, ResolvedSpec) Result
}

// Inspector is the minimum Process Driver surface required by process readiness.
type Inspector interface {
	Inspect(context.Context, driver.RuntimeIdentity) (driver.RuntimeObservation, error)
}

// ComposeInspector checks one opaque verified Compose project identity.
type ComposeInspector interface {
	CheckCompose(context.Context, string) (bool, string, error)
}

// Recorder persists each completed check.
type Recorder interface {
	Record(context.Context, domain.ServiceInstanceID, Result) error
}

// IDRecorder additionally returns the durable health-result cursor.
type IDRecorder interface {
	RecordWithID(context.Context, domain.ServiceInstanceID, Result) (int64, error)
}

// Redactor removes sensitive response material before it reaches a summary.
type Redactor interface {
	Redact(string) (string, error)
}

var (
	// ErrInvalidSpec indicates unresolved or unsafe readiness inputs.
	ErrInvalidSpec = errors.New("health specification is invalid")
	// ErrRecorderRequired indicates that checks cannot run without durable results.
	ErrRecorderRequired = errors.New("health result recorder is required")
)
