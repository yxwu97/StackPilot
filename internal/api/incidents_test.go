package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"stackpilot/internal/domain"
	"stackpilot/internal/incident"
	"stackpilot/internal/orchestrator"
)

func TestIncidentRoutesExposeBoundedEvidenceAndValidateScope(t *testing.T) {
	now := time.Date(2026, 8, 18, 8, 0, 0, 0, time.UTC)
	store := &fakeIncidentStore{record: incident.Record{
		ID: "inc_01ARZ3NDEKTSV4RRFFQ69G5FAV", Severity: incident.SeverityWarning, State: incident.StateOpen,
		OccurrenceCount: 2, FirstSeenAt: now, LastSeenAt: now,
		Context: incident.Context{
			SchemaVersion: "1", WorkspaceID: "ws_01ARZ3NDEKTSV4RRFFQ69G5FAV", Kind: incident.KindLivenessFailure,
			TriggerCode: "TCP_REFUSED", WindowStart: now.Add(-time.Minute), WindowEnd: now.Add(time.Minute),
			Dependencies: map[string]domain.ServiceState{}, Ports: map[string]int{}, Evidence: []incident.EvidenceRef{}, Logs: []incident.LogLine{},
		},
	}, analysis: incident.Analysis{ID: 1, IncidentID: "inc_01ARZ3NDEKTSV4RRFFQ69G5FAV", Engine: "rules", SchemaVersion: "1", Result: json.RawMessage(`{"results":[]}`), CreatedAt: now}}
	handler := newRouter(Config{Incidents: store}, newTestSPAHandler(t))

	invalid := performRequest(handler, http.MethodGet, "/api/v1/incidents")
	assertErrorCode(t, invalid, http.StatusBadRequest, ErrorRequestValidationFailed)

	list := performRequest(handler, http.MethodGet, "/api/v1/incidents?workspaceId=ws_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	if list.Code != http.StatusOK || !json.Valid(list.Body.Bytes()) {
		t.Fatalf("incident list = (%d,%s)", list.Code, list.Body.String())
	}
	detail := performRequest(handler, http.MethodGet, "/api/v1/incidents/inc_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	if detail.Code != http.StatusOK || !containsJSONText(detail.Body.String(), `"engine":"rules"`) {
		t.Fatalf("incident detail = (%d,%s)", detail.Code, detail.Body.String())
	}
}

func TestAnalyzeIncidentQueuesReadOnlyOperation(t *testing.T) {
	service := &fakeIncidentAnalysisService{}
	store := &fakeIncidentStore{record: incident.Record{ID: "inc_01ARZ3NDEKTSV4RRFFQ69G5FAV"}}
	router := chi.NewRouter()
	router.Post("/api/v1/incidents/{incidentID}/analyze", analyzeIncidentHandler(store, service))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/incidents/inc_01ARZ3NDEKTSV4RRFFQ69G5FAV/analyze", strings.NewReader("{}"))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "analysis-1")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || service.input.IdempotencyKey != "analysis-1" || service.input.IncidentID.String() == "" {
		t.Fatalf("analyze response/input = (%d,%s,%#v)", response.Code, response.Body.String(), service.input)
	}
}

type fakeIncidentStore struct {
	record   incident.Record
	analysis incident.Analysis
}

type fakeIncidentAnalysisService struct {
	input orchestrator.AnalyzeIncidentInput
}

func (service *fakeIncidentAnalysisService) SubmitIncidentAnalysis(_ context.Context, input orchestrator.AnalyzeIncidentInput) (*orchestrator.CreateResult, error) {
	service.input = input
	return &orchestrator.CreateResult{Operation: orchestrator.Operation{ID: "op_01ARZ3NDEKTSV4RRFFQ69G5FAV", State: domain.OperationQueued}}, nil
}

func (store *fakeIncidentStore) List(context.Context, domain.WorkspaceID, int) ([]incident.Record, error) {
	return []incident.Record{store.record}, nil
}

func (store *fakeIncidentStore) Get(_ context.Context, id domain.IncidentID) (*incident.Record, error) {
	if id != store.record.ID {
		return nil, sql.ErrNoRows
	}
	copy := store.record
	return &copy, nil
}

func (store *fakeIncidentStore) ListAnalyses(context.Context, domain.IncidentID, int) ([]incident.Analysis, error) {
	return []incident.Analysis{store.analysis}, nil
}

func containsJSONText(value, fragment string) bool { return strings.Contains(value, fragment) }
