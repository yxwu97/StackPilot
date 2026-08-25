package orchestrator

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"stackpilot/internal/domain"
)

const (
	maxIdempotencyValueLength = 128
	maxOperationSteps         = 128
)

// Repository persists Operations and validates transitions atomically.
type Repository interface {
	Create(context.Context, CreateCommand) (*CreateResult, error)
	Get(context.Context, domain.OperationID) (*Operation, error)
	List(context.Context, *domain.WorkspaceID, int) ([]Operation, error)
	Transition(context.Context, domain.OperationID, domain.OperationState, string, time.Time) (*Operation, error)
	RequestCancel(context.Context, domain.OperationID, time.Time) (*Operation, error)
	TransitionStep(context.Context, domain.OperationID, int, domain.OperationStepState, string, string, time.Time) (*Operation, error)
	RecoverInterrupted(context.Context, time.Time) ([]domain.OperationID, error)
}

// Manager provides the Phase 1B Operation lifecycle use cases.
type Manager struct {
	repository Repository
	now        func() time.Time
	newID      func(time.Time) (domain.OperationID, error)
}

type operationEventReader interface {
	LatestOperationEvent(context.Context, domain.OperationID) (domain.EventID, bool, error)
}

// LatestEvent returns the newest durable event for an Operation when the repository supports event evidence.
func (manager *Manager) LatestEvent(ctx context.Context, id domain.OperationID) (domain.EventID, bool, error) {
	reader, ok := manager.repository.(operationEventReader)
	if !ok {
		return 0, false, nil
	}
	return reader.LatestOperationEvent(ctx, id)
}

// NewManager constructs an Operation manager.
func NewManager(repository Repository) (*Manager, error) {
	if repository == nil {
		return nil, fmt.Errorf("operation repository is required")
	}
	return &Manager{
		repository: repository, now: time.Now,
		newID: func(now time.Time) (domain.OperationID, error) { return domain.NewOperationID(now, rand.Reader) },
	}, nil
}

// Create atomically creates a queued Operation, its steps, and the workspace lock.
func (manager *Manager) Create(ctx context.Context, input CreateInput) (*CreateResult, error) {
	if err := validateCreateInput(input); err != nil {
		return nil, err
	}
	now := manager.now().UTC()
	id, err := manager.newID(now)
	if err != nil {
		return nil, fmt.Errorf("generate Operation ID: %w", err)
	}
	digest := sha256.Sum256(input.Request)
	operation := Operation{
		ID: id, WorkspaceID: input.WorkspaceID, SystemID: input.SystemID, Type: input.Type,
		State: domain.OperationQueued, IdempotencySubject: input.IdempotencySubject,
		RouteKey: input.RouteKey, IdempotencyKey: input.IdempotencyKey,
		RequestDigest: hex.EncodeToString(digest[:]), Cancellable: effectiveCancellable(input), CreatedAt: now,
	}
	return manager.repository.Create(ctx, CreateCommand{Operation: operation, StepKeys: input.StepKeys})
}

// Get returns one Operation and its ordered steps.
func (manager *Manager) Get(ctx context.Context, id domain.OperationID) (*Operation, error) {
	return manager.repository.Get(ctx, id)
}

// List returns newest Operations with a bounded optional workspace scope.
func (manager *Manager) List(ctx context.Context, workspaceID *domain.WorkspaceID, limit int) ([]Operation, error) {
	if limit < 1 || limit > 200 {
		return nil, fmt.Errorf("%w: operation list limit", ErrInvalidInput)
	}
	return manager.repository.List(ctx, workspaceID, limit)
}

// Start transitions a queued Operation to running.
func (manager *Manager) Start(ctx context.Context, id domain.OperationID) (*Operation, error) {
	return manager.repository.Transition(ctx, id, domain.OperationRunning, "", manager.now().UTC())
}

// Succeed completes a running Operation successfully.
func (manager *Manager) Succeed(ctx context.Context, id domain.OperationID) (*Operation, error) {
	return manager.repository.Transition(ctx, id, domain.OperationSucceeded, "", manager.now().UTC())
}

// Fail completes a running or cancelling Operation with a stable error code.
func (manager *Manager) Fail(ctx context.Context, id domain.OperationID, errorCode string) (*Operation, error) {
	if strings.TrimSpace(errorCode) == "" {
		return nil, fmt.Errorf("%w: terminal error code is required", ErrInvalidInput)
	}
	return manager.repository.Transition(ctx, id, domain.OperationFailed, errorCode, manager.now().UTC())
}

// CompleteCancellation finishes compensation for a cancelling Operation.
func (manager *Manager) CompleteCancellation(ctx context.Context, id domain.OperationID) (*Operation, error) {
	return manager.repository.Transition(ctx, id, domain.OperationCancelled, "", manager.now().UTC())
}

// RequestCancel records cooperative cancellation or cancels a queued Operation immediately.
func (manager *Manager) RequestCancel(ctx context.Context, id domain.OperationID) (*Operation, error) {
	return manager.repository.RequestCancel(ctx, id, manager.now().UTC())
}

// TransitionStep applies a centralized structured-step transition.
func (manager *Manager) TransitionStep(ctx context.Context, id domain.OperationID, number int, target domain.OperationStepState, errorCode, detailRef string) (*Operation, error) {
	if number <= 0 || !target.Valid() {
		return nil, fmt.Errorf("%w: invalid step transition input", ErrInvalidInput)
	}
	return manager.repository.TransitionStep(ctx, id, number, target, errorCode, detailRef, manager.now().UTC())
}

// RecoverInterrupted atomically fails Operations that lost their in-memory worker during a control-plane restart.
func (manager *Manager) RecoverInterrupted(ctx context.Context) ([]domain.OperationID, error) {
	return manager.repository.RecoverInterrupted(ctx, manager.now().UTC())
}

func validateCreateInput(input CreateInput) error {
	if _, err := domain.ParseWorkspaceID(input.WorkspaceID.String()); err != nil {
		return fmt.Errorf("%w: workspace ID", ErrInvalidInput)
	}
	if _, err := domain.ParseSystemID(input.SystemID.String()); err != nil || !input.Type.Valid() {
		return fmt.Errorf("%w: system ID or type", ErrInvalidInput)
	}
	if !validScopeValue(input.IdempotencySubject) || !validScopeValue(input.RouteKey) {
		return fmt.Errorf("%w: idempotency scope", ErrInvalidInput)
	}
	if len(input.IdempotencyKey) > maxIdempotencyValueLength || len(input.Request) == 0 {
		return fmt.Errorf("%w: key or request", ErrInvalidInput)
	}
	if len(input.StepKeys) == 0 || len(input.StepKeys) > maxOperationSteps {
		return fmt.Errorf("%w: steps", ErrInvalidInput)
	}
	seen := make(map[string]struct{}, len(input.StepKeys))
	for _, key := range input.StepKeys {
		if !validScopeValue(key) {
			return fmt.Errorf("%w: step key", ErrInvalidInput)
		}
		if _, exists := seen[key]; exists {
			return fmt.Errorf("%w: duplicate step key", ErrInvalidInput)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validScopeValue(value string) bool {
	return value != "" && len(value) <= maxIdempotencyValueLength && strings.TrimSpace(value) == value
}

func effectiveCancellable(input CreateInput) bool {
	return input.Cancellable && input.Type != domain.OperationStop
}
