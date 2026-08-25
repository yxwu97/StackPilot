package incident

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"stackpilot/internal/domain"
)

// Repository is the persistence surface used by incident coordination.
type Repository interface {
	UpsertOpen(context.Context, Record) (*Record, bool, error)
	Get(context.Context, domain.IncidentID) (*Record, error)
	UpdateContext(context.Context, domain.IncidentID, Context) error
	AddAnalysis(context.Context, Analysis) (int64, error)
}

// ReportInput contains bounded evidence for one incident occurrence.
type ReportInput struct {
	Context               Context
	Severity              Severity
	OccurredAt            time.Time
	TriggerEventID        domain.EventID
	TriggerHealthResultID int64
}

// Coordinator deduplicates incidents and persists deterministic diagnoses.
type Coordinator struct {
	repository Repository
	rules      *RuleEngine
	entropy    io.Reader
}

// NewCoordinator constructs an incident coordinator.
func NewCoordinator(repository Repository, rules *RuleEngine) (*Coordinator, error) {
	if repository == nil {
		return nil, fmt.Errorf("incident repository is required")
	}
	if rules == nil {
		rules = NewRuleEngine()
	}
	return &Coordinator{repository: repository, rules: rules, entropy: rand.Reader}, nil
}

// Report merges one occurrence and stores a fresh deterministic analysis.
func (coordinator *Coordinator) Report(ctx context.Context, input ReportInput) (*Record, []RuleResult, error) {
	id, err := domain.NewIncidentID(input.OccurredAt, coordinator.entropy)
	if err != nil {
		return nil, nil, err
	}
	record := Record{
		ID: id, Context: input.Context, Severity: input.Severity, State: StateOpen,
		Fingerprint:     Fingerprint(input.Context.WorkspaceID, input.Context.ServiceID, input.Context.Kind, input.Context.TriggerCode),
		OccurrenceCount: 1, TriggerEventID: input.TriggerEventID, TriggerHealthResultID: input.TriggerHealthResultID,
		FirstSeenAt: input.OccurredAt, LastSeenAt: input.OccurredAt,
	}
	stored, _, err := coordinator.repository.UpsertOpen(ctx, record)
	if err != nil {
		return nil, nil, err
	}
	results := coordinator.rules.Analyze(input.Context)
	payload, err := json.Marshal(map[string]any{"results": results})
	if err != nil {
		return nil, nil, err
	}
	_, err = coordinator.repository.AddAnalysis(ctx, Analysis{
		IncidentID: stored.ID, Engine: "rules", SchemaVersion: "1", Result: payload, CreatedAt: input.OccurredAt,
	})
	return stored, results, err
}

// Get returns the persisted incident used as the immutable analysis target.
func (coordinator *Coordinator) Get(ctx context.Context, id domain.IncidentID) (*Record, error) {
	return coordinator.repository.Get(ctx, id)
}

// Reanalyze replaces bounded context evidence and appends a versioned deterministic result.
func (coordinator *Coordinator) Reanalyze(ctx context.Context, id domain.IncidentID, value Context, analyzedAt time.Time) ([]RuleResult, error) {
	if analyzedAt.IsZero() || analyzedAt.Location() != time.UTC {
		return nil, fmt.Errorf("invalid incident analysis time")
	}
	if err := coordinator.repository.UpdateContext(ctx, id, value); err != nil {
		return nil, err
	}
	results := coordinator.rules.Analyze(value)
	payload, err := json.Marshal(map[string]any{"results": results})
	if err != nil {
		return nil, err
	}
	_, err = coordinator.repository.AddAnalysis(ctx, Analysis{IncidentID: id, Engine: "rules", SchemaVersion: "1", Result: payload, CreatedAt: analyzedAt})
	return results, err
}
