package changeplan

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"stackpilot/internal/domain"
	"stackpilot/internal/revision"
)

// Repository persists and reads immutable ChangePlans.
type Repository interface {
	SaveOrGet(context.Context, Record) (*Record, error)
	Get(context.Context, domain.ChangePlanID) (*Record, error)
}

// RevisionReader reads immutable revision records.
type RevisionReader interface {
	Get(context.Context, domain.RevisionID) (*revision.Record, error)
}

// Service coordinates deterministic comparison and persistence.
type Service struct {
	repository Repository
	revisions  RevisionReader
	now        func() time.Time
	entropy    io.Reader
}

// NewService constructs a ChangePlan service.
func NewService(repository Repository, revisions RevisionReader) (*Service, error) {
	if repository == nil || revisions == nil {
		return nil, ErrInvalidInput
	}
	return &Service{repository: repository, revisions: revisions, now: time.Now, entropy: rand.Reader}, nil
}

// Create compares two persisted revisions and saves or reuses the immutable result.
func (service *Service) Create(ctx context.Context, operationID domain.OperationID, from, to revision.Record) (*Record, error) {
	if _, err := domain.ParseOperationID(operationID.String()); err != nil || from.WorkspaceID != to.WorkspaceID || from.SystemID != to.SystemID {
		return nil, ErrInvalidInput
	}
	var fromSnapshot, toSnapshot revision.Snapshot
	if json.Unmarshal(from.JSON, &fromSnapshot) != nil || json.Unmarshal(to.JSON, &toSnapshot) != nil {
		return nil, ErrInvalidInput
	}
	result, err := Compare(fromSnapshot, toSnapshot, from.Digest, to.Digest)
	if err != nil {
		return nil, err
	}
	return service.Persist(ctx, operationID, from, to, result)
}

// Persist stores a previously computed result for the exact revision pair.
func (service *Service) Persist(ctx context.Context, operationID domain.OperationID, from, to revision.Record, result Result) (*Record, error) {
	if _, err := domain.ParseOperationID(operationID.String()); err != nil || from.WorkspaceID != to.WorkspaceID ||
		from.SystemID != to.SystemID || result.FromDigest != from.Digest || result.ToDigest != to.Digest ||
		result.RuleVersion != RuleVersion || result.SchemaVersion != ResultSchemaVersion {
		return nil, ErrInvalidInput
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("encode change plan result: %w", err)
	}
	digest := sha256.Sum256(encoded)
	now := service.now().UTC()
	id, err := domain.NewChangePlanID(now, service.entropy)
	if err != nil {
		return nil, fmt.Errorf("generate ChangePlan ID: %w", err)
	}
	record := Record{ID: id, CreatedByOperationID: operationID, WorkspaceID: from.WorkspaceID, SystemID: from.SystemID,
		FromSnapshotID: from.ID, ToSnapshotID: to.ID, RuleVersion: RuleVersion, State: result.State, Risk: result.Risk,
		ItemCount: len(result.Items), BlockedCount: result.BlockedCount, ResultSchemaVersion: ResultSchemaVersion,
		ResultDigest: hex.EncodeToString(digest[:]), ResultJSON: encoded, CreatedAt: now}
	return service.repository.SaveOrGet(ctx, record)
}

// Get returns one hydrated immutable plan.
func (service *Service) Get(ctx context.Context, id domain.ChangePlanID) (*Plan, error) {
	record, err := service.repository.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	from, err := service.revisions.Get(ctx, record.FromSnapshotID)
	if err != nil {
		return nil, err
	}
	to, err := service.revisions.Get(ctx, record.ToSnapshotID)
	if err != nil {
		return nil, err
	}
	var result Result
	if err := json.Unmarshal(record.ResultJSON, &result); err != nil {
		return nil, fmt.Errorf("decode change plan result: %w", err)
	}
	return &Plan{Record: *record, From: *from, To: *to, Result: result}, nil
}
