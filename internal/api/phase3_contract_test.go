package api

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"stackpilot/internal/capability"
	"stackpilot/internal/changeplan"
	"stackpilot/internal/domain"
	"stackpilot/internal/manifest"
	"stackpilot/internal/metrics"
	"stackpilot/internal/orchestrator"
	"stackpilot/internal/revision"
	"stackpilot/internal/storage"
	"stackpilot/internal/workspace"
)

type metricQueryStub struct {
	window metrics.Window
	result metrics.WindowResult
	err    error
}

func (stub *metricQueryStub) Query(_ context.Context, window metrics.Window) (metrics.WindowResult, error) {
	stub.window = window
	return stub.result, stub.err
}

func TestPhase3ContractRoutesRemainCapabilityGated(t *testing.T) {
	t.Parallel()
	handler := newRouter(Config{}, newTestSPAHandler(t))
	tests := []struct {
		method  string
		path    string
		feature string
	}{
		{method: http.MethodGet, path: "/api/v1/workspaces/ws_01ARZ3NDEKTSV4RRFFQ69G5FAV/metrics", feature: capability.Phase3ResourceMonitoring},
		{method: http.MethodGet, path: "/api/v1/workspaces/ws_01ARZ3NDEKTSV4RRFFQ69G5FAV/revisions", feature: capability.Phase3ChangePlanning},
		{method: http.MethodGet, path: "/api/v1/revisions/rev_01ARZ3NDEKTSV4RRFFQ69G5FAV", feature: capability.Phase3ChangePlanning},
		{method: http.MethodGet, path: "/api/v1/change-plans/plan_01ARZ3NDEKTSV4RRFFQ69G5FAV", feature: capability.Phase3ChangePlanning},
		{method: http.MethodPost, path: "/api/v1/workspaces/ws_01ARZ3NDEKTSV4RRFFQ69G5FAV/change-plans", feature: capability.Phase3ChangePlanning},
		{method: http.MethodPost, path: "/api/v1/workspaces/ws_01ARZ3NDEKTSV4RRFFQ69G5FAV/verified-restart", feature: capability.Phase3VerifiedRestart},
	}
	for _, test := range tests {
		test := test
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			response := performRequest(handler, test.method, test.path)
			if response.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422", response.Code)
			}
			var body errorEnvelope
			decodeResponse(t, response, &body)
			if body.Error.Code != ErrorFeatureNotEnabled || body.Error.Details["feature"] != test.feature {
				t.Fatalf("error = %#v, want gated feature %q", body.Error, test.feature)
			}
		})
	}
}

func TestWorkspaceMetricsRouteReturnsBoundedSafeDTO(t *testing.T) {
	manager, workspaceID := newMetricWorkspaceManager(t)
	start := time.Date(2026, 8, 31, 10, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	cpu, memory := 5.25, int64(2048)
	queries := &metricQueryStub{result: metrics.WindowResult{Start: start, End: end, Series: []metrics.Series{{
		ServiceID: "backend", Source: domain.MetricSourceProcessJob, Points: []metrics.Point{{ObservedAt: start.Add(time.Minute),
			Status: domain.MetricAvailable, CPUPercent: &cpu, MemoryBytes: &memory}},
	}}}}
	handler := newRouter(Config{Capabilities: []string{capability.Phase3ResourceMonitoring}, Workspaces: manager, MetricQueries: queries}, newTestSPAHandler(t))
	response := performRequest(handler, http.MethodGet, "/api/v1/workspaces/"+workspaceID.String()+"/metrics?from=2026-08-31T10:00:00Z&to=2026-08-31T11:00:00Z&granularity=detail&serviceId=backend")
	if response.Code != http.StatusOK {
		t.Fatalf("GET metrics = (%d, %q)", response.Code, response.Body.String())
	}
	var body metricSeriesListDTO
	decodeResponse(t, response, &body)
	if body.Granularity != "detail" || len(body.Series) != 1 || body.Series[0].ServiceID != "backend" || len(body.Series[0].Points) != 1 {
		t.Fatalf("metric response = %#v", body)
	}
	if queries.window.WorkspaceID != workspaceID || len(queries.window.ServiceIDs) != 1 || queries.window.Limit != metrics.MaximumPoints {
		t.Fatalf("metric query = %#v", queries.window)
	}
	if response.Body.String() == "" || containsUnsafeMetricIdentity(response.Body.String()) {
		t.Fatalf("metric response leaked platform identity: %s", response.Body.String())
	}
}

func TestWorkspaceMetricsRouteRejectsInvalidWindowAndMissingWorkspace(t *testing.T) {
	manager, workspaceID := newMetricWorkspaceManager(t)
	handler := newRouter(Config{Capabilities: []string{capability.Phase3ResourceMonitoring}, Workspaces: manager, MetricQueries: &metricQueryStub{}}, newTestSPAHandler(t))
	invalid := []string{
		"?from=2026-08-31T10:00:00%2B08:00&to=2026-08-31T11:00:00%2B08:00&granularity=detail",
		"?from=2026-08-31T10:00:00Z&to=2026-08-31T09:00:00Z&granularity=detail",
		"?from=2026-08-31T10:00:00Z&to=2026-08-31T11:00:00Z&granularity=minute",
		"?from=2026-08-31T10:00:00Z&to=2026-08-31T11:00:00Z&granularity=detail&serviceId=bad%20id",
	}
	for _, suffix := range invalid {
		response := performRequest(handler, http.MethodGet, "/api/v1/workspaces/"+workspaceID.String()+"/metrics"+suffix)
		assertErrorCode(t, response, http.StatusBadRequest, ErrorMetricQueryInvalid)
	}
	response := performRequest(handler, http.MethodGet, "/api/v1/workspaces/ws_01ARZ3NDEKTSV4RRFFQ69G5FAA/metrics?from=2026-08-31T10:00:00Z&to=2026-08-31T11:00:00Z&granularity=hour")
	assertErrorCode(t, response, http.StatusNotFound, ErrorWorkspaceNotFound)
}

func TestChangePlanDTOContainsOnlyBoundedComparisonFacts(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	workspaceID := domain.WorkspaceID("ws_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	from := revision.Record{ID: "rev_01ARZ3NDEKTSV4RRFFQ69G5FAV", WorkspaceID: workspaceID, SystemID: "sample",
		Kind: domain.RevisionRunning, SchemaVersion: revision.SchemaVersion, Digest: strings.Repeat("a", 64), CreatedAt: now}
	to := revision.Record{ID: "rev_01ARZ3NDEKTSV4RRFFQ69G5FAW", WorkspaceID: workspaceID, SystemID: "sample",
		Kind: domain.RevisionWorkspace, SchemaVersion: revision.SchemaVersion, Digest: strings.Repeat("b", 64), CreatedAt: now}
	plan := changeplan.Plan{Record: changeplan.Record{
		ID: "plan_01ARZ3NDEKTSV4RRFFQ69G5FAV", CreatedByOperationID: "op_01ARZ3NDEKTSV4RRFFQ69G5FAV",
		WorkspaceID: workspaceID, SystemID: "sample", RuleVersion: changeplan.RuleVersion,
		State: domain.ChangePlanReady, Risk: domain.ChangeRiskHigh, ItemCount: 1, CreatedAt: now,
	}, From: from, To: to, Result: changeplan.Result{Items: []changeplan.Item{{
		Kind: domain.ChangeItemRunner, Change: changeplan.ChangeChanged, Risk: domain.ChangeRiskHigh,
		Key: "backend", Summary: "The resolved Runner identity changed.",
	}}}}
	encoded, err := json.Marshal(mapChangePlan(plan))
	if err != nil {
		t.Fatal(err)
	}
	value := string(encoded)
	for _, forbidden := range []string{"workingDirectory", "arguments", "environment", "secretValue", `C:\\`, "executablePath", "gitOutput"} {
		if strings.Contains(value, forbidden) {
			t.Fatalf("ChangePlan DTO leaked %q: %s", forbidden, value)
		}
	}
	for _, required := range []string{`"state":"ready"`, `"risk":"high"`, `"kind":"runner"`, `"key":"backend"`} {
		if !strings.Contains(value, required) {
			t.Fatalf("ChangePlan DTO missing %q: %s", required, value)
		}
	}
}

func TestVerifiedRestartErrorsUseStableHTTPMapping(t *testing.T) {
	t.Parallel()
	tests := []struct {
		err    error
		status int
		code   ErrorCode
	}{
		{orchestrator.ErrChangePlanStale, http.StatusConflict, ErrorChangePlanStale},
		{orchestrator.ErrChangePlanBlocked, http.StatusConflict, ErrorChangePlanBlocked},
		{orchestrator.ErrChangePlanInvalidState, http.StatusConflict, ErrorChangePlanInvalidState},
		{orchestrator.ErrVerificationHealthIncomplete, http.StatusUnprocessableEntity, ErrorVerificationHealthIncomplete},
		{orchestrator.ErrVerificationUnavailable, http.StatusUnprocessableEntity, ErrorVerificationUnavailable},
		{orchestrator.ErrVerificationFailed, http.StatusConflict, ErrorVerificationFailed},
	}
	for _, test := range tests {
		response := performRequest(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			writePhase3Error(writer, request, test.err)
		}), http.MethodPost, "/verified-restart")
		assertErrorCode(t, response, test.status, test.code)
	}
}

func newMetricWorkspaceManager(t *testing.T) (*workspace.Manager, domain.WorkspaceID) {
	t.Helper()
	database, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "stackpilot.db"))
	if err != nil {
		t.Fatalf("open metric API database: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	repository, _ := storage.NewWorkspaceRepository(database)
	loader, _ := manifest.NewLoader()
	manager, err := workspace.NewManager(repository, loader, manifest.NewValidator())
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	record, err := manager.Register(context.Background(), createAPIWorkspaceFixture(t, validAPIManifest()))
	if err != nil {
		t.Fatalf("register metric workspace: %v", err)
	}
	return manager, record.ID
}

func containsUnsafeMetricIdentity(value string) bool {
	for _, token := range []string{"containerId", "processId", "workingDirectory", "commandDigest"} {
		if len(value) >= len(token) {
			for index := 0; index+len(token) <= len(value); index++ {
				if value[index:index+len(token)] == token {
					return true
				}
			}
		}
	}
	return false
}
