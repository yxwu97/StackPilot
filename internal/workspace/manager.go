package workspace

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"stackpilot/internal/domain"
	"stackpilot/internal/manifest"
)

const manifestRelativePath = ".stackpilot/system.yaml"

// Repository persists the registration catalog and immutable snapshots.
type Repository interface {
	Register(context.Context, Registration) (*Record, error)
	Refresh(context.Context, domain.WorkspaceID, Snapshot) (*Record, error)
	MarkInvalid(context.Context, domain.WorkspaceID, string, time.Time) error
	Get(context.Context, domain.WorkspaceID) (*Record, error)
	List(context.Context) ([]Record, error)
	Definition(context.Context, domain.WorkspaceID) (*Definition, error)
	ManifestByDigest(context.Context, string) (ManifestView, error)
	Delete(context.Context, domain.WorkspaceID) error
	Relink(context.Context, Relink) (*Record, error)
}

// Manager coordinates fixed manifest discovery, validation, and catalog persistence.
type Manager struct {
	repository Repository
	loader     *manifest.Loader
	validator  *manifest.Validator
	now        func() time.Time
	newID      func(time.Time) (domain.WorkspaceID, error)
}

// NewManager constructs the Phase 1A workspace use cases.
func NewManager(repository Repository, loader *manifest.Loader, validator *manifest.Validator) (*Manager, error) {
	if repository == nil || loader == nil || validator == nil {
		return nil, fmt.Errorf("workspace manager dependencies are required")
	}
	return &Manager{
		repository: repository, loader: loader, validator: validator,
		now: time.Now, newID: func(now time.Time) (domain.WorkspaceID, error) {
			return domain.NewWorkspaceID(now, rand.Reader)
		},
	}, nil
}

// Register validates the fixed manifest and atomically records its first snapshot.
func (manager *Manager) Register(ctx context.Context, path string) (*Record, error) {
	rootPath, canonicalPath, err := resolveRegistrationRoot(path)
	if err != nil {
		return nil, err
	}
	snapshot, err := manager.readSnapshot(ctx, canonicalPath)
	if err != nil {
		return nil, err
	}
	now := manager.now().UTC()
	id, err := manager.newID(now)
	if err != nil {
		return nil, fmt.Errorf("generate workspace ID: %w", err)
	}
	return manager.repository.Register(ctx, Registration{
		ID: id, RootPath: rootPath, CanonicalPath: canonicalPath, Snapshot: snapshot.withCreatedAt(now),
	})
}

// Refresh validates the current fixed manifest and preserves the last valid snapshot on failure.
func (manager *Manager) Refresh(ctx context.Context, id domain.WorkspaceID) (*Record, error) {
	current, err := manager.repository.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	snapshot, err := manager.readSnapshot(ctx, current.CanonicalPath)
	if err != nil {
		return nil, manager.recordRefreshFailure(ctx, id, err)
	}
	if snapshot.SystemID != current.SystemID {
		return nil, manager.recordRefreshFailure(ctx, id, ErrSystemChanged)
	}
	return manager.repository.Refresh(ctx, id, snapshot.withCreatedAt(manager.now().UTC()))
}

// Get returns one registered workspace.
func (manager *Manager) Get(ctx context.Context, id domain.WorkspaceID) (*Record, error) {
	return manager.repository.Get(ctx, id)
}

// List returns registrations in stable creation order.
func (manager *Manager) List(ctx context.Context) ([]Record, error) {
	return manager.repository.List(ctx)
}

// Definition returns the last valid immutable definition for a workspace.
func (manager *Manager) Definition(ctx context.Context, id domain.WorkspaceID) (*Definition, error) {
	return manager.repository.Definition(ctx, id)
}

// ManifestByDigest returns one immutable historical manifest snapshot.
func (manager *Manager) ManifestByDigest(ctx context.Context, digest string) (ManifestView, error) {
	return manager.repository.ManifestByDigest(ctx, digest)
}

func (manager *Manager) CurrentSnapshot(ctx context.Context, root string) (Snapshot, error) {
	canonical, err := manifest.CanonicalWorkspaceRoot(root)
	if err != nil {
		return Snapshot{}, err
	}
	return manager.readSnapshot(ctx, canonical)
}

// ExecutionManifest strictly decodes the last valid immutable snapshot for orchestration.
func (manager *Manager) ExecutionManifest(ctx context.Context, id domain.WorkspaceID) (*Record, *manifest.Manifest, error) {
	definition, err := manager.repository.Definition(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if definition.Workspace.ManifestStatus != ManifestValid {
		return nil, nil, ErrManifestUnavailable
	}
	decoder := json.NewDecoder(strings.NewReader(definition.Manifest.ParsedJSON))
	decoder.DisallowUnknownFields()
	var value manifest.Manifest
	if err := decoder.Decode(&value); err != nil {
		return nil, nil, fmt.Errorf("decode persisted manifest snapshot: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, nil, fmt.Errorf("decode persisted manifest snapshot: trailing data")
	}
	return &definition.Workspace, &value, nil
}

// ResolveSystem selects one registration by system ID and optional workspace ID.
func (manager *Manager) ResolveSystem(ctx context.Context, systemID domain.SystemID, workspaceID *domain.WorkspaceID) (*Record, error) {
	if workspaceID != nil {
		record, err := manager.repository.Get(ctx, *workspaceID)
		if err != nil || record.SystemID != systemID {
			if err == nil {
				err = ErrNotFound
			}
			return nil, err
		}
		return record, nil
	}
	registrations, err := manager.repository.List(ctx)
	if err != nil {
		return nil, err
	}
	var match *Record
	for index := range registrations {
		if registrations[index].SystemID != systemID {
			continue
		}
		if match != nil {
			return nil, ErrWorkspaceRequired
		}
		copy := registrations[index]
		match = &copy
	}
	if match == nil {
		return nil, ErrNotFound
	}
	return match, nil
}

// Unregister removes only StackPilot catalog data and never touches workspace files.
func (manager *Manager) Unregister(ctx context.Context, id domain.WorkspaceID) error {
	return manager.repository.Delete(ctx, id)
}

// Relink validates the fixed manifest at a new root and preserves workspace identity.
func (manager *Manager) Relink(ctx context.Context, id domain.WorkspaceID, path string) (*Record, error) {
	current, err := manager.repository.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	rootPath, canonicalPath, err := resolveRegistrationRoot(path)
	if err != nil {
		return nil, err
	}
	snapshot, err := manager.readSnapshot(ctx, canonicalPath)
	if err != nil {
		return nil, err
	}
	if snapshot.SystemID != current.SystemID {
		return nil, ErrRelinkSystemMismatch
	}
	snapshot = snapshot.withCreatedAt(manager.now().UTC())
	return manager.repository.Relink(ctx, Relink{ID: id, RootPath: rootPath, CanonicalPath: canonicalPath, Snapshot: snapshot})
}

func (manager *Manager) readSnapshot(ctx context.Context, root string) (Snapshot, error) {
	manifestPath, err := manifest.ResolveWorkspaceFile(root, manifestRelativePath, "$.workspace.manifest")
	if err != nil {
		return Snapshot{}, classifyManifestAccess(err)
	}
	document, err := manager.loader.Load(ctx, manifestPath)
	if err != nil {
		return Snapshot{}, classifyManifestAccess(err)
	}
	validated, err := manager.validator.Validate(ctx, document, root)
	if err != nil {
		return Snapshot{}, err
	}
	return buildSnapshot(validated)
}

func buildSnapshot(document *manifest.ValidatedDocument) (Snapshot, error) {
	normalizedYAML, err := yaml.Marshal(document.Manifest)
	if err != nil {
		return Snapshot{}, fmt.Errorf("encode normalized manifest YAML: %w", err)
	}
	systemID, err := domain.ParseSystemID(document.Manifest.Metadata.ID)
	if err != nil {
		return Snapshot{}, err
	}
	services, err := buildServiceDefinitions(document.Manifest)
	if err != nil {
		return Snapshot{}, err
	}
	digest := sha256.Sum256(document.JSON)
	return Snapshot{
		SystemID: systemID, SystemName: document.Manifest.Metadata.Name,
		APIVersion: document.Manifest.APIVersion, Digest: hex.EncodeToString(digest[:]),
		NormalizedYAML: string(normalizedYAML), ParsedJSON: string(document.JSON), Services: services,
	}, nil
}

func buildServiceDefinitions(value manifest.Manifest) ([]ServiceDefinition, error) {
	names := make([]string, 0, len(value.Spec.Services))
	for name := range value.Spec.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]ServiceDefinition, 0, len(names))
	for _, name := range names {
		service := value.Spec.Services[name]
		encoded, err := json.Marshal(service)
		if err != nil {
			return nil, fmt.Errorf("encode service definition: %w", err)
		}
		serviceID, _ := domain.ParseServiceID(name)
		digest := sha256.Sum256(encoded)
		result = append(result, ServiceDefinition{
			ID: serviceID, Driver: domain.DriverKind(service.Driver), Mode: domain.ProcessMode(service.Mode),
			Required: *service.Required, DefinitionDigest: hex.EncodeToString(digest[:]),
		})
	}
	return result, nil
}

func resolveRegistrationRoot(path string) (string, string, error) {
	if path == "" {
		return "", "", manifest.ErrSemanticInvalid
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", "", fmt.Errorf("resolve workspace path: %w", err)
	}
	canonical, err := manifest.CanonicalWorkspaceRoot(absolute)
	if err != nil {
		return "", "", fmt.Errorf("%w: %v", ErrPathInvalid, err)
	}
	return filepath.Clean(absolute), canonical, nil
}

func (manager *Manager) recordRefreshFailure(ctx context.Context, id domain.WorkspaceID, failure error) error {
	code, ok := refreshFailureCode(failure)
	if !ok || errors.Is(failure, context.Canceled) || errors.Is(failure, context.DeadlineExceeded) {
		return failure
	}
	if err := manager.repository.MarkInvalid(ctx, id, code, manager.now().UTC()); err != nil {
		return errors.Join(failure, fmt.Errorf("mark manifest invalid: %w", err))
	}
	return failure
}

func refreshFailureCode(err error) (string, bool) {
	if errors.Is(err, ErrManifestUnavailable) {
		return CodeManifestUnavailable, true
	}
	if errors.Is(err, ErrSystemChanged) {
		return "WORKSPACE_SYSTEM_ID_CHANGED", true
	}
	return manifest.ErrorCode(err)
}

func classifyManifestAccess(err error) error {
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrPermission) {
		return fmt.Errorf("%w: %w", ErrManifestUnavailable, err)
	}
	return err
}

func (snapshot Snapshot) withCreatedAt(value time.Time) Snapshot {
	snapshot.CreatedAt = value
	return snapshot
}
