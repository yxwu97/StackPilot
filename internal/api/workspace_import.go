package api

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"stackpilot/internal/domain"
	"stackpilot/internal/importer"
	"stackpilot/internal/security"
	"stackpilot/internal/workspace"
)

type workspaceProbeRequest struct {
	Path string `json:"path"`
}

type workspaceAnalyzeRequest struct {
	Path   string `json:"path"`
	Script string `json:"script"`
}

type workspaceApplyRequest struct {
	CandidateID string `json:"candidateId"`
}

type workspaceCorrectionRequest struct {
	CandidateID         string            `json:"candidateId"`
	SystemName          string            `json:"systemName"`
	Description         string            `json:"description"`
	ServiceDisplayNames map[string]string `json:"serviceDisplayNames"`
	PortPreferred       map[string]int    `json:"portPreferred"`
	ComposeRunning      map[string]bool   `json:"composeRunning"`
	ComposeBuild        bool              `json:"composeBuild"`
}

type workspaceDraftDTO struct {
	ID        string                         `json:"id"`
	State     string                         `json:"state"`
	Path      string                         `json:"path"`
	ExpiresAt string                         `json:"expiresAt"`
	Draft     workspaceImportDraftContentDTO `json:"draft"`
}

type workspaceImportDraftContentDTO struct {
	SystemID     string                        `json:"systemId"`
	SystemName   string                        `json:"systemName"`
	Description  string                        `json:"description,omitempty"`
	SourceScript string                        `json:"sourceScript,omitempty"`
	SourceDigest string                        `json:"sourceDigest,omitempty"`
	AnalyzedAt   string                        `json:"analyzedAt"`
	Candidates   []workspaceImportCandidateDTO `json:"candidates"`
}

type workspaceImportCandidateDTO struct {
	ID                   string                     `json:"id"`
	Name                 string                     `json:"name"`
	Description          string                     `json:"description"`
	Applyable            bool                       `json:"applyable"`
	RequiredCapabilities []string                   `json:"requiredCapabilities"`
	Services             []workspaceServiceDraftDTO `json:"services"`
	Ports                []importer.PortDraft       `json:"ports"`
	Findings             []importer.Finding         `json:"findings"`
	ManifestYAML         string                     `json:"manifestYaml"`
	ManifestDigest       string                     `json:"manifestDigest"`
}

type workspaceServiceDraftDTO struct {
	ID               string                 `json:"id"`
	DisplayName      string                 `json:"displayName"`
	Driver           string                 `json:"driver"`
	Runner           string                 `json:"runner"`
	Mode             string                 `json:"mode"`
	WorkingDirectory string                 `json:"workingDirectory"`
	ReadinessType    string                 `json:"readinessType"`
	ReadinessTarget  string                 `json:"readinessTarget,omitempty"`
	Confidence       importer.Confidence    `json:"confidence"`
	Compose          *importer.ComposeDraft `json:"compose,omitempty"`
}

type workspaceImportOperationDTO struct {
	ID          string                   `json:"id"`
	WorkspaceID *string                  `json:"workspaceId,omitempty"`
	Type        string                   `json:"type"`
	State       string                   `json:"state"`
	ErrorCode   string                   `json:"errorCode,omitempty"`
	CreatedAt   string                   `json:"createdAt"`
	StartedAt   *string                  `json:"startedAt,omitempty"`
	FinishedAt  *string                  `json:"finishedAt,omitempty"`
	DurationMs  *int64                   `json:"durationMs,omitempty"`
	Steps       []workspaceImportStepDTO `json:"steps"`
}

type workspaceImportStepDTO struct {
	Number     int     `json:"number"`
	Key        string  `json:"key"`
	State      string  `json:"state"`
	StartedAt  *string `json:"startedAt,omitempty"`
	FinishedAt *string `json:"finishedAt,omitempty"`
	ErrorCode  string  `json:"errorCode,omitempty"`
}

func registerWorkspaceImportRoutes(router chi.Router, service *workspace.ImportService, auth Authenticator, audit security.AuditStore, logger *slog.Logger) {
	router.With(browserMutationGuard(auth)).Post("/workspaces/probe", probeWorkspaceHandler(service))
	router.With(auditMutation(audit, logger, "workspace.import.analyze", "workspace-draft", ""), browserMutationGuard(auth)).Post("/workspace-imports/analyze", analyzeWorkspaceHandler(service))
	router.Get("/workspace-imports/drafts/{draftID}", getWorkspaceDraftHandler(service))
	router.With(auditMutation(audit, logger, "workspace.import.correct", "workspace-draft", "draftID"), browserMutationGuard(auth)).Post("/workspace-imports/drafts/{draftID}/corrections", correctWorkspaceDraftHandler(service))
	router.With(auditMutation(audit, logger, "workspace.import.apply", "workspace-draft", "draftID"), browserMutationGuard(auth)).Post("/workspace-imports/drafts/{draftID}/apply", applyWorkspaceDraftHandler(service))
	router.Get("/workspace-imports/operations/{operationID}", getWorkspaceImportOperationHandler(service))
}

func correctWorkspaceDraftHandler(service *workspace.ImportService) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		var input workspaceCorrectionRequest
		if decodeJSONRequest(response, request, &input) != nil || input.CandidateID == "" {
			writeRegisteredError(response, request, ErrorRequestValidationFailed)
			return
		}
		draft, err := service.CorrectDraft(request.Context(), chi.URLParam(request, "draftID"), workspace.ImportCorrectionInput{
			CandidateID: input.CandidateID, SystemName: input.SystemName, Description: input.Description,
			ServiceDisplayNames: input.ServiceDisplayNames, PortPreferred: input.PortPreferred, ComposeRunning: input.ComposeRunning, ComposeBuild: input.ComposeBuild,
		})
		if err != nil {
			writeBoundaryError(response, request, err)
			return
		}
		writeJSON(response, http.StatusCreated, mapWorkspaceDraft(*draft))
	}
}

func probeWorkspaceHandler(service *workspace.ImportService) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		var input workspaceProbeRequest
		if decodeJSONRequest(response, request, &input) != nil {
			writeRegisteredError(response, request, ErrorRequestValidationFailed)
			return
		}
		result, err := service.Probe(request.Context(), input.Path)
		if err != nil {
			writeBoundaryError(response, request, err)
			return
		}
		writeJSON(response, http.StatusOK, result)
	}
}

func analyzeWorkspaceHandler(service *workspace.ImportService) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		var input workspaceAnalyzeRequest
		if decodeJSONRequest(response, request, &input) != nil {
			writeRegisteredError(response, request, ErrorRequestValidationFailed)
			return
		}
		draft, err := service.Analyze(request.Context(), input.Path, input.Script)
		if err != nil {
			writeBoundaryError(response, request, err)
			return
		}
		writeJSON(response, http.StatusCreated, mapWorkspaceDraft(*draft))
	}
}

func getWorkspaceDraftHandler(service *workspace.ImportService) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		draft, err := service.GetDraft(request.Context(), chi.URLParam(request, "draftID"))
		if err != nil {
			writeBoundaryError(response, request, err)
			return
		}
		writeJSON(response, http.StatusOK, mapWorkspaceDraft(*draft))
	}
}

func applyWorkspaceDraftHandler(service *workspace.ImportService) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		var input workspaceApplyRequest
		if decodeJSONRequest(response, request, &input) != nil || input.CandidateID == "" || request.Header.Get("Idempotency-Key") == "" {
			writeRegisteredError(response, request, ErrorRequestValidationFailed)
			return
		}
		result, err := service.Apply(request.Context(), chi.URLParam(request, "draftID"), input.CandidateID, "local-user", request.Header.Get("Idempotency-Key"))
		if err != nil {
			writeBoundaryError(response, request, err)
			return
		}
		writeJSON(response, http.StatusAccepted, operationRefDTO{OperationID: result.Operation.ID.String(), State: string(result.Operation.State)})
	}
}

func getWorkspaceImportOperationHandler(service *workspace.ImportService) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		id, err := domain.ParseOperationID(chi.URLParam(request, "operationID"))
		if err != nil {
			writeRegisteredError(response, request, ErrorRequestValidationFailed)
			return
		}
		operation, err := service.GetOperation(request.Context(), id)
		if err != nil {
			writeBoundaryError(response, request, err)
			return
		}
		writeJSON(response, http.StatusOK, mapWorkspaceImportOperation(*operation))
	}
}

func mapWorkspaceDraft(value workspace.DraftRecord) workspaceDraftDTO {
	candidates := make([]workspaceImportCandidateDTO, 0, len(value.Draft.Candidates))
	for _, candidate := range value.Draft.Candidates {
		services := make([]workspaceServiceDraftDTO, 0, len(candidate.Services))
		for _, service := range candidate.Services {
			services = append(services, workspaceServiceDraftDTO{ID: service.ID, DisplayName: service.DisplayName, Driver: service.Driver,
				Runner: service.Runner, Mode: service.Mode, WorkingDirectory: service.WorkingDirectory,
				ReadinessType: service.ReadinessType, ReadinessTarget: service.ReadinessTarget, Confidence: service.Confidence, Compose: service.Compose})
		}
		candidates = append(candidates, workspaceImportCandidateDTO{ID: candidate.ID, Name: candidate.Name,
			Description: candidate.Description, Applyable: candidate.Applyable,
			RequiredCapabilities: candidate.RequiredCapabilities, Services: services, Ports: candidate.Ports,
			Findings: candidate.Findings, ManifestYAML: candidate.ManifestYAML, ManifestDigest: candidate.ManifestDigest})
	}
	draft := workspaceImportDraftContentDTO{SystemID: value.Draft.SystemID, SystemName: value.Draft.SystemName,
		Description: value.Draft.Description, SourceScript: value.Draft.SourceScript, SourceDigest: value.Draft.SourceDigest,
		AnalyzedAt: formatAPITime(value.Draft.AnalyzedAt), Candidates: candidates}
	return workspaceDraftDTO{ID: value.ID, State: value.State, Path: value.RootPath, ExpiresAt: formatAPITime(value.ExpiresAt), Draft: draft}
}

func mapWorkspaceImportOperation(value workspace.ImportOperation) workspaceImportOperationDTO {
	steps := make([]workspaceImportStepDTO, 0, len(value.Steps))
	for _, step := range value.Steps {
		steps = append(steps, workspaceImportStepDTO{Number: step.Number, Key: step.Key, State: string(step.State),
			StartedAt: optionalAPITime(step.StartedAt), FinishedAt: optionalAPITime(step.FinishedAt), ErrorCode: step.ErrorCode})
	}
	var workspaceID *string
	if value.WorkspaceID != nil {
		encoded := value.WorkspaceID.String()
		workspaceID = &encoded
	}
	return workspaceImportOperationDTO{ID: value.ID.String(), WorkspaceID: workspaceID, Type: value.Type, State: string(value.State),
		ErrorCode: value.ErrorCode, CreatedAt: formatAPITime(value.CreatedAt), StartedAt: optionalAPITime(value.StartedAt),
		FinishedAt: optionalAPITime(value.FinishedAt), DurationMs: value.DurationMillis, Steps: steps}
}
