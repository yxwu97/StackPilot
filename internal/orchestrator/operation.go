package orchestrator

import (
	"time"

	"stackpilot/internal/domain"
)

// Operation is the persisted asynchronous mutation record.
type Operation struct {
	ID                 domain.OperationID
	WorkspaceID        domain.WorkspaceID
	SystemID           domain.SystemID
	Type               domain.OperationType
	State              domain.OperationState
	IdempotencySubject string
	RouteKey           string
	IdempotencyKey     string
	RequestDigest      string
	Cancellable        bool
	CancelRequestedAt  *time.Time
	ErrorCode          string
	CreatedAt          time.Time
	StartedAt          *time.Time
	FinishedAt         *time.Time
	DurationMillis     *int64
	Steps              []Step
}

// Step is one stable, ordered unit of Operation progress.
type Step struct {
	Number         int
	Key            string
	State          domain.OperationStepState
	Attempt        int
	StartedAt      *time.Time
	FinishedAt     *time.Time
	DurationMillis *int64
	ErrorCode      string
	DetailRef      string
}

// CreateCommand contains validated values for one atomic Operation creation.
type CreateCommand struct {
	Operation Operation
	StepKeys  []string
}

// CreateResult distinguishes a new record from an idempotent replay.
type CreateResult struct {
	Operation Operation
	Created   bool
}

// CreateInput is the application boundary for a new mutation Operation.
type CreateInput struct {
	WorkspaceID        domain.WorkspaceID
	SystemID           domain.SystemID
	Type               domain.OperationType
	IdempotencySubject string
	RouteKey           string
	IdempotencyKey     string
	Request            []byte
	Cancellable        bool
	StepKeys           []string
}
