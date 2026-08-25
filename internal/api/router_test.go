package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"stackpilot/internal/buildinfo"
	"stackpilot/internal/orchestrator"
	"stackpilot/internal/workspace"
)

var traceIDPattern = regexp.MustCompile(`^tr_[0-9a-f]{32}$`)

func TestAvailabilityAndVersionRoutes(t *testing.T) {
	config := Config{
		BuildInfo: buildinfo.Info{
			Version:   "1.2.3",
			Commit:    "abc123",
			BuildTime: "2026-08-17T12:00:00Z",
		},
		Capabilities: []string{"process", "", "web", "process"},
		Readiness:    func(context.Context) bool { return true },
	}
	handler := newRouter(config, newTestSPAHandler(t))

	tests := []struct {
		path   string
		status int
		body   string
	}{
		{path: "/health/live", status: http.StatusOK, body: `{"status":"live"}`},
		{path: "/health/ready", status: http.StatusOK, body: `{"status":"ready"}`},
		{path: "/version", status: http.StatusOK, body: `"apiVersion":"v1"`},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			response := performRequest(handler, http.MethodGet, test.path)
			if response.Code != test.status || !strings.Contains(response.Body.String(), test.body) {
				t.Fatalf("response = (%d, %q), want (%d, body containing %q)", response.Code, response.Body.String(), test.status, test.body)
			}
			assertJSONHeaders(t, response)
		})
	}

	version := performRequest(handler, http.MethodGet, "/version")
	var body versionResponse
	decodeResponse(t, version, &body)
	if body.Version != "1.2.3" || body.Commit != "abc123" || body.BuildTime != "2026-08-17T12:00:00Z" || body.APIVersion != "v1" {
		t.Fatalf("version response = %#v, want injected build identity", body)
	}
	if body.Capabilities == nil || strings.Join(body.Capabilities, ",") != "process,web" {
		t.Fatalf("capabilities = %#v, want sorted unique values", body.Capabilities)
	}
}

func TestReadinessIsUnavailableUntilProbeSucceeds(t *testing.T) {
	handler := newRouter(Config{}, newTestSPAHandler(t))
	response := performRequest(handler, http.MethodGet, "/health/ready")
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", response.Code)
	}
	if response.Header().Get("Retry-After") != "1" {
		t.Fatalf("Retry-After = %q, want 1", response.Header().Get("Retry-After"))
	}

	var body errorEnvelope
	decodeResponse(t, response, &body)
	if body.Error.Code != ErrorHealthNotReady || body.Error.Message != errorRegistry[ErrorHealthNotReady].Message {
		t.Fatalf("error = %#v, want registered readiness error", body.Error)
	}
	if !traceIDPattern.MatchString(body.Error.TraceID) || body.Error.TraceID != response.Header().Get("X-Trace-ID") {
		t.Fatalf("trace IDs = (%q, %q), want matching generated IDs", body.Error.TraceID, response.Header().Get("X-Trace-ID"))
	}
}

func TestAPIErrorsDoNotFallBackToSPA(t *testing.T) {
	handler := newRouter(Config{}, newTestSPAHandler(t))
	tests := []struct {
		method string
		path   string
		status int
		code   ErrorCode
	}{
		{method: http.MethodGet, path: "/api/v1/missing", status: http.StatusNotFound, code: ErrorResourceNotFound},
		{method: http.MethodPost, path: "/version", status: http.StatusMethodNotAllowed, code: ErrorMethodNotAllowed},
	}
	for _, test := range tests {
		response := performRequest(handler, test.method, test.path)
		var body errorEnvelope
		decodeResponse(t, response, &body)
		if response.Code != test.status || body.Error.Code != test.code {
			t.Fatalf("%s %s = (%d, %s), want (%d, %s)", test.method, test.path, response.Code, body.Error.Code, test.status, test.code)
		}
		if strings.Contains(response.Body.String(), "StackPilot") {
			t.Fatal("API error unexpectedly returned the SPA index")
		}
	}

	spa := performRequest(handler, http.MethodGet, "/systems/btc")
	if spa.Code != http.StatusOK || !strings.Contains(spa.Body.String(), "StackPilot") {
		t.Fatalf("SPA fallback = (%d, %q), want embedded index", spa.Code, spa.Body.String())
	}
}

func TestRecoveryReturnsSafeInternalError(t *testing.T) {
	panicHandler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("sensitive failure text")
	})
	handler := traceMiddleware(recoveryMiddleware(nil)(panicHandler))
	response := performRequest(handler, http.MethodGet, "/panic")

	var body errorEnvelope
	decodeResponse(t, response, &body)
	if response.Code != http.StatusInternalServerError || body.Error.Code != ErrorInternal {
		t.Fatalf("response = (%d, %s), want safe internal error", response.Code, body.Error.Code)
	}
	if strings.Contains(response.Body.String(), "sensitive") {
		t.Fatal("internal error response leaked panic content")
	}
}

func TestOperationRestartAndListRoutesValidateAtBoundary(t *testing.T) {
	handler := newRouter(Config{
		Workspaces:    &workspace.Manager{},
		SingleService: &orchestrator.SingleService{},
	}, newTestSPAHandler(t))
	for _, path := range []string{
		"/api/v1/systems/btc/restart",
		"/api/v1/services/btc/backend/restart",
	} {
		response := performJSONRequest(handler, http.MethodPost, path, json.RawMessage(`{"broken"`))
		assertErrorCode(t, response, http.StatusBadRequest, ErrorRequestValidationFailed)
	}
	response := performRequest(handler, http.MethodGet, "/api/v1/operations?workspaceId=invalid")
	assertErrorCode(t, response, http.StatusBadRequest, ErrorRequestValidationFailed)
}

func TestMutationRoutesRejectCommandShapingFields(t *testing.T) {
	handler := newRouter(Config{
		Workspaces:    &workspace.Manager{},
		SingleService: &orchestrator.SingleService{},
	}, newTestSPAHandler(t))
	tests := []json.RawMessage{
		json.RawMessage(`{"workspaceId":"ws_01ARZ3NDEKTSV4RRFFQ69G5FAV","command":"unsafe"}`),
		json.RawMessage(`{"workspaceId":"ws_01ARZ3NDEKTSV4RRFFQ69G5FAV","arguments":["unsafe"]}`),
		json.RawMessage(`{"workspaceId":"ws_01ARZ3NDEKTSV4RRFFQ69G5FAV","workingDirectory":"C:\\\\"}`),
		json.RawMessage(`{"workspaceId":"ws_01ARZ3NDEKTSV4RRFFQ69G5FAV","environment":{"TOKEN":"unsafe"}}`),
	}
	for _, body := range tests {
		response := performJSONRequest(handler, http.MethodPost, "/api/v1/systems/btc/start", body)
		assertErrorCode(t, response, http.StatusBadRequest, ErrorRequestValidationFailed)
	}
}

func performRequest(handler http.Handler, method, path string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(method, path, nil))
	return response
}

func decodeResponse(t *testing.T, response *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.Unmarshal(response.Body.Bytes(), target); err != nil {
		t.Fatalf("decode response %q: %v", response.Body.String(), err)
	}
}

func assertJSONHeaders(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	if response.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q", response.Header().Get("Content-Type"))
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q", response.Header().Get("Cache-Control"))
	}
	if !traceIDPattern.MatchString(response.Header().Get("X-Trace-ID")) {
		t.Fatalf("X-Trace-ID = %q", response.Header().Get("X-Trace-ID"))
	}
}
