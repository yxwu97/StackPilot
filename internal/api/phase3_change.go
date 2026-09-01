package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"

	"github.com/go-chi/chi/v5"

	"stackpilot/internal/capability"
	"stackpilot/internal/changeplan"
	"stackpilot/internal/domain"
	"stackpilot/internal/orchestrator"
	"stackpilot/internal/revision"
	"stackpilot/internal/workspace"
)

type revisionSummaryDTO struct {
	ID               string  `json:"id"`
	WorkspaceID      string  `json:"workspaceId"`
	SystemID         string  `json:"systemId"`
	SystemInstanceID *string `json:"systemInstanceId,omitempty"`
	Kind             string  `json:"kind"`
	SchemaVersion    string  `json:"schemaVersion"`
	Digest           string  `json:"digest"`
	CreatedAt        string  `json:"createdAt"`
}

type changePlanItemDTO struct {
	Kind    string `json:"kind"`
	Change  string `json:"change"`
	Risk    string `json:"risk"`
	Key     string `json:"key"`
	Summary string `json:"summary"`
}

type changePlanDTO struct {
	ID                   string              `json:"id"`
	WorkspaceID          string              `json:"workspaceId"`
	SystemID             string              `json:"systemId"`
	CreatedByOperationID string              `json:"createdByOperationId"`
	FromRevision         revisionSummaryDTO  `json:"fromRevision"`
	ToRevision           revisionSummaryDTO  `json:"toRevision"`
	RuleVersion          string              `json:"ruleVersion"`
	State                string              `json:"state"`
	Risk                 string              `json:"risk"`
	ItemCount            int                 `json:"itemCount"`
	BlockedCount         int                 `json:"blockedCount"`
	Items                []changePlanItemDTO `json:"items"`
	CreatedAt            string              `json:"createdAt"`
}

type verifiedRestartRequest struct {
	ChangePlanID string `json:"changePlanId"`
}

func registerChangePlanRoutes(router chi.Router, config Config) {
	if !capabilityEnabled(config.Capabilities, capability.Phase3ChangePlanning) {
		registerClosedChangePlanRoutes(router, config.Auth)
		return
	}
	router.Get("/workspaces/{workspaceID}/revisions", listRevisionsHandler(config.Workspaces, config.SingleService))
	router.Get("/revisions/{revisionID}", getRevisionHandler(config.SingleService))
	router.Get("/change-plans/{changePlanID}", getChangePlanHandler(config.SingleService))
	router.With(auditMutation(config.Audit, config.Logger, "change-plan.create", "workspace", "workspaceID"),
		browserMutationGuard(config.Auth)).Post("/workspaces/{workspaceID}/change-plans", createChangePlanHandler(config.Workspaces, config.SingleService))
}

func registerClosedChangePlanRoutes(router chi.Router, auth Authenticator) {
	router.Get("/workspaces/{workspaceID}/revisions", featureNotEnabledHandler(capability.Phase3ChangePlanning))
	router.Get("/revisions/{revisionID}", featureNotEnabledHandler(capability.Phase3ChangePlanning))
	router.Get("/change-plans/{changePlanID}", featureNotEnabledHandler(capability.Phase3ChangePlanning))
	router.With(browserMutationGuard(auth)).Post("/workspaces/{workspaceID}/change-plans", featureNotEnabledHandler(capability.Phase3ChangePlanning))
}

func createChangePlanHandler(workspaces *workspace.Manager, services *orchestrator.SingleService) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		if workspaces == nil || services == nil {
			writeRegisteredError(response, request, ErrorInternal)
			return
		}
		workspaceID, err := domain.ParseWorkspaceID(chi.URLParam(request, "workspaceID"))
		if err != nil {
			writeRegisteredError(response, request, ErrorRequestValidationFailed)
			return
		}
		record, err := workspaces.Get(request.Context(), workspaceID)
		if err != nil {
			writeBoundaryError(response, request, err)
			return
		}
		encoded, _ := json.Marshal(struct {
			WorkspaceID string `json:"workspaceId"`
		}{WorkspaceID: workspaceID.String()})
		result, err := services.SubmitChangePlan(request.Context(), orchestrator.ChangePlanInput{
			WorkspaceID: workspaceID, SystemID: record.SystemID, IdempotencySubject: "local-user",
			IdempotencyKey: request.Header.Get("Idempotency-Key"), Request: encoded,
		})
		writeOperationSubmission(response, request, result, err)
	}
}

func verifiedRestartHandler(workspaces *workspace.Manager, services *orchestrator.SingleService) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		if workspaces == nil || services == nil {
			writeRegisteredError(response, request, ErrorInternal)
			return
		}
		workspaceID, err := domain.ParseWorkspaceID(chi.URLParam(request, "workspaceID"))
		var input verifiedRestartRequest
		if err != nil || decodeJSONRequest(response, request, &input) != nil {
			writeRegisteredError(response, request, ErrorRequestValidationFailed)
			return
		}
		planID, err := domain.ParseChangePlanID(input.ChangePlanID)
		if err != nil {
			writeRegisteredError(response, request, ErrorRequestValidationFailed)
			return
		}
		record, err := workspaces.Get(request.Context(), workspaceID)
		if err != nil {
			writeBoundaryError(response, request, err)
			return
		}
		encoded, _ := json.Marshal(input)
		result, err := services.SubmitVerifiedRestart(request.Context(), orchestrator.VerifiedRestartInput{
			WorkspaceID: workspaceID, SystemID: record.SystemID, ChangePlanID: planID,
			IdempotencySubject: "local-user", IdempotencyKey: request.Header.Get("Idempotency-Key"), Request: encoded,
		})
		if err != nil {
			writePhase3Error(response, request, err)
			return
		}
		writeOperationSubmission(response, request, result, nil)
	}
}

func listRevisionsHandler(workspaces *workspace.Manager, services *orchestrator.SingleService) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		workspaceID, err := domain.ParseWorkspaceID(chi.URLParam(request, "workspaceID"))
		if err != nil || workspaces == nil || services == nil {
			writeRegisteredError(response, request, ErrorRequestValidationFailed)
			return
		}
		if _, err := workspaces.Get(request.Context(), workspaceID); err != nil {
			writeBoundaryError(response, request, err)
			return
		}
		values, err := services.ListRevisions(request.Context(), workspaceID, 100)
		if err != nil {
			writePhase3Error(response, request, err)
			return
		}
		items := make([]revisionSummaryDTO, 0, len(values))
		for _, value := range values {
			items = append(items, mapRevisionSummary(value))
		}
		writeJSON(response, http.StatusOK, struct {
			Items []revisionSummaryDTO `json:"items"`
		}{Items: items})
	}
}

func getRevisionHandler(services *orchestrator.SingleService) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		id, err := domain.ParseRevisionID(chi.URLParam(request, "revisionID"))
		if err != nil || services == nil {
			writeRegisteredError(response, request, ErrorRequestValidationFailed)
			return
		}
		value, err := services.GetRevision(request.Context(), id)
		if err != nil {
			writePhase3Error(response, request, err)
			return
		}
		writeJSON(response, http.StatusOK, mapRevisionSummary(*value))
	}
}

func getChangePlanHandler(services *orchestrator.SingleService) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		id, err := domain.ParseChangePlanID(chi.URLParam(request, "changePlanID"))
		if err != nil || services == nil {
			writeRegisteredError(response, request, ErrorRequestValidationFailed)
			return
		}
		plan, err := services.GetChangePlan(request.Context(), id)
		if err != nil {
			writePhase3Error(response, request, err)
			return
		}
		writeJSON(response, http.StatusOK, mapChangePlan(*plan))
	}
}

func mapRevisionSummary(value revision.Record) revisionSummaryDTO {
	var instanceID *string
	if value.SystemInstanceID != nil {
		encoded := value.SystemInstanceID.String()
		instanceID = &encoded
	}
	return revisionSummaryDTO{ID: value.ID.String(), WorkspaceID: value.WorkspaceID.String(), SystemID: value.SystemID.String(),
		SystemInstanceID: instanceID, Kind: string(value.Kind), SchemaVersion: value.SchemaVersion, Digest: value.Digest,
		CreatedAt: formatAPITime(value.CreatedAt)}
}

func mapChangePlan(value changeplan.Plan) changePlanDTO {
	items := make([]changePlanItemDTO, 0, len(value.Result.Items))
	for _, finding := range value.Result.Items {
		items = append(items, changePlanItemDTO{Kind: string(finding.Kind), Change: string(finding.Change), Risk: string(finding.Risk), Key: finding.Key, Summary: finding.Summary})
	}
	sort.SliceStable(items, func(left, right int) bool {
		return items[left].Kind+items[left].Key < items[right].Kind+items[right].Key
	})
	record := value.Record
	return changePlanDTO{ID: record.ID.String(), WorkspaceID: record.WorkspaceID.String(), SystemID: record.SystemID.String(),
		CreatedByOperationID: record.CreatedByOperationID.String(), FromRevision: mapRevisionSummary(value.From), ToRevision: mapRevisionSummary(value.To),
		RuleVersion: record.RuleVersion, State: string(record.State), Risk: string(record.Risk), ItemCount: record.ItemCount,
		BlockedCount: record.BlockedCount, Items: items, CreatedAt: formatAPITime(record.CreatedAt)}
}

func writePhase3Error(response http.ResponseWriter, request *http.Request, err error) {
	switch {
	case errors.Is(err, revision.ErrNotFound):
		writeRegisteredError(response, request, ErrorRevisionNotFound)
	case errors.Is(err, changeplan.ErrNotFound):
		writeRegisteredError(response, request, ErrorChangePlanNotFound)
	case errors.Is(err, orchestrator.ErrChangePlanStale):
		writeRegisteredError(response, request, ErrorChangePlanStale)
	case errors.Is(err, orchestrator.ErrChangePlanBlocked):
		writeRegisteredError(response, request, ErrorChangePlanBlocked)
	case errors.Is(err, orchestrator.ErrChangePlanInvalidState):
		writeRegisteredError(response, request, ErrorChangePlanInvalidState)
	case errors.Is(err, orchestrator.ErrVerificationHealthIncomplete):
		writeRegisteredError(response, request, ErrorVerificationHealthIncomplete)
	case errors.Is(err, orchestrator.ErrVerificationUnavailable):
		writeRegisteredError(response, request, ErrorVerificationUnavailable)
	case errors.Is(err, orchestrator.ErrVerificationFailed):
		writeRegisteredError(response, request, ErrorVerificationFailed)
	case errors.Is(err, revision.ErrInvalidInput), errors.Is(err, changeplan.ErrInvalidInput):
		writeRegisteredError(response, request, ErrorRequestValidationFailed)
	default:
		writeBoundaryError(response, request, err)
	}
}
