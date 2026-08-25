package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"stackpilot/internal/domain"
	"stackpilot/internal/importer"
	"stackpilot/internal/manifest"
	"stackpilot/internal/orchestrator"
	"stackpilot/internal/security"
	"stackpilot/internal/workspace"
)

const maxRequestBodyBytes = 64 << 10

func registerWorkspaceRoutes(router chi.Router, manager *workspace.Manager, imports *workspace.ImportService, runtimes *orchestrator.SingleService, auth Authenticator, audit security.AuditStore, logger *slog.Logger) {
	router.Get("/workspaces", listWorkspacesHandler(manager))
	router.Get("/workspaces/{workspaceID}", getWorkspaceDetailHandler(manager, imports, runtimes))
	router.With(auditMutation(audit, logger, "workspace.register", "workspace", ""), browserMutationGuard(auth)).Post("/workspaces", registerWorkspaceHandler(manager))
	router.With(auditMutation(audit, logger, "workspace.unregister", "workspace", "workspaceID"), browserMutationGuard(auth)).Delete("/workspaces/{workspaceID}", unregisterWorkspaceHandler(manager))
	router.With(auditMutation(audit, logger, "manifest.refresh", "workspace", "workspaceID"), browserMutationGuard(auth)).Post("/workspaces/{workspaceID}/refresh", refreshWorkspaceHandler(manager))
	if imports != nil {
		router.With(auditMutation(audit, logger, "workspace.edit.draft", "workspace", "workspaceID"), browserMutationGuard(auth)).Post("/workspaces/{workspaceID}/edit-drafts", createWorkspaceEditDraftHandler(imports))
		router.With(auditMutation(audit, logger, "workspace.relink.draft", "workspace", "workspaceID"), browserMutationGuard(auth)).Post("/workspaces/{workspaceID}/relink-drafts", createWorkspaceRelinkDraftHandler(imports))
	}
	router.Get("/systems", listSystemsHandler(manager, runtimes))
	router.Get("/systems/{systemID}", getSystemHandler(manager, runtimes))
	router.Get("/services/{systemID}/{serviceID}", getServiceHandler(manager))
}

type registerWorkspaceRequest struct {
	Path string `json:"path"`
}

type workspaceDTO struct {
	ID             string `json:"id"`
	SystemID       string `json:"systemId"`
	SystemName     string `json:"systemName"`
	Path           string `json:"path"`
	ManifestStatus string `json:"manifestStatus"`
	ManifestDigest string `json:"manifestDigest"`
	LastErrorCode  string `json:"lastErrorCode,omitempty"`
	ServiceCount   int    `json:"serviceCount"`
	CreatedAt      string `json:"createdAt"`
	UpdatedAt      string `json:"updatedAt"`
}

type workspaceListDTO struct {
	Items []workspaceDTO `json:"items"`
}

type workspaceEditDraftRequest struct {
	SystemName          string            `json:"systemName"`
	Description         string            `json:"description"`
	ServiceDisplayNames map[string]string `json:"serviceDisplayNames"`
	PortPreferred       map[string]int    `json:"portPreferred"`
}

type workspaceRelinkDraftRequest struct {
	Path string `json:"path"`
}

type workspaceManagementDetailDTO struct {
	Workspace workspaceDTO                `json:"workspace"`
	Source    workspaceSourceDTO          `json:"source"`
	Manifest  workspaceManifestDetailDTO  `json:"manifest"`
	Services  []workspaceServiceDetailDTO `json:"services"`
	Ports     []workspacePortDetailDTO    `json:"ports"`
	Runtime   workspaceRuntimeDetailDTO   `json:"runtime"`
}

type workspaceSourceDTO struct {
	Type         string  `json:"type"`
	EntryScript  string  `json:"entryScript,omitempty"`
	SourceDigest string  `json:"sourceDigest,omitempty"`
	AnalyzedAt   *string `json:"analyzedAt,omitempty"`
}
type workspaceManifestDetailDTO struct {
	Digest      string `json:"digest"`
	APIVersion  string `json:"apiVersion"`
	Description string `json:"description,omitempty"`
	YAML        string `json:"yaml"`
	CreatedAt   string `json:"createdAt"`
}
type workspaceServiceDetailDTO struct {
	ID               string            `json:"id"`
	DisplayName      string            `json:"displayName"`
	Driver           string            `json:"driver"`
	Mode             string            `json:"mode"`
	Runner           string            `json:"runner"`
	WorkingDirectory string            `json:"workingDirectory"`
	Required         bool              `json:"required"`
	DependsOn        map[string]string `json:"dependsOn"`
	Readiness        string            `json:"readiness"`
}
type workspacePortDetailDTO struct {
	Name           string `json:"name"`
	Protocol       string `json:"protocol"`
	Preferred      *int   `json:"preferred,omitempty"`
	FallbackRange  string `json:"fallbackRange,omitempty"`
	ConflictPolicy string `json:"conflictPolicy"`
	Exposure       string `json:"exposure"`
}
type workspaceRuntimeDetailDTO struct {
	State             string  `json:"state"`
	ActiveOperationID *string `json:"activeOperationId"`
}

type systemSummaryDTO struct {
	ID                string            `json:"id"`
	Name              string            `json:"name"`
	WorkspaceID       string            `json:"workspaceId"`
	WorkspacePath     string            `json:"workspacePath"`
	ManifestStatus    string            `json:"manifestStatus"`
	ManifestDigest    string            `json:"manifestDigest"`
	State             string            `json:"state"`
	ServiceSummary    serviceSummaryDTO `json:"serviceSummary"`
	ActiveOperationID *string           `json:"activeOperationId"`
	UpdatedAt         string            `json:"updatedAt"`
}

type serviceSummaryDTO struct {
	Ready int `json:"ready"`
	Total int `json:"total"`
}

type systemListDTO struct {
	Items []systemSummaryDTO `json:"items"`
}

type systemRuntimeSummary struct {
	state             domain.SystemState
	ready             int
	activeOperationID *string
}

type systemDetailDTO struct {
	System   systemSummaryDTO `json:"system"`
	Manifest manifestDTO      `json:"manifest"`
	Services []serviceDTO     `json:"services"`
}

type manifestDTO struct {
	Digest     string `json:"digest"`
	APIVersion string `json:"apiVersion"`
	CreatedAt  string `json:"createdAt"`
}

type serviceDTO struct {
	ID               string `json:"id"`
	Driver           string `json:"driver"`
	Mode             string `json:"mode"`
	Required         bool   `json:"required"`
	DefinitionDigest string `json:"definitionDigest"`
}

func registerWorkspaceHandler(manager *workspace.Manager) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		var input registerWorkspaceRequest
		if err := decodeJSONRequest(response, request, &input); err != nil {
			writeRegisteredError(response, request, ErrorRequestValidationFailed)
			return
		}
		if strings.TrimSpace(input.Path) == "" {
			writeRegisteredError(response, request, ErrorRequestValidationFailed)
			return
		}
		record, err := manager.Register(request.Context(), input.Path)
		if err != nil {
			writeBoundaryError(response, request, err)
			return
		}
		writeJSON(response, http.StatusCreated, mapWorkspace(*record))
	}
}

func listWorkspacesHandler(manager *workspace.Manager) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		records, err := manager.List(request.Context())
		if err != nil {
			writeBoundaryError(response, request, err)
			return
		}
		items := make([]workspaceDTO, 0, len(records))
		for _, record := range records {
			items = append(items, mapWorkspace(record))
		}
		writeJSON(response, http.StatusOK, workspaceListDTO{Items: items})
	}
}

func getWorkspaceDetailHandler(manager *workspace.Manager, imports *workspace.ImportService, runtimes *orchestrator.SingleService) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		id, err := domain.ParseWorkspaceID(chi.URLParam(request, "workspaceID"))
		if err != nil {
			writeRegisteredError(response, request, ErrorWorkspaceNotFound)
			return
		}
		definition, err := manager.Definition(request.Context(), id)
		if err != nil {
			writeBoundaryError(response, request, err)
			return
		}
		runtime, err := loadSystemRuntimeSummary(request, runtimes, id)
		if err != nil {
			writeBoundaryError(response, request, err)
			return
		}
		detail, err := mapWorkspaceDetail(request, definition, imports, runtime)
		if err != nil {
			writeBoundaryError(response, request, err)
			return
		}
		writeJSON(response, http.StatusOK, detail)
	}
}

func createWorkspaceEditDraftHandler(imports *workspace.ImportService) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		id, err := domain.ParseWorkspaceID(chi.URLParam(request, "workspaceID"))
		if err != nil {
			writeRegisteredError(response, request, ErrorWorkspaceNotFound)
			return
		}
		var input workspaceEditDraftRequest
		if decodeJSONRequest(response, request, &input) != nil {
			writeRegisteredError(response, request, ErrorRequestValidationFailed)
			return
		}
		draft, err := imports.CreateEditDraft(request.Context(), id, workspace.EditInput{
			SystemName: input.SystemName, Description: input.Description,
			ServiceDisplayNames: input.ServiceDisplayNames, PortPreferred: input.PortPreferred,
		})
		if err != nil {
			writeBoundaryError(response, request, err)
			return
		}
		writeJSON(response, http.StatusCreated, mapWorkspaceDraft(*draft))
	}
}

func createWorkspaceRelinkDraftHandler(imports *workspace.ImportService) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		id, err := domain.ParseWorkspaceID(chi.URLParam(request, "workspaceID"))
		if err != nil {
			writeRegisteredError(response, request, ErrorWorkspaceNotFound)
			return
		}
		var input workspaceRelinkDraftRequest
		if decodeJSONRequest(response, request, &input) != nil || strings.TrimSpace(input.Path) == "" {
			writeRegisteredError(response, request, ErrorRequestValidationFailed)
			return
		}
		draft, err := imports.CreateRelinkDraft(request.Context(), id, input.Path)
		if err != nil {
			writeBoundaryError(response, request, err)
			return
		}
		writeJSON(response, http.StatusCreated, mapWorkspaceDraft(*draft))
	}
}

func mapWorkspaceDetail(request *http.Request, definition *workspace.Definition, imports *workspace.ImportService, runtime systemRuntimeSummary) (workspaceManagementDetailDTO, error) {
	var value manifest.Manifest
	if err := json.Unmarshal([]byte(definition.Manifest.ParsedJSON), &value); err != nil {
		return workspaceManagementDetailDTO{}, err
	}
	source := workspaceSourceDTO{Type: "existing-manifest"}
	if imports != nil {
		if record, err := imports.GetSource(request.Context(), definition.Workspace.ID); err == nil {
			source = workspaceSourceDTO{Type: record.SourceType, EntryScript: record.EntryScript, SourceDigest: record.SourceDigest, AnalyzedAt: optionalAPITime(record.AnalyzedAt)}
		} else if !errors.Is(err, workspace.ErrNotFound) {
			return workspaceManagementDetailDTO{}, err
		}
	}
	services := mapWorkspaceDetailServices(value.Spec.Services)
	ports := mapWorkspaceDetailPorts(value.Spec.Ports)
	return workspaceManagementDetailDTO{Workspace: mapWorkspace(definition.Workspace), Source: source,
		Manifest: workspaceManifestDetailDTO{Digest: definition.Manifest.Digest, APIVersion: definition.Manifest.APIVersion, Description: value.Metadata.Description, YAML: definition.Manifest.NormalizedYAML, CreatedAt: formatAPITime(definition.Manifest.CreatedAt)},
		Services: services, Ports: ports, Runtime: workspaceRuntimeDetailDTO{State: string(runtime.state), ActiveOperationID: runtime.activeOperationID}}, nil
}

func mapWorkspaceDetailServices(values map[string]manifest.Service) []workspaceServiceDetailDTO {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]workspaceServiceDetailDTO, 0, len(names))
	for _, name := range names {
		value := values[name]
		readiness := ""
		if value.Readiness != nil {
			readiness = value.Readiness.Type
		}
		required := value.Required == nil || *value.Required
		result = append(result, workspaceServiceDetailDTO{ID: name, DisplayName: value.DisplayName, Driver: value.Driver, Mode: value.Mode, Runner: value.Runner, WorkingDirectory: value.WorkingDirectory, Required: required, DependsOn: value.DependsOn, Readiness: readiness})
	}
	return result
}

func mapWorkspaceDetailPorts(values map[string]manifest.Port) []workspacePortDetailDTO {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]workspacePortDetailDTO, 0, len(names))
	for _, name := range names {
		value := values[name]
		result = append(result, workspacePortDetailDTO{Name: name, Protocol: value.Protocol, Preferred: value.Preferred, FallbackRange: value.FallbackRange, ConflictPolicy: value.ConflictPolicy, Exposure: value.Exposure})
	}
	return result
}

func unregisterWorkspaceHandler(manager *workspace.Manager) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		id, err := domain.ParseWorkspaceID(chi.URLParam(request, "workspaceID"))
		if err != nil {
			writeRegisteredError(response, request, ErrorWorkspaceNotFound)
			return
		}
		if err := manager.Unregister(request.Context(), id); err != nil {
			writeBoundaryError(response, request, err)
			return
		}
		response.WriteHeader(http.StatusNoContent)
	}
}

func refreshWorkspaceHandler(manager *workspace.Manager) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		id, err := domain.ParseWorkspaceID(chi.URLParam(request, "workspaceID"))
		if err != nil {
			writeRegisteredError(response, request, ErrorWorkspaceNotFound)
			return
		}
		record, err := manager.Refresh(request.Context(), id)
		if err != nil {
			writeBoundaryError(response, request, err)
			return
		}
		writeJSON(response, http.StatusOK, mapWorkspace(*record))
	}
}

func listSystemsHandler(manager *workspace.Manager, runtimes *orchestrator.SingleService) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		records, err := manager.List(request.Context())
		if err != nil {
			writeBoundaryError(response, request, err)
			return
		}
		items := make([]systemSummaryDTO, 0, len(records))
		for _, record := range records {
			summary, err := loadSystemRuntimeSummary(request, runtimes, record.ID)
			if err != nil {
				writeBoundaryError(response, request, err)
				return
			}
			items = append(items, mapSystem(record, summary))
		}
		writeJSON(response, http.StatusOK, systemListDTO{Items: items})
	}
}

func getSystemHandler(manager *workspace.Manager, runtimes *orchestrator.SingleService) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		record, err := resolveSystemRequest(request, manager)
		if err != nil {
			writeBoundaryError(response, request, err)
			return
		}
		definition, err := manager.Definition(request.Context(), record.ID)
		if err != nil {
			writeBoundaryError(response, request, err)
			return
		}
		summary, err := loadSystemRuntimeSummary(request, runtimes, record.ID)
		if err != nil {
			writeBoundaryError(response, request, err)
			return
		}
		writeJSON(response, http.StatusOK, mapSystemDetail(*definition, summary))
	}
}

func getServiceHandler(manager *workspace.Manager) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		record, err := resolveSystemRequest(request, manager)
		if err != nil {
			writeBoundaryError(response, request, err)
			return
		}
		serviceID, err := domain.ParseServiceID(chi.URLParam(request, "serviceID"))
		if err != nil {
			writeRegisteredError(response, request, ErrorResourceNotFound)
			return
		}
		definition, err := manager.Definition(request.Context(), record.ID)
		if err != nil {
			writeBoundaryError(response, request, err)
			return
		}
		for _, service := range definition.Services {
			if service.ID == serviceID {
				writeJSON(response, http.StatusOK, mapService(service))
				return
			}
		}
		writeRegisteredError(response, request, ErrorResourceNotFound)
	}
}

func resolveSystemRequest(request *http.Request, manager *workspace.Manager) (*workspace.Record, error) {
	systemID, err := domain.ParseSystemID(chi.URLParam(request, "systemID"))
	if err != nil {
		return nil, workspace.ErrNotFound
	}
	var workspaceID *domain.WorkspaceID
	if value := request.URL.Query().Get("workspaceId"); value != "" {
		parsed, err := domain.ParseWorkspaceID(value)
		if err != nil {
			return nil, workspace.ErrNotFound
		}
		workspaceID = &parsed
	}
	return manager.ResolveSystem(request.Context(), systemID, workspaceID)
}

func decodeJSONRequest(response http.ResponseWriter, request *http.Request, target any) error {
	mediaType := strings.ToLower(strings.TrimSpace(strings.Split(request.Header.Get("Content-Type"), ";")[0]))
	if mediaType != "application/json" {
		return fmt.Errorf("request content type must be application/json")
	}
	request.Body = http.MaxBytesReader(response, request.Body, maxRequestBodyBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return fmt.Errorf("request body must contain one JSON value")
	}
	return nil
}

func writeBoundaryError(response http.ResponseWriter, request *http.Request, err error) {
	code, details := boundaryError(err)
	writeRegisteredErrorDetails(response, request, code, details)
}

func boundaryError(err error) (ErrorCode, map[string]any) {
	switch {
	case errors.Is(err, security.ErrAuthenticationFailed):
		return ErrorAuthenticationRequired, nil
	case errors.Is(err, security.ErrBootstrapInvalid):
		return ErrorBootstrapInvalid, nil
	case errors.Is(err, security.ErrSessionInvalid):
		return ErrorSessionInvalid, nil
	case errors.Is(err, security.ErrAuthCapacity):
		return ErrorAuthCapacity, nil
	case errors.Is(err, security.ErrSecretNotFound):
		return ErrorSecretNotFound, nil
	case errors.Is(err, security.ErrSecretInvalid):
		return ErrorSecretInvalid, nil
	case errors.Is(err, security.ErrSecretVersionConflict):
		return ErrorSecretVersionConflict, nil
	}
	if code, ok := operationBoundaryError(err); ok {
		return code, nil
	}
	switch {
	case errors.Is(err, workspace.ErrAlreadyRegistered):
		return ErrorWorkspaceAlreadyExists, nil
	case errors.Is(err, workspace.ErrNotFound):
		return ErrorWorkspaceNotFound, nil
	case errors.Is(err, workspace.ErrManifestUnavailable):
		return ErrorWorkspaceManifestAbsent, nil
	case errors.Is(err, workspace.ErrSystemChanged):
		return ErrorWorkspaceSystemChanged, nil
	case errors.Is(err, workspace.ErrWorkspaceRequired):
		return ErrorWorkspaceIDRequired, nil
	case errors.Is(err, workspace.ErrPathInvalid):
		return ErrorWorkspacePathInvalid, nil
	case errors.Is(err, workspace.ErrDraftNotFound), errors.Is(err, workspace.ErrImportOperationNotFound):
		return ErrorResourceNotFound, nil
	case errors.Is(err, workspace.ErrDraftExpired):
		return ErrorWorkspaceDraftExpired, nil
	case errors.Is(err, workspace.ErrImportAlreadyActive):
		return ErrorOperationAlreadyActive, nil
	case errors.Is(err, workspace.ErrImportSourceChanged):
		return ErrorWorkspaceImportSourceChanged, nil
	case errors.Is(err, workspace.ErrManifestConflict):
		return ErrorWorkspaceManifestConflict, nil
	case errors.Is(err, workspace.ErrManifestWriteFailed):
		return ErrorWorkspaceManifestWrite, nil
	case errors.Is(err, workspace.ErrEditRuntimeActive):
		return ErrorWorkspaceEditRuntimeActive, nil
	case errors.Is(err, workspace.ErrUnregisterRuntimeActive):
		return ErrorWorkspaceUnregisterActive, nil
	case errors.Is(err, workspace.ErrRelinkSystemMismatch):
		return ErrorWorkspaceRelinkSystemMismatch, nil
	case errors.Is(err, importer.ErrPathInvalid):
		return ErrorWorkspacePathInvalid, nil
	case errors.Is(err, importer.ErrScriptNotFound):
		return ErrorWorkspaceScriptNotFound, nil
	case errors.Is(err, importer.ErrScriptOutside):
		return ErrorWorkspaceScriptOutside, nil
	case errors.Is(err, importer.ErrScriptType):
		return ErrorWorkspaceScriptType, nil
	case errors.Is(err, importer.ErrScriptEncoding):
		return ErrorWorkspaceScriptEncoding, nil
	case errors.Is(err, importer.ErrScriptTooLarge):
		return ErrorWorkspaceScriptTooLarge, nil
	case errors.Is(err, importer.ErrScriptDangerous):
		return ErrorWorkspaceScriptDangerous, nil
	case errors.Is(err, importer.ErrScriptUnsupported):
		return ErrorWorkspaceScriptSyntax, nil
	case errors.Is(err, importer.ErrComposeBuildConfig):
		return ErrorComposeBuildConfigInvalid, nil
	case errors.Is(err, importer.ErrReferenceCycle):
		return ErrorWorkspaceScriptCycle, nil
	case errors.Is(err, importer.ErrPortUnconfirmed):
		return ErrorWorkspaceImportPort, nil
	case errors.Is(err, importer.ErrDependencyUnconfirmed):
		return ErrorWorkspaceImportDependency, nil
	case errors.Is(err, importer.ErrImportIncomplete):
		return ErrorWorkspaceImportIncomplete, nil
	case errors.Is(err, importer.ErrSourceChanged):
		return ErrorWorkspaceImportSourceChanged, nil
	}
	if code, ok := manifest.ErrorCode(err); ok {
		return ErrorCode(code), manifestErrorDetails(err)
	}
	return ErrorInternal, nil
}

func manifestErrorDetails(err error) map[string]any {
	details := make(map[string]any)
	var validation *manifest.ValidationError
	if errors.As(err, &validation) {
		details["location"] = validation.Path
		if validation.Field != "" {
			details["field"] = validation.Field
		}
	}
	var feature *manifest.FeatureError
	if errors.As(err, &feature) {
		details["feature"] = feature.Feature
	}
	return details
}

func mapWorkspace(record workspace.Record) workspaceDTO {
	return workspaceDTO{
		ID: record.ID.String(), SystemID: record.SystemID.String(), SystemName: record.SystemName,
		Path: record.RootPath, ManifestStatus: record.ManifestStatus, ManifestDigest: record.LastValidDigest,
		LastErrorCode: record.LastErrorCode, ServiceCount: record.ServiceCount,
		CreatedAt: formatAPITime(record.CreatedAt), UpdatedAt: formatAPITime(record.UpdatedAt),
	}
}

func loadSystemRuntimeSummary(request *http.Request, runtimes *orchestrator.SingleService, workspaceID domain.WorkspaceID) (systemRuntimeSummary, error) {
	result := systemRuntimeSummary{state: domain.SystemStopped}
	if runtimes == nil {
		return result, nil
	}
	status, err := runtimes.Status(request.Context(), workspaceID)
	if err != nil {
		return result, err
	}
	if status.System != nil {
		result.state = status.System.State
		for _, service := range status.Services {
			if serviceCountsAsReady(service.State) {
				result.ready++
			}
		}
	}
	operations, err := runtimes.ListOperations(request.Context(), &workspaceID, 50)
	if err != nil {
		return result, err
	}
	result.activeOperationID = activeOperationID(operations)
	return result, nil
}

func serviceCountsAsReady(state domain.ServiceState) bool {
	return state == domain.ServiceReady || state == domain.ServiceCompleted
}

func activeOperationID(operations []orchestrator.Operation) *string {
	for _, operation := range operations {
		if operation.State == domain.OperationQueued || operation.State == domain.OperationRunning || operation.State == domain.OperationCancelling {
			value := operation.ID.String()
			return &value
		}
	}
	return nil
}

func mapSystem(record workspace.Record, runtime systemRuntimeSummary) systemSummaryDTO {
	return systemSummaryDTO{
		ID: record.SystemID.String(), Name: record.SystemName, WorkspaceID: record.ID.String(),
		WorkspacePath: record.RootPath, ManifestStatus: record.ManifestStatus, ManifestDigest: record.LastValidDigest,
		State: string(runtime.state), ServiceSummary: serviceSummaryDTO{Ready: runtime.ready, Total: record.ServiceCount},
		ActiveOperationID: runtime.activeOperationID, UpdatedAt: formatAPITime(record.UpdatedAt),
	}
}

func mapSystemDetail(definition workspace.Definition, runtime systemRuntimeSummary) systemDetailDTO {
	services := make([]serviceDTO, 0, len(definition.Services))
	for _, service := range definition.Services {
		services = append(services, mapService(service))
	}
	return systemDetailDTO{
		System: mapSystem(definition.Workspace, runtime), Services: services,
		Manifest: manifestDTO{Digest: definition.Manifest.Digest, APIVersion: definition.Manifest.APIVersion,
			CreatedAt: formatAPITime(definition.Manifest.CreatedAt)},
	}
}

func mapService(service workspace.ServiceDefinition) serviceDTO {
	return serviceDTO{
		ID: service.ID.String(), Driver: string(service.Driver), Mode: string(service.Mode),
		Required: service.Required, DefinitionDigest: service.DefinitionDigest,
	}
}

func formatAPITime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }
