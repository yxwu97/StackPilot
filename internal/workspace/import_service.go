package workspace

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"stackpilot/internal/domain"
	"stackpilot/internal/importer"
	"stackpilot/internal/manifest"
	"stackpilot/internal/security"
)

const (
	importDraftLifetime = 24 * time.Hour
	importQueueCapacity = 64
)

var importStepKeys = []string{"verify-source", "validate-draft", "stage-manifest", "publish-manifest", "register-workspace", "record-source"}

type ImportServiceConfig struct {
	Context    context.Context
	Analyzer   *importer.Analyzer
	Repository ImportRepository
	Workspaces *Manager
	Logger     *slog.Logger
	CanEdit    func(context.Context, domain.WorkspaceID) error
}

type ImportService struct {
	ctx        context.Context
	analyzer   *importer.Analyzer
	repository ImportRepository
	workspaces *Manager
	logger     *slog.Logger
	queue      chan domain.OperationID
	now        func() time.Time
	newID      func(time.Time) (domain.OperationID, error)
	wait       sync.WaitGroup
	canEdit    func(context.Context, domain.WorkspaceID) error
}

func NewImportService(config ImportServiceConfig) (*ImportService, error) {
	if config.Context == nil || config.Analyzer == nil || config.Repository == nil || config.Workspaces == nil {
		return nil, fmt.Errorf("workspace import service dependencies are required")
	}
	logger := config.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	service := &ImportService{ctx: config.Context, analyzer: config.Analyzer, repository: config.Repository,
		workspaces: config.Workspaces, logger: logger, queue: make(chan domain.OperationID, importQueueCapacity), now: time.Now, canEdit: config.CanEdit,
		newID: func(now time.Time) (domain.OperationID, error) { return domain.NewOperationID(now, rand.Reader) }}
	service.wait.Add(1)
	go service.worker()
	if err := service.recover(config.Context); err != nil {
		return nil, err
	}
	return service, nil
}

func (service *ImportService) Probe(ctx context.Context, path string) (*importer.ProbeResult, error) {
	return service.analyzer.Probe(ctx, path)
}

func (service *ImportService) Analyze(ctx context.Context, path, script string) (*DraftRecord, error) {
	draft, err := service.analyzer.Analyze(ctx, path, script)
	if err != nil {
		return nil, err
	}
	root, canonical, err := resolveRegistrationRoot(path)
	if err != nil {
		return nil, err
	}
	now := service.now().UTC()
	record := DraftRecord{ID: newDraftID(), Kind: "import", RootPath: root, CanonicalPath: canonical,
		TargetKey: canonicalTargetKey(canonical), EntryScript: draft.SourceScript, SourceDigest: draft.SourceDigest,
		State: DraftActive, Draft: *draft, CreatedAt: now, ExpiresAt: now.Add(importDraftLifetime)}
	if record.ID == "" {
		return nil, fmt.Errorf("generate workspace draft ID")
	}
	if err := service.repository.ExpireDrafts(ctx, now); err != nil {
		return nil, err
	}
	if err := service.repository.SaveDraft(ctx, record); err != nil {
		return nil, err
	}
	return &record, nil
}

func (service *ImportService) CreateEditDraft(ctx context.Context, id domain.WorkspaceID, input EditInput) (*DraftRecord, error) {
	definition, err := service.workspaces.Definition(ctx, id)
	if err != nil {
		return nil, err
	}
	var value manifest.Manifest
	if err := json.Unmarshal([]byte(definition.Manifest.ParsedJSON), &value); err != nil {
		return nil, err
	}
	if err := applyStructuredEdit(&value, input); err != nil {
		return nil, err
	}
	validated, yamlValue, digest, err := service.analyzer.RenderManifest(ctx, definition.Workspace.CanonicalPath, value)
	if err != nil {
		return nil, err
	}
	now := service.now().UTC()
	draft := importer.Draft{SystemID: validated.Metadata.ID, SystemName: validated.Metadata.Name,
		Description: validated.Metadata.Description, AnalyzedAt: now,
		Candidates: []importer.CandidateDraft{editCandidate(validated, yamlValue, digest)}}
	record := DraftRecord{ID: newDraftID(), Kind: "edit", WorkspaceID: &id, RootPath: definition.Workspace.RootPath,
		CanonicalPath: definition.Workspace.CanonicalPath, TargetKey: canonicalTargetKey(definition.Workspace.CanonicalPath),
		BaseManifestDigest: definition.Manifest.Digest, State: DraftActive, Draft: draft,
		CreatedAt: now, ExpiresAt: now.Add(importDraftLifetime)}
	if err := service.repository.SaveDraft(ctx, record); err != nil {
		return nil, err
	}
	return &record, nil
}

func (service *ImportService) CreateRelinkDraft(ctx context.Context, id domain.WorkspaceID, path string) (*DraftRecord, error) {
	current, err := service.workspaces.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	root, canonical, err := resolveRegistrationRoot(path)
	if err != nil {
		return nil, err
	}
	snapshot, err := service.workspaces.CurrentSnapshot(ctx, canonical)
	if err != nil {
		return nil, err
	}
	if snapshot.SystemID != current.SystemID {
		return nil, ErrRelinkSystemMismatch
	}
	var value manifest.Manifest
	if err := json.Unmarshal([]byte(snapshot.ParsedJSON), &value); err != nil {
		return nil, err
	}
	validated, yamlValue, digest, err := service.analyzer.RenderManifest(ctx, canonical, value)
	if err != nil {
		return nil, err
	}
	candidate := editCandidate(validated, yamlValue, digest)
	candidate.ID, candidate.Name, candidate.Description = "relink", "Relink workspace", "Use the validated manifest at the new workspace root."
	now := service.now().UTC()
	draft := importer.Draft{SystemID: validated.Metadata.ID, SystemName: validated.Metadata.Name,
		Description: validated.Metadata.Description, AnalyzedAt: now, Candidates: []importer.CandidateDraft{candidate}}
	record := DraftRecord{ID: newDraftID(), Kind: "relink", WorkspaceID: &id, RootPath: root,
		CanonicalPath: canonical, TargetKey: canonicalTargetKey(canonical), BaseManifestDigest: current.LastValidDigest,
		State: DraftActive, Draft: draft, CreatedAt: now, ExpiresAt: now.Add(importDraftLifetime)}
	if err := service.repository.SaveDraft(ctx, record); err != nil {
		return nil, err
	}
	return &record, nil
}

func (service *ImportService) CorrectDraft(ctx context.Context, id string, input ImportCorrectionInput) (*DraftRecord, error) {
	base, err := service.GetDraft(ctx, id)
	if err != nil {
		return nil, err
	}
	if base.Kind != "import" {
		return nil, importer.ErrImportIncomplete
	}
	candidate, found := findCandidate(base.Draft, input.CandidateID)
	if !found {
		return nil, ErrDraftNotFound
	}
	if err := applyCandidateCorrection(&candidate, input); err != nil {
		return nil, err
	}
	identity := manifest.Metadata{ID: base.Draft.SystemID, Name: strings.TrimSpace(input.SystemName), Description: input.Description}
	if identity.Name == "" {
		identity.Name = base.Draft.SystemName
	}
	candidate.Manifest.Metadata = identity
	validated, yamlValue, digest, err := service.analyzer.RenderManifest(ctx, base.CanonicalPath, candidate.Manifest)
	if err != nil {
		return nil, err
	}
	candidate.Manifest, candidate.ManifestYAML, candidate.ManifestDigest = validated, yamlValue, digest
	candidate.Findings = correctedFindings(candidate.Findings, candidate.Ports, input.ComposeRunning, input.ComposeBuild)
	candidate.Applyable = noBlockingFindings(candidate.Findings)
	now := service.now().UTC()
	draft := base.Draft
	draft.SystemName, draft.Description, draft.AnalyzedAt = identity.Name, identity.Description, now
	draft.Candidates = []importer.CandidateDraft{candidate}
	record := DraftRecord{ID: newDraftID(), Kind: base.Kind, RootPath: base.RootPath, CanonicalPath: base.CanonicalPath,
		TargetKey: base.TargetKey, EntryScript: base.EntryScript, SourceDigest: base.SourceDigest,
		State: DraftActive, Draft: draft, CreatedAt: now, ExpiresAt: now.Add(importDraftLifetime)}
	if err := service.repository.SaveDraft(ctx, record); err != nil {
		return nil, err
	}
	return &record, nil
}

func (service *ImportService) GetDraft(ctx context.Context, id string) (*DraftRecord, error) {
	draft, err := service.repository.GetDraft(ctx, id)
	if err != nil {
		return nil, err
	}
	if draft.State == DraftExpired || service.now().UTC().After(draft.ExpiresAt) {
		return nil, ErrDraftExpired
	}
	return draft, nil
}

func (service *ImportService) Apply(ctx context.Context, draftID, candidateID, subject, key string) (*ImportCreateResult, error) {
	draft, err := service.GetDraft(ctx, draftID)
	if err != nil {
		return nil, err
	}
	candidate, found := findCandidate(draft.Draft, candidateID)
	if !found {
		return nil, ErrDraftNotFound
	}
	if !candidate.Applyable || len(candidate.Blockers()) > 0 {
		return nil, importer.ErrImportIncomplete
	}
	request, _ := json.Marshal(struct{ DraftID, CandidateID string }{draftID, candidateID})
	digest := sha256.Sum256(request)
	now := service.now().UTC()
	id, err := service.newID(now)
	if err != nil {
		return nil, err
	}
	operationType := "workspace-import-apply"
	if draft.Kind == "edit" {
		operationType = "workspace-edit-apply"
	} else if draft.Kind == "relink" {
		operationType = "workspace-relink-apply"
	}
	operation := ImportOperation{ID: id, DraftID: draftID, TargetKey: draft.TargetKey, CandidateID: candidateID,
		Type: operationType, State: domain.OperationQueued, IdempotencySubject: subject,
		RouteKey: operationType, IdempotencyKey: key, RequestDigest: hex.EncodeToString(digest[:]), CreatedAt: now}
	result, err := service.repository.CreateImportOperation(ctx, operation, stepKeysForDraft(draft.Kind))
	if err != nil {
		return nil, err
	}
	if result.Created {
		service.enqueue(result.Operation.ID)
	}
	return result, nil
}

func (service *ImportService) GetOperation(ctx context.Context, id domain.OperationID) (*ImportOperation, error) {
	return service.repository.GetImportOperation(ctx, id)
}

func (service *ImportService) GetSource(ctx context.Context, id domain.WorkspaceID) (*SourceRecord, error) {
	return service.repository.GetWorkspaceSource(ctx, id)
}

func (service *ImportService) Wait() { service.wait.Wait() }

func (service *ImportService) recover(ctx context.Context) error {
	ids, err := service.repository.ListRecoverableImports(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		service.enqueue(id)
	}
	return nil
}

func (service *ImportService) enqueue(id domain.OperationID) {
	select {
	case service.queue <- id:
	case <-service.ctx.Done():
	}
}

func (service *ImportService) worker() {
	defer service.wait.Done()
	for {
		select {
		case <-service.ctx.Done():
			return
		case id := <-service.queue:
			service.run(id)
		}
	}
}

func (service *ImportService) run(id domain.OperationID) {
	ctx := service.ctx
	operation, err := service.repository.GetImportOperation(ctx, id)
	if err != nil || operation.State.Terminal() {
		return
	}
	if operation.State == domain.OperationQueued {
		if err = service.repository.TransitionImportOperation(ctx, id, domain.OperationRunning, "", nil, service.now().UTC()); err != nil {
			return
		}
	}
	draft, err := service.repository.GetDraft(ctx, operation.DraftID)
	if err != nil {
		service.fail(operation, 1, "WORKSPACE_IMPORT_DRAFT_EXPIRED", err)
		return
	}
	candidate, found := findCandidate(draft.Draft, operation.CandidateID)
	if !found {
		service.fail(operation, 2, "WORKSPACE_IMPORT_INCOMPLETE", ErrDraftNotFound)
		return
	}
	workspaceID, err := service.executeSteps(ctx, operation, draft, candidate)
	if err != nil {
		return
	}
	_ = service.repository.TransitionImportOperation(ctx, id, domain.OperationSucceeded, "", workspaceID, service.now().UTC())
}

func (service *ImportService) executeSteps(ctx context.Context, operation *ImportOperation, draft *DraftRecord, candidate importer.CandidateDraft) (*domain.WorkspaceID, error) {
	if draft.Kind == "edit" {
		return service.executeEditSteps(ctx, operation, draft, candidate)
	}
	if draft.Kind == "relink" {
		return service.executeRelinkSteps(ctx, operation, draft, candidate)
	}
	actions := []func(context.Context) error{
		func(ctx context.Context) error {
			return service.analyzer.VerifySource(ctx, draft.CanonicalPath, draft.EntryScript, draft.SourceDigest)
		},
		func(context.Context) error {
			if !candidate.Applyable || len(candidate.Blockers()) > 0 {
				return importer.ErrImportIncomplete
			}
			return nil
		},
		func(context.Context) error {
			return ensureManifestTarget(draft.CanonicalPath, candidate.ManifestYAML, operation.ID, false)
		},
		func(context.Context) error { return publishStagedManifest(draft.CanonicalPath, operation.ID, false) },
	}
	for index, action := range actions {
		if err := service.runStep(ctx, operation, index+1, action); err != nil {
			return nil, err
		}
	}
	var registered *Record
	if err := service.runStep(ctx, operation, 5, func(ctx context.Context) error {
		var err error
		registered, err = service.registerOrRecover(ctx, draft)
		return err
	}); err != nil {
		return nil, err
	}
	if registered == nil {
		record, err := service.findByCanonical(ctx, draft.CanonicalPath)
		if err != nil {
			return nil, err
		}
		registered = record
	}
	if err := service.runStep(ctx, operation, 6, func(ctx context.Context) error { return service.recordSource(ctx, draft, registered.ID) }); err != nil {
		return nil, err
	}
	return &registered.ID, nil
}

func (service *ImportService) executeRelinkSteps(ctx context.Context, operation *ImportOperation, draft *DraftRecord, candidate importer.CandidateDraft) (*domain.WorkspaceID, error) {
	if draft.WorkspaceID == nil {
		return nil, ErrDraftNotFound
	}
	actions := []func(context.Context) error{
		func(ctx context.Context) error { return service.verifyRelink(ctx, draft) },
		func(context.Context) error {
			if !candidate.Applyable || len(candidate.Blockers()) > 0 {
				return importer.ErrImportIncomplete
			}
			return nil
		},
		func(context.Context) error { return nil },
		func(context.Context) error { return nil },
		func(ctx context.Context) error {
			_, err := service.workspaces.Relink(ctx, *draft.WorkspaceID, draft.RootPath)
			return err
		},
		func(ctx context.Context) error { return service.recordRelinkSource(ctx, draft, *draft.WorkspaceID) },
	}
	for index, action := range actions {
		if err := service.runStep(ctx, operation, index+1, action); err != nil {
			return nil, err
		}
	}
	return draft.WorkspaceID, nil
}

func (service *ImportService) verifyRelink(ctx context.Context, draft *DraftRecord) error {
	if service.canEdit != nil {
		if err := service.canEdit(ctx, *draft.WorkspaceID); err != nil {
			return ErrEditRuntimeActive
		}
	}
	current, err := service.workspaces.Get(ctx, *draft.WorkspaceID)
	if err != nil {
		return err
	}
	if current.LastValidDigest != draft.BaseManifestDigest {
		return ErrManifestConflict
	}
	snapshot, err := service.workspaces.CurrentSnapshot(ctx, draft.CanonicalPath)
	if err != nil {
		return err
	}
	if snapshot.SystemID != current.SystemID {
		return ErrRelinkSystemMismatch
	}
	return nil
}

func (service *ImportService) executeEditSteps(ctx context.Context, operation *ImportOperation, draft *DraftRecord, candidate importer.CandidateDraft) (*domain.WorkspaceID, error) {
	if draft.WorkspaceID == nil {
		return nil, ErrDraftNotFound
	}
	actions := []func(context.Context) error{
		func(ctx context.Context) error { return service.verifyEdit(ctx, draft) },
		func(context.Context) error {
			if !candidate.Applyable || len(candidate.Blockers()) > 0 {
				return importer.ErrImportIncomplete
			}
			return nil
		},
		func(context.Context) error {
			return ensureManifestTarget(draft.CanonicalPath, candidate.ManifestYAML, operation.ID, true)
		},
		func(context.Context) error { return publishStagedManifest(draft.CanonicalPath, operation.ID, true) },
		func(ctx context.Context) error {
			_, err := service.workspaces.Refresh(ctx, *draft.WorkspaceID)
			return err
		},
		func(ctx context.Context) error { return service.recordEditSource(ctx, draft, *draft.WorkspaceID) },
	}
	for index, action := range actions {
		if err := service.runStep(ctx, operation, index+1, action); err != nil {
			return nil, err
		}
	}
	return draft.WorkspaceID, nil
}

func (service *ImportService) verifyEdit(ctx context.Context, draft *DraftRecord) error {
	if service.canEdit != nil {
		if err := service.canEdit(ctx, *draft.WorkspaceID); err != nil {
			return ErrEditRuntimeActive
		}
	}
	snapshot, err := service.workspaces.CurrentSnapshot(ctx, draft.CanonicalPath)
	if err != nil {
		return err
	}
	if snapshot.Digest != draft.BaseManifestDigest {
		return ErrManifestConflict
	}
	return nil
}

func (service *ImportService) runStep(ctx context.Context, operation *ImportOperation, number int, action func(context.Context) error) error {
	step := operation.Steps[number-1]
	if step.State == domain.OperationStepSucceeded {
		return nil
	}
	if step.State != domain.OperationStepRunning {
		if err := service.repository.TransitionImportStep(ctx, operation.ID, number, domain.OperationStepRunning, "", service.now().UTC()); err != nil {
			return err
		}
	}
	if err := action(ctx); err != nil {
		code := importFailureCode(err)
		_ = service.repository.TransitionImportStep(ctx, operation.ID, number, domain.OperationStepFailed, code, service.now().UTC())
		_ = service.repository.TransitionImportOperation(ctx, operation.ID, domain.OperationFailed, code, nil, service.now().UTC())
		service.logger.Error("workspace import failed", "operation_id", operation.ID.String(), "error_code", code, "error", err)
		return err
	}
	return service.repository.TransitionImportStep(ctx, operation.ID, number, domain.OperationStepSucceeded, "", service.now().UTC())
}

func (service *ImportService) registerOrRecover(ctx context.Context, draft *DraftRecord) (*Record, error) {
	record, err := service.workspaces.Register(ctx, draft.RootPath)
	if errors.Is(err, ErrAlreadyRegistered) {
		return service.findByCanonical(ctx, draft.CanonicalPath)
	}
	return record, err
}

func (service *ImportService) findByCanonical(ctx context.Context, canonical string) (*Record, error) {
	records, err := service.workspaces.List(ctx)
	if err != nil {
		return nil, err
	}
	for index := range records {
		if strings.EqualFold(records[index].CanonicalPath, canonical) {
			return &records[index], nil
		}
	}
	return nil, ErrNotFound
}

func (service *ImportService) recordSource(ctx context.Context, draft *DraftRecord, id domain.WorkspaceID) error {
	now := service.now().UTC()
	if err := service.repository.SaveWorkspaceSource(ctx, SourceRecord{WorkspaceID: id, SourceType: "bat-import", EntryScript: draft.EntryScript,
		SourceDigest: draft.SourceDigest, AnalyzedAt: &draft.Draft.AnalyzedAt, UpdatedAt: now}); err != nil {
		return err
	}
	return service.repository.MarkDraftApplied(ctx, draft.ID, now)
}

func (service *ImportService) recordEditSource(ctx context.Context, draft *DraftRecord, id domain.WorkspaceID) error {
	now := service.now().UTC()
	if err := service.repository.SaveWorkspaceSource(ctx, SourceRecord{WorkspaceID: id, SourceType: "structured-edit", UpdatedAt: now}); err != nil {
		return err
	}
	return service.repository.MarkDraftApplied(ctx, draft.ID, now)
}

func (service *ImportService) recordRelinkSource(ctx context.Context, draft *DraftRecord, id domain.WorkspaceID) error {
	now := service.now().UTC()
	if err := service.repository.SaveWorkspaceSource(ctx, SourceRecord{WorkspaceID: id, SourceType: "relinked-manifest", UpdatedAt: now}); err != nil {
		return err
	}
	return service.repository.MarkDraftApplied(ctx, draft.ID, now)
}

func applyStructuredEdit(value *manifest.Manifest, input EditInput) error {
	name := strings.TrimSpace(input.SystemName)
	if name == "" || len(name) > 128 || len(input.Description) > 1024 {
		return importer.ErrImportIncomplete
	}
	value.Metadata.Name, value.Metadata.Description = name, input.Description
	for id, displayName := range input.ServiceDisplayNames {
		service, found := value.Spec.Services[id]
		if !found || len(displayName) > 128 {
			return importer.ErrImportIncomplete
		}
		service.DisplayName = strings.TrimSpace(displayName)
		value.Spec.Services[id] = service
	}
	for name, preferred := range input.PortPreferred {
		port, found := value.Spec.Ports[name]
		if !found || preferred < 1024 || preferred > 65535 {
			return importer.ErrPortUnconfirmed
		}
		port.Preferred = &preferred
		value.Spec.Ports[name] = port
	}
	return nil
}

func editCandidate(value manifest.Manifest, yamlValue, digest string) importer.CandidateDraft {
	serviceIDs := make([]string, 0, len(value.Spec.Services))
	for id := range value.Spec.Services {
		serviceIDs = append(serviceIDs, id)
	}
	sort.Strings(serviceIDs)
	services := make([]importer.ServiceDraft, 0, len(serviceIDs))
	for _, id := range serviceIDs {
		service := value.Spec.Services[id]
		readiness, target := "", ""
		if service.Readiness != nil {
			readiness, target = service.Readiness.Type, service.Readiness.URL
		}
		services = append(services, importer.ServiceDraft{ID: id, DisplayName: service.DisplayName, Driver: service.Driver, Runner: service.Runner,
			Mode: service.Mode, WorkingDirectory: service.WorkingDirectory, Arguments: service.Arguments,
			ReadinessType: readiness, ReadinessTarget: target, Confidence: importer.Confirmed})
	}
	portNames := make([]string, 0, len(value.Spec.Ports))
	for name := range value.Spec.Ports {
		portNames = append(portNames, name)
	}
	sort.Strings(portNames)
	ports := make([]importer.PortDraft, 0, len(portNames))
	for _, name := range portNames {
		port := value.Spec.Ports[name]
		preferred := 0
		if port.Preferred != nil {
			preferred = *port.Preferred
		}
		ports = append(ports, importer.PortDraft{Name: name, Preferred: preferred, Exposure: port.Exposure, Confidence: importer.Confirmed})
	}
	return importer.CandidateDraft{ID: "edit", Name: "Structured edit", Applyable: true, Services: services,
		Ports: ports, Findings: []importer.Finding{}, Manifest: value, ManifestYAML: yamlValue, ManifestDigest: digest}
}

func applyCandidateCorrection(candidate *importer.CandidateDraft, input ImportCorrectionInput) error {
	if len(input.Description) > 1024 || len(input.SystemName) > 128 {
		return importer.ErrImportIncomplete
	}
	for id, displayName := range input.ServiceDisplayNames {
		service, found := candidate.Manifest.Spec.Services[id]
		if !found || len(displayName) > 128 || strings.TrimSpace(displayName) == "" {
			return importer.ErrImportIncomplete
		}
		service.DisplayName = strings.TrimSpace(displayName)
		candidate.Manifest.Spec.Services[id] = service
		for index := range candidate.Services {
			if candidate.Services[index].ID == id {
				candidate.Services[index].DisplayName = service.DisplayName
			}
		}
	}
	for name, preferred := range input.PortPreferred {
		port, found := candidate.Manifest.Spec.Ports[name]
		if !found || preferred < 1024 || preferred > 65535 {
			return importer.ErrPortUnconfirmed
		}
		port.Preferred = &preferred
		candidate.Manifest.Spec.Ports[name] = port
		for index := range candidate.Ports {
			if candidate.Ports[index].Name == name {
				candidate.Ports[index].Preferred, candidate.Ports[index].Confidence = preferred, importer.Confirmed
			}
		}
	}
	for _, service := range candidate.Manifest.Spec.Services {
		if service.Compose == nil {
			continue
		}
		for name, requirement := range service.Compose.Readiness {
			if requirement == "running" && !input.ComposeRunning[name] {
				return importer.ErrDependencyUnconfirmed
			}
		}
		if service.Compose.BuildPolicy == "always" && !input.ComposeBuild {
			return importer.ErrDependencyUnconfirmed
		}
	}
	return nil
}

func correctedFindings(findings []importer.Finding, ports []importer.PortDraft, running map[string]bool, build bool) []importer.Finding {
	confirmed := true
	for _, port := range ports {
		confirmed = confirmed && port.Preferred >= 1024 && port.Preferred <= 65535
	}
	result := make([]importer.Finding, 0, len(findings))
	for _, finding := range findings {
		if confirmed && finding.Code == "WORKSPACE_IMPORT_PORT_UNCONFIRMED" {
			continue
		}
		if finding.Code == "WORKSPACE_IMPORT_READINESS_UNCONFIRMED" && running[finding.Field] {
			continue
		}
		if finding.Code == "WORKSPACE_IMPORT_BUILD_UNCONFIRMED" && build {
			continue
		}
		result = append(result, finding)
	}
	return result
}

func noBlockingFindings(findings []importer.Finding) bool {
	for _, finding := range findings {
		if finding.Severity == "blocking" {
			return false
		}
	}
	return true
}

func stepKeysForDraft(kind string) []string {
	if kind == "relink" {
		return []string{"verify-target", "validate-draft", "stage-catalog", "confirm-catalog", "relink-workspace", "record-source"}
	}
	return importStepKeys
}

func (service *ImportService) fail(operation *ImportOperation, step int, code string, err error) {
	_ = service.repository.TransitionImportStep(service.ctx, operation.ID, step, domain.OperationStepRunning, "", service.now().UTC())
	_ = service.repository.TransitionImportStep(service.ctx, operation.ID, step, domain.OperationStepFailed, code, service.now().UTC())
	_ = service.repository.TransitionImportOperation(service.ctx, operation.ID, domain.OperationFailed, code, nil, service.now().UTC())
	service.logger.Error("workspace import failed", "operation_id", operation.ID.String(), "error_code", code, "error", err)
}

func ensureManifestTarget(root, contents string, operationID domain.OperationID, allowExisting bool) error {
	directory := filepath.Join(root, ".stackpilot")
	if err := os.Mkdir(directory, 0o700); err != nil && !os.IsExist(err) {
		return fmt.Errorf("%w: create directory", ErrManifestWriteFailed)
	}
	canonicalDirectory, err := security.CanonicalExistingPath(directory)
	if err != nil {
		return fmt.Errorf("%w: canonical directory", ErrManifestWriteFailed)
	}
	inside, err := security.PathWithinRoot(root, canonicalDirectory)
	if err != nil || !inside {
		return ErrManifestWriteFailed
	}
	target := filepath.Join(canonicalDirectory, "system.yaml")
	if _, err := os.Lstat(target); err == nil && !allowExisting {
		current, readErr := os.ReadFile(target)
		if readErr == nil && string(current) == contents {
			return nil
		}
		return ErrManifestConflict
	}
	temporary := stagedManifestPath(canonicalDirectory, operationID)
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return nil
		}
		return fmt.Errorf("%w: stage file", ErrManifestWriteFailed)
	}
	if _, err = file.WriteString(contents); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("%w: write stage", ErrManifestWriteFailed)
	}
	return nil
}

func publishStagedManifest(root string, operationID domain.OperationID, replace bool) error {
	directory := filepath.Join(root, ".stackpilot")
	target, temporary := filepath.Join(directory, "system.yaml"), stagedManifestPath(directory, operationID)
	if _, err := os.Lstat(target); err == nil && !replace {
		if _, stageErr := os.Lstat(temporary); os.IsNotExist(stageErr) {
			return nil
		}
		current, currentErr := os.ReadFile(target)
		staged, stagedErr := os.ReadFile(temporary)
		if currentErr == nil && stagedErr == nil && string(current) == string(staged) {
			_ = os.Remove(temporary)
			return nil
		}
		return ErrManifestConflict
	}
	if err := atomicReplaceManifest(temporary, target, replace); err != nil {
		return fmt.Errorf("%w: publish", ErrManifestWriteFailed)
	}
	return nil
}

func stagedManifestPath(directory string, id domain.OperationID) string {
	return filepath.Join(directory, ".system.yaml."+id.String()+".tmp")
}

func findCandidate(draft importer.Draft, id string) (importer.CandidateDraft, bool) {
	for _, candidate := range draft.Candidates {
		if candidate.ID == id {
			return candidate, true
		}
	}
	return importer.CandidateDraft{}, false
}

func canonicalTargetKey(path string) string {
	value := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(path))))
	return hex.EncodeToString(value[:])
}

func newDraftID() string {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return ""
	}
	return "draft_" + hex.EncodeToString(value)
}

func importFailureCode(err error) string {
	switch {
	case errors.Is(err, importer.ErrImportIncomplete):
		return "WORKSPACE_IMPORT_INCOMPLETE"
	case errors.Is(err, importer.ErrSourceChanged):
		return "WORKSPACE_IMPORT_SOURCE_CHANGED"
	case errors.Is(err, ErrManifestConflict):
		return "WORKSPACE_MANIFEST_CONFLICT"
	case errors.Is(err, ErrManifestWriteFailed):
		return "WORKSPACE_MANIFEST_WRITE_FAILED"
	case errors.Is(err, ErrEditRuntimeActive):
		return "WORKSPACE_EDIT_RUNTIME_ACTIVE"
	case errors.Is(err, ErrRelinkSystemMismatch), errors.Is(err, ErrSystemChanged):
		return "WORKSPACE_RELINK_SYSTEM_MISMATCH"
	case errors.Is(err, ErrAlreadyRegistered):
		return "WORKSPACE_ALREADY_EXISTS"
	default:
		return "INTERNAL_ERROR"
	}
}
