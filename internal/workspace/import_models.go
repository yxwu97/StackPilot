package workspace

import (
	"context"
	"time"

	"stackpilot/internal/domain"
	"stackpilot/internal/importer"
)

const (
	DraftActive  = "active"
	DraftApplied = "applied"
	DraftExpired = "expired"
)

type DraftRecord struct {
	ID                 string
	Kind               string
	WorkspaceID        *domain.WorkspaceID
	RootPath           string
	CanonicalPath      string
	TargetKey          string
	EntryScript        string
	SourceDigest       string
	BaseManifestDigest string
	State              string
	Draft              importer.Draft
	CreatedAt          time.Time
	ExpiresAt          time.Time
	AppliedAt          *time.Time
}

type ImportOperation struct {
	ID                 domain.OperationID
	DraftID            string
	WorkspaceID        *domain.WorkspaceID
	TargetKey          string
	CandidateID        string
	Type               string
	State              domain.OperationState
	IdempotencySubject string
	RouteKey           string
	IdempotencyKey     string
	RequestDigest      string
	ErrorCode          string
	CreatedAt          time.Time
	StartedAt          *time.Time
	FinishedAt         *time.Time
	DurationMillis     *int64
	Steps              []ImportStep
}

type ImportStep struct {
	Number     int
	Key        string
	State      domain.OperationStepState
	StartedAt  *time.Time
	FinishedAt *time.Time
	ErrorCode  string
}

type ImportCreateResult struct {
	Operation ImportOperation
	Created   bool
}

type SourceRecord struct {
	WorkspaceID  domain.WorkspaceID
	SourceType   string
	EntryScript  string
	SourceDigest string
	AnalyzedAt   *time.Time
	UpdatedAt    time.Time
}

type EditInput struct {
	SystemName          string
	Description         string
	ServiceDisplayNames map[string]string
	PortPreferred       map[string]int
}

type ImportCorrectionInput struct {
	CandidateID         string
	SystemName          string
	Description         string
	ServiceDisplayNames map[string]string
	PortPreferred       map[string]int
	ComposeRunning      map[string]bool
	ComposeBuild        bool
}

type ImportRepository interface {
	SaveDraft(context.Context, DraftRecord) error
	GetDraft(context.Context, string) (*DraftRecord, error)
	CreateImportOperation(context.Context, ImportOperation, []string) (*ImportCreateResult, error)
	GetImportOperation(context.Context, domain.OperationID) (*ImportOperation, error)
	ListRecoverableImports(context.Context) ([]domain.OperationID, error)
	TransitionImportOperation(context.Context, domain.OperationID, domain.OperationState, string, *domain.WorkspaceID, time.Time) error
	TransitionImportStep(context.Context, domain.OperationID, int, domain.OperationStepState, string, time.Time) error
	MarkDraftApplied(context.Context, string, time.Time) error
	SaveWorkspaceSource(context.Context, SourceRecord) error
	GetWorkspaceSource(context.Context, domain.WorkspaceID) (*SourceRecord, error)
	ExpireDrafts(context.Context, time.Time) error
}
