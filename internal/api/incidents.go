package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"stackpilot/internal/domain"
	"stackpilot/internal/incident"
	"stackpilot/internal/orchestrator"
	"stackpilot/internal/security"
)

func registerIncidentRoutes(router chi.Router, store incidentStore, services *orchestrator.SingleService, auth Authenticator, audit security.AuditStore, logger *slog.Logger) {
	router.Get("/incidents", listIncidentsHandler(store))
	router.Get("/incidents/{incidentID}", getIncidentHandler(store))
	if services != nil {
		router.With(auditMutation(audit, logger, "incident.analyze", "incident", "incidentID"), browserMutationGuard(auth)).Post("/incidents/{incidentID}/analyze", analyzeIncidentHandler(store, services))
	}
}

type analyzeIncidentRequest struct{}

type incidentAnalysisService interface {
	SubmitIncidentAnalysis(context.Context, orchestrator.AnalyzeIncidentInput) (*orchestrator.CreateResult, error)
}

type incidentDTO struct {
	ID                string           `json:"id"`
	WorkspaceID       string           `json:"workspaceId"`
	SystemInstanceID  string           `json:"systemInstanceId,omitempty"`
	ServiceInstanceID string           `json:"serviceInstanceId,omitempty"`
	ServiceID         string           `json:"serviceId,omitempty"`
	Kind              string           `json:"kind"`
	Severity          string           `json:"severity"`
	State             string           `json:"state"`
	OccurrenceCount   int              `json:"occurrenceCount"`
	Context           incident.Context `json:"context"`
	FirstSeenAt       string           `json:"firstSeenAt"`
	LastSeenAt        string           `json:"lastSeenAt"`
}

type incidentAnalysisDTO struct {
	ID            int64           `json:"id"`
	Engine        string          `json:"engine"`
	SchemaVersion string          `json:"schemaVersion"`
	Result        json.RawMessage `json:"result"`
	CreatedAt     string          `json:"createdAt"`
}

type incidentDetailDTO struct {
	Incident incidentDTO           `json:"incident"`
	Analyses []incidentAnalysisDTO `json:"analyses"`
}

func listIncidentsHandler(store incidentStore) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		workspaceID, err := domain.ParseWorkspaceID(request.URL.Query().Get("workspaceId"))
		limit := parseIncidentLimit(request.URL.Query().Get("limit"))
		if err != nil || limit == 0 {
			writeRegisteredError(response, request, ErrorRequestValidationFailed)
			return
		}
		records, err := store.List(request.Context(), workspaceID, limit)
		if err != nil {
			writeBoundaryError(response, request, err)
			return
		}
		items := make([]incidentDTO, 0, len(records))
		for _, record := range records {
			items = append(items, mapIncident(record))
		}
		writeJSON(response, http.StatusOK, map[string]any{"items": items})
	}
}

func getIncidentHandler(store incidentStore) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		id, err := domain.ParseIncidentID(chi.URLParam(request, "incidentID"))
		if err != nil {
			writeRegisteredError(response, request, ErrorResourceNotFound)
			return
		}
		record, err := store.Get(request.Context(), id)
		if errors.Is(err, sql.ErrNoRows) {
			writeRegisteredError(response, request, ErrorResourceNotFound)
			return
		}
		if err != nil {
			writeBoundaryError(response, request, err)
			return
		}
		analyses, err := store.ListAnalyses(request.Context(), id, 100)
		if err != nil {
			writeBoundaryError(response, request, err)
			return
		}
		writeJSON(response, http.StatusOK, incidentDetailDTO{Incident: mapIncident(*record), Analyses: mapIncidentAnalyses(analyses)})
	}
}

func analyzeIncidentHandler(store incidentStore, services incidentAnalysisService) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		id, err := domain.ParseIncidentID(chi.URLParam(request, "incidentID"))
		if err != nil {
			writeRegisteredError(response, request, ErrorRequestValidationFailed)
			return
		}
		if _, err := store.Get(request.Context(), id); errors.Is(err, sql.ErrNoRows) {
			writeRegisteredError(response, request, ErrorResourceNotFound)
			return
		} else if err != nil {
			writeBoundaryError(response, request, err)
			return
		}
		var input analyzeIncidentRequest
		if err := decodeJSONRequest(response, request, &input); err != nil {
			writeRegisteredError(response, request, ErrorRequestValidationFailed)
			return
		}
		result, err := services.SubmitIncidentAnalysis(request.Context(), orchestrator.AnalyzeIncidentInput{
			IncidentID: id, IdempotencySubject: "local-user", IdempotencyKey: request.Header.Get("Idempotency-Key"), Request: []byte("{}"),
		})
		writeOperationSubmission(response, request, result, err)
	}
}

func parseIncidentLimit(value string) int {
	if value == "" {
		return 50
	}
	limit, err := strconv.Atoi(value)
	if err != nil || limit < 1 || limit > 200 {
		return 0
	}
	return limit
}

func mapIncident(record incident.Record) incidentDTO {
	return incidentDTO{
		ID: record.ID.String(), WorkspaceID: record.Context.WorkspaceID.String(), SystemInstanceID: record.Context.SystemInstanceID.String(),
		ServiceInstanceID: record.Context.ServiceInstanceID.String(), ServiceID: record.Context.ServiceID.String(), Kind: string(record.Kind()),
		Severity: string(record.Severity), State: string(record.State), OccurrenceCount: record.OccurrenceCount,
		Context: record.Context, FirstSeenAt: formatAPITime(record.FirstSeenAt), LastSeenAt: formatAPITime(record.LastSeenAt),
	}
}

func mapIncidentAnalyses(values []incident.Analysis) []incidentAnalysisDTO {
	result := make([]incidentAnalysisDTO, 0, len(values))
	for _, value := range values {
		result = append(result, incidentAnalysisDTO{ID: value.ID, Engine: value.Engine, SchemaVersion: value.SchemaVersion, Result: value.Result, CreatedAt: formatAPITime(value.CreatedAt)})
	}
	return result
}
