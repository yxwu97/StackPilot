package revision

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"time"

	"stackpilot/internal/domain"
)

// Repository persists and queries immutable revision records.
type Repository interface {
	Save(context.Context, Record) error
	GetByDigest(context.Context, string) (*Record, error)
	Get(context.Context, domain.RevisionID) (*Record, error)
	ListLatest(context.Context, domain.WorkspaceID, domain.RevisionKind, int) ([]Record, error)
}

// Service coordinates collection, canonicalization, and idempotent persistence.
type Service struct {
	collector  *Collector
	repository Repository
	now        func() time.Time
	entropy    io.Reader
}

// NewService constructs a revision application service.
func NewService(collector *Collector, repository Repository) (*Service, error) {
	if collector == nil || repository == nil {
		return nil, ErrInvalidInput
	}
	return &Service{collector: collector, repository: repository, now: time.Now, entropy: rand.Reader}, nil
}

// Collect creates or reuses the immutable record for the current facts.
func (service *Service) Collect(ctx context.Context, workspaceID domain.WorkspaceID, kind domain.RevisionKind) (*Record, error) {
	snapshot, err := service.collector.Collect(ctx, workspaceID, kind)
	if err != nil {
		return nil, err
	}
	encoded, digest, err := Canonicalize(snapshot)
	if err != nil {
		return nil, err
	}
	if existing, err := service.repository.GetByDigest(ctx, digest); err == nil {
		return existing, nil
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	now := service.now().UTC()
	id, err := domain.NewRevisionID(now, service.entropy)
	if err != nil {
		return nil, fmt.Errorf("generate revision ID: %w", err)
	}
	record := Record{
		ID: id, WorkspaceID: snapshot.WorkspaceID, SystemID: snapshot.SystemID,
		SystemInstanceID: snapshot.SystemInstanceID, Kind: snapshot.Kind, SchemaVersion: snapshot.SchemaVersion,
		Digest: digest, JSON: encoded, CreatedAt: now,
	}
	if err := service.repository.Save(ctx, record); err != nil {
		return nil, err
	}
	return service.repository.GetByDigest(ctx, digest)
}

// Get returns one revision by ID.
func (service *Service) Get(ctx context.Context, id domain.RevisionID) (*Record, error) {
	return service.repository.Get(ctx, id)
}

// ListLatest returns a bounded newest-first revision list.
func (service *Service) ListLatest(ctx context.Context, workspaceID domain.WorkspaceID, kind domain.RevisionKind, limit int) ([]Record, error) {
	return service.repository.ListLatest(ctx, workspaceID, kind, limit)
}
