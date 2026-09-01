package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/go-chi/chi/v5"

	"stackpilot/internal/domain"
	"stackpilot/internal/health"
	"stackpilot/internal/orchestrator"
	"stackpilot/internal/security"
	"stackpilot/internal/workspace"
)

type startSystemRequest struct {
	WorkspaceID   string                 `json:"workspaceId,omitempty"`
	PortOverrides map[string]int         `json:"portOverrides,omitempty"`
	FailurePolicy *failurePolicyOverride `json:"failurePolicy,omitempty"`
}

type failurePolicyOverride struct {
	FailFast          *bool `json:"failFast,omitempty"`
	CleanupOnFailure  *bool `json:"cleanupOnFailure,omitempty"`
	KeepReadyServices *bool `json:"keepReadyServices,omitempty"`
}

type stopSystemRequest struct {
	WorkspaceID string `json:"workspaceId,omitempty"`
}

type operationRefDTO struct {
	OperationID string `json:"operationId"`
	State       string `json:"state"`
}

type operationDTO struct {
	ID                string             `json:"id"`
	WorkspaceID       string             `json:"workspaceId"`
	SystemID          string             `json:"systemId"`
	Type              string             `json:"type"`
	State             string             `json:"state"`
	Cancellable       bool               `json:"cancellable"`
	CancelRequestedAt *string            `json:"cancelRequestedAt,omitempty"`
	ErrorCode         string             `json:"errorCode,omitempty"`
	CreatedAt         string             `json:"createdAt"`
	StartedAt         *string            `json:"startedAt,omitempty"`
	FinishedAt        *string            `json:"finishedAt,omitempty"`
	DurationMillis    *int64             `json:"durationMs,omitempty"`
	Steps             []operationStepDTO `json:"steps"`
}

type operationStepDTO struct {
	Number         int     `json:"number"`
	Key            string  `json:"key"`
	State          string  `json:"state"`
	Attempt        int     `json:"attempt"`
	StartedAt      *string `json:"startedAt,omitempty"`
	FinishedAt     *string `json:"finishedAt,omitempty"`
	DurationMillis *int64  `json:"durationMs,omitempty"`
	ErrorCode      string  `json:"errorCode,omitempty"`
	DetailRef      string  `json:"detailRef,omitempty"`
}

type systemStatusDTO struct {
	SystemID           string              `json:"systemId"`
	WorkspaceID        string              `json:"workspaceId"`
	State              string              `json:"state"`
	InstanceID         string              `json:"instanceId,omitempty"`
	ManifestDigest     string              `json:"manifestDigest,omitempty"`
	ResolvedSpecDigest string              `json:"resolvedSpecDigest,omitempty"`
	StartedAt          string              `json:"startedAt,omitempty"`
	StoppedAt          *string             `json:"stoppedAt,omitempty"`
	Services           []serviceStatusDTO  `json:"services"`
	Ports              []portStatusDTO     `json:"ports"`
	HealthCoverage     []healthCoverageDTO `json:"healthCoverage"`
}

type healthCoverageDTO struct {
	ServiceInstanceID     string  `json:"serviceInstanceId"`
	ReadinessKind         string  `json:"readinessKind,omitempty"`
	LivenessKind          string  `json:"livenessKind,omitempty"`
	Coverage              string  `json:"coverage"`
	SatisfiesVerification bool    `json:"satisfiesVerification"`
	LatestSuccess         *bool   `json:"latestSuccess,omitempty"`
	LatestErrorCode       string  `json:"latestErrorCode,omitempty"`
	LatestCheckedAt       *string `json:"latestCheckedAt,omitempty"`
}

type portStatusDTO struct {
	LogicalName  string `json:"logicalName"`
	Port         int    `json:"port"`
	Source       string `json:"source"`
	Replaced     bool   `json:"replaced"`
	ConflictPort *int   `json:"conflictPort,omitempty"`
}

type serviceStatusDTO struct {
	ServiceID         string                      `json:"serviceId"`
	ServiceInstanceID string                      `json:"serviceInstanceId"`
	Driver            string                      `json:"driver"`
	Mode              string                      `json:"mode"`
	State             string                      `json:"state"`
	StateVersion      int64                       `json:"stateVersion"`
	PID               *int                        `json:"pid,omitempty"`
	ProcessStartedAt  *string                     `json:"processStartedAt,omitempty"`
	CommandDigest     string                      `json:"commandDigest,omitempty"`
	ExitCode          *uint32                     `json:"exitCode,omitempty"`
	DependsOn         []string                    `json:"dependsOn"`
	Containers        []composeContainerStatusDTO `json:"containers"`
}

type composeContainerStatusDTO struct {
	Service  string `json:"service"`
	State    string `json:"state"`
	Health   string `json:"health"`
	ExitCode int    `json:"exitCode"`
}

func registerOperationRoutes(router chi.Router, manager *workspace.Manager, services *orchestrator.SingleService, auth Authenticator, audit security.AuditStore, logger *slog.Logger) {
	router.With(auditMutation(audit, logger, "system.start", "system", "systemID"), browserMutationGuard(auth)).Post("/systems/{systemID}/start", startSystemHandler(manager, services))
	router.With(auditMutation(audit, logger, "system.stop", "system", "systemID"), browserMutationGuard(auth)).Post("/systems/{systemID}/stop", stopSystemHandler(manager, services))
	router.With(auditMutation(audit, logger, "system.restart", "system", "systemID"), browserMutationGuard(auth)).Post("/systems/{systemID}/restart", restartSystemHandler(manager, services))
	router.With(auditMutation(audit, logger, "service.restart", "service", "serviceID"), browserMutationGuard(auth)).Post("/services/{systemID}/{serviceID}/restart", restartServiceHandler(manager, services))
	router.Get("/systems/{systemID}/status", systemStatusHandler(manager, services))
	router.Get("/operations/{operationID}", getOperationHandler(services))
	router.Get("/operations", listOperationsHandler(services))
	router.With(auditMutation(audit, logger, "operation.cancel", "operation", "operationID"), browserMutationGuard(auth)).Post("/operations/{operationID}/cancel", cancelOperationHandler(services))
}

func listOperationsHandler(services *orchestrator.SingleService) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		var workspaceID *domain.WorkspaceID
		if value := request.URL.Query().Get("workspaceId"); value != "" {
			parsed, err := domain.ParseWorkspaceID(value)
			if err != nil {
				writeRegisteredError(response, request, ErrorRequestValidationFailed)
				return
			}
			workspaceID = &parsed
		}
		operations, err := services.ListOperations(request.Context(), workspaceID, 50)
		if err != nil {
			writeBoundaryError(response, request, err)
			return
		}
		items := make([]operationDTO, 0, len(operations))
		for _, operation := range operations {
			items = append(items, mapOperation(operation))
		}
		writeJSON(response, http.StatusOK, struct {
			Items []operationDTO `json:"items"`
		}{Items: items})
	}
}

func restartSystemHandler(workspaces *workspace.Manager, services *orchestrator.SingleService) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		var input startSystemRequest
		if err := decodeJSONRequest(response, request, &input); err != nil {
			writeRegisteredError(response, request, ErrorRequestValidationFailed)
			return
		}
		record, systemID, err := resolveMutationWorkspace(request, workspaces, input.WorkspaceID)
		if err != nil {
			writeBoundaryError(response, request, err)
			return
		}
		encoded, _ := json.Marshal(input)
		result, err := services.SubmitRestart(request.Context(), orchestrator.RestartSystemInput{
			WorkspaceID: record.ID, SystemID: systemID, PortOverrides: input.PortOverrides,
			IdempotencySubject: "local-user", IdempotencyKey: request.Header.Get("Idempotency-Key"), Request: encoded,
			FailurePolicy: mapFailurePolicy(input.FailurePolicy),
		})
		writeOperationSubmission(response, request, result, err)
	}
}

func restartServiceHandler(workspaces *workspace.Manager, services *orchestrator.SingleService) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		var input stopSystemRequest
		if err := decodeJSONRequest(response, request, &input); err != nil {
			writeRegisteredError(response, request, ErrorRequestValidationFailed)
			return
		}
		record, systemID, err := resolveMutationWorkspace(request, workspaces, input.WorkspaceID)
		serviceID, serviceErr := domain.ParseServiceID(chi.URLParam(request, "serviceID"))
		if err != nil || serviceErr != nil {
			if err == nil {
				err = serviceErr
			}
			writeBoundaryError(response, request, err)
			return
		}
		encoded, _ := json.Marshal(input)
		result, err := services.SubmitServiceRestart(request.Context(), orchestrator.RestartServiceInput{
			WorkspaceID: record.ID, SystemID: systemID, ServiceID: serviceID,
			IdempotencySubject: "local-user", IdempotencyKey: request.Header.Get("Idempotency-Key"), Request: encoded,
		})
		writeOperationSubmission(response, request, result, err)
	}
}

func startSystemHandler(workspaces *workspace.Manager, services *orchestrator.SingleService) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		var input startSystemRequest
		if err := decodeJSONRequest(response, request, &input); err != nil {
			writeRegisteredError(response, request, ErrorRequestValidationFailed)
			return
		}
		record, systemID, err := resolveMutationWorkspace(request, workspaces, input.WorkspaceID)
		if err != nil {
			writeBoundaryError(response, request, err)
			return
		}
		encoded, _ := json.Marshal(input)
		result, err := services.SubmitStart(request.Context(), orchestrator.StartSingleServiceInput{
			WorkspaceID: record.ID, SystemID: systemID, PortOverrides: input.PortOverrides,
			IdempotencySubject: "local-user", IdempotencyKey: request.Header.Get("Idempotency-Key"), Request: encoded,
			FailurePolicy: mapFailurePolicy(input.FailurePolicy),
		})
		writeOperationSubmission(response, request, result, err)
	}
}

func mapFailurePolicy(value *failurePolicyOverride) orchestrator.FailurePolicyOverride {
	if value == nil {
		return orchestrator.FailurePolicyOverride{}
	}
	return orchestrator.FailurePolicyOverride{
		FailFast: value.FailFast, CleanupOnFailure: value.CleanupOnFailure, KeepReadyServices: value.KeepReadyServices,
	}
}

func stopSystemHandler(workspaces *workspace.Manager, services *orchestrator.SingleService) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		var input stopSystemRequest
		if err := decodeJSONRequest(response, request, &input); err != nil {
			writeRegisteredError(response, request, ErrorRequestValidationFailed)
			return
		}
		record, systemID, err := resolveMutationWorkspace(request, workspaces, input.WorkspaceID)
		if err != nil {
			writeBoundaryError(response, request, err)
			return
		}
		encoded, _ := json.Marshal(input)
		result, err := services.SubmitStop(request.Context(), orchestrator.StopSingleServiceInput{
			WorkspaceID: record.ID, SystemID: systemID, IdempotencySubject: "local-user",
			IdempotencyKey: request.Header.Get("Idempotency-Key"), Request: encoded,
		})
		writeOperationSubmission(response, request, result, err)
	}
}

func systemStatusHandler(workspaces *workspace.Manager, services *orchestrator.SingleService) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		record, systemID, err := resolveQueryWorkspace(request, workspaces)
		if err != nil {
			writeBoundaryError(response, request, err)
			return
		}
		status, err := services.Status(request.Context(), record.ID)
		if err != nil {
			writeBoundaryError(response, request, err)
			return
		}
		writeJSON(response, http.StatusOK, mapSystemStatus(systemID, record.ID, status))
	}
}

func getOperationHandler(services *orchestrator.SingleService) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		id, err := domain.ParseOperationID(chi.URLParam(request, "operationID"))
		if err != nil {
			writeRegisteredError(response, request, ErrorRequestValidationFailed)
			return
		}
		operation, err := services.GetOperation(request.Context(), id)
		if err != nil {
			writeBoundaryError(response, request, err)
			return
		}
		writeJSON(response, http.StatusOK, mapOperation(*operation))
	}
}

func cancelOperationHandler(services *orchestrator.SingleService) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		id, err := domain.ParseOperationID(chi.URLParam(request, "operationID"))
		if err != nil {
			writeRegisteredError(response, request, ErrorRequestValidationFailed)
			return
		}
		operation, err := services.CancelOperation(request.Context(), id)
		if err != nil {
			writeBoundaryError(response, request, err)
			return
		}
		writeJSON(response, http.StatusAccepted, operationRefDTO{OperationID: operation.ID.String(), State: string(operation.State)})
	}
}

func resolveMutationWorkspace(request *http.Request, manager *workspace.Manager, workspaceValue string) (*workspace.Record, domain.SystemID, error) {
	systemID, err := domain.ParseSystemID(chi.URLParam(request, "systemID"))
	if err != nil {
		return nil, "", err
	}
	var workspaceID *domain.WorkspaceID
	if workspaceValue != "" {
		parsed, err := domain.ParseWorkspaceID(workspaceValue)
		if err != nil {
			return nil, "", err
		}
		workspaceID = &parsed
	}
	record, err := manager.ResolveSystem(request.Context(), systemID, workspaceID)
	return record, systemID, err
}

func resolveQueryWorkspace(request *http.Request, manager *workspace.Manager) (*workspace.Record, domain.SystemID, error) {
	return resolveMutationWorkspace(request, manager, request.URL.Query().Get("workspaceId"))
}

func writeOperationSubmission(response http.ResponseWriter, request *http.Request, result *orchestrator.CreateResult, err error) {
	if err != nil {
		writeBoundaryError(response, request, err)
		return
	}
	writeJSON(response, http.StatusAccepted, operationRefDTO{OperationID: result.Operation.ID.String(), State: string(result.Operation.State)})
}

func mapOperation(operation orchestrator.Operation) operationDTO {
	steps := make([]operationStepDTO, 0, len(operation.Steps))
	for _, step := range operation.Steps {
		steps = append(steps, operationStepDTO{
			Number: step.Number, Key: step.Key, State: string(step.State), Attempt: step.Attempt,
			StartedAt: optionalAPITime(step.StartedAt), FinishedAt: optionalAPITime(step.FinishedAt),
			DurationMillis: step.DurationMillis, ErrorCode: step.ErrorCode, DetailRef: step.DetailRef,
		})
	}
	return operationDTO{
		ID: operation.ID.String(), WorkspaceID: operation.WorkspaceID.String(), SystemID: operation.SystemID.String(),
		Type: string(operation.Type), State: string(operation.State), Cancellable: operation.Cancellable,
		CancelRequestedAt: optionalAPITime(operation.CancelRequestedAt), ErrorCode: operation.ErrorCode,
		CreatedAt: formatAPITime(operation.CreatedAt), StartedAt: optionalAPITime(operation.StartedAt),
		FinishedAt: optionalAPITime(operation.FinishedAt), DurationMillis: operation.DurationMillis, Steps: steps,
	}
}

func mapSystemStatus(systemID domain.SystemID, workspaceID domain.WorkspaceID, status orchestrator.RuntimeStatus) systemStatusDTO {
	result := systemStatusDTO{SystemID: systemID.String(), WorkspaceID: workspaceID.String(), State: string(domain.SystemStopped), Services: []serviceStatusDTO{}, Ports: []portStatusDTO{}, HealthCoverage: []healthCoverageDTO{}}
	if status.System == nil {
		return result
	}
	result.State, result.InstanceID = string(status.System.State), status.System.ID.String()
	result.ManifestDigest, result.ResolvedSpecDigest = status.System.ManifestDigest, status.System.ResolvedSpecDigest
	result.StartedAt, result.StoppedAt = formatAPITime(status.System.StartedAt), optionalAPITime(status.System.StoppedAt)
	for _, service := range status.Services {
		mapped := mapServiceStatus(service)
		if status.Resolved != nil {
			resolved := status.Resolved.Services[service.ServiceID.String()]
			mapped.Driver = string(resolved.Driver)
			mapped.Mode = string(resolved.Process.Mode)
			if resolved.Driver == domain.DriverCompose {
				mapped.Mode = string(domain.ProcessDaemon)
			}
			for dependency := range resolved.Dependencies {
				mapped.DependsOn = append(mapped.DependsOn, dependency)
			}
			sort.Strings(mapped.DependsOn)
		}
		for _, container := range status.ComposeContainers[service.ID] {
			mapped.Containers = append(mapped.Containers, composeContainerStatusDTO{
				Service: container.Service, State: container.State, Health: container.Health, ExitCode: container.ExitCode,
			})
		}
		result.Services = append(result.Services, mapped)
		result.HealthCoverage = append(result.HealthCoverage, mapHealthCoverage(service.ID, status.HealthCoverage[service.ID]))
	}
	if status.Resolved != nil {
		logicalNames := make([]string, 0, len(status.Resolved.Ports))
		for logicalName := range status.Resolved.Ports {
			logicalNames = append(logicalNames, logicalName)
		}
		sort.Strings(logicalNames)
		for _, logicalName := range logicalNames {
			port := status.Resolved.Ports[logicalName]
			result.Ports = append(result.Ports, portStatusDTO{LogicalName: logicalName, Port: port.Port, Source: port.Source, Replaced: port.Replaced, ConflictPort: port.ConflictPort})
		}
	}
	return result
}

func mapHealthCoverage(instanceID domain.ServiceInstanceID, summary health.CoverageSummary) healthCoverageDTO {
	result := healthCoverageDTO{
		ServiceInstanceID: instanceID.String(), ReadinessKind: string(summary.ReadinessKind),
		LivenessKind: string(summary.LivenessKind), Coverage: string(summary.Coverage),
		SatisfiesVerification: summary.SatisfiesVerification,
	}
	if summary.Latest != nil {
		success := summary.Latest.Success
		result.LatestSuccess = &success
		result.LatestErrorCode = string(summary.Latest.ErrorCode)
		result.LatestCheckedAt = optionalAPITime(&summary.Latest.CheckedAt)
	}
	return result
}

func mapServiceStatus(service domain.ServiceInstance) serviceStatusDTO {
	result := serviceStatusDTO{
		ServiceID: service.ServiceID.String(), ServiceInstanceID: service.ID.String(), State: string(service.State),
		StateVersion: service.StateVersion, ExitCode: service.ExitCode,
		Driver: string(service.Driver), Mode: string(service.ProcessMode), DependsOn: []string{}, Containers: []composeContainerStatusDTO{},
	}
	if service.Identity != nil {
		pid := service.Identity.PID
		result.PID, result.CommandDigest = &pid, service.Identity.CommandDigest
		result.ProcessStartedAt = optionalAPITime(&service.Identity.StartedAt)
	}
	return result
}

func optionalAPITime(value *time.Time) *string {
	if value == nil {
		return nil
	}
	formatted := formatAPITime(*value)
	return &formatted
}

func operationBoundaryError(err error) (ErrorCode, bool) {
	switch {
	case errors.Is(err, orchestrator.ErrOperationAlreadyActive):
		return ErrorOperationAlreadyActive, true
	case errors.Is(err, orchestrator.ErrIdempotencyKeyReused):
		return ErrorIdempotencyKeyReused, true
	case errors.Is(err, orchestrator.ErrOperationNotFound):
		return ErrorOperationNotFound, true
	case errors.Is(err, orchestrator.ErrNotCancellable):
		return ErrorOperationNotCancellable, true
	case errors.Is(err, orchestrator.ErrInvalidTransition):
		return ErrorOperationInvalidState, true
	case errors.Is(err, orchestrator.ErrSystemAlreadyActive):
		return ErrorOperationInvalidState, true
	case errors.Is(err, orchestrator.ErrSingleServiceScope), errors.Is(err, orchestrator.ErrPortPlanRequired):
		return ErrorFeatureNotEnabled, true
	case errors.Is(err, orchestrator.ErrInvalidInput):
		return ErrorRequestValidationFailed, true
	case errors.Is(err, orchestrator.ErrManifestChanged):
		return ErrorManifestChanged, true
	case errors.Is(err, orchestrator.ErrChangePlanStale):
		return ErrorChangePlanStale, true
	case errors.Is(err, orchestrator.ErrChangePlanBlocked):
		return ErrorChangePlanBlocked, true
	case errors.Is(err, orchestrator.ErrChangePlanInvalidState):
		return ErrorChangePlanInvalidState, true
	case errors.Is(err, orchestrator.ErrVerificationHealthIncomplete):
		return ErrorVerificationHealthIncomplete, true
	case errors.Is(err, orchestrator.ErrVerificationUnavailable):
		return ErrorVerificationUnavailable, true
	case errors.Is(err, orchestrator.ErrVerificationFailed):
		return ErrorVerificationFailed, true
	default:
		return "", false
	}
}
