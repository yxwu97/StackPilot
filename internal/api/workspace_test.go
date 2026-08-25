package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"stackpilot/internal/domain"
	"stackpilot/internal/manifest"
	"stackpilot/internal/security"
	"stackpilot/internal/storage"
	"stackpilot/internal/workspace"
)

func TestWorkspaceAPIRegistrationAndReadOnlyQueries(t *testing.T) {
	handler := newWorkspaceAPIHandler(t)
	root := createAPIWorkspaceFixture(t, validAPIManifest())
	registered := postWorkspace(t, handler, root)
	if registered.SystemID != "sample" || registered.ServiceCount != 1 || registered.ManifestStatus != workspace.ManifestValid {
		t.Fatalf("registered workspace = %#v", registered)
	}

	workspaces := performRequest(handler, http.MethodGet, "/api/v1/workspaces")
	if workspaces.Code != http.StatusOK || !strings.Contains(workspaces.Body.String(), registered.ID) {
		t.Fatalf("GET workspaces = (%d, %q)", workspaces.Code, workspaces.Body.String())
	}
	systems := performRequest(handler, http.MethodGet, "/api/v1/systems")
	if systems.Code != http.StatusOK || !strings.Contains(systems.Body.String(), `"state":"stopped"`) {
		t.Fatalf("GET systems = (%d, %q)", systems.Code, systems.Body.String())
	}
	detail := performRequest(handler, http.MethodGet, "/api/v1/systems/sample?workspaceId="+registered.ID)
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), registered.ManifestDigest) {
		t.Fatalf("GET system = (%d, %q)", detail.Code, detail.Body.String())
	}
	service := performRequest(handler, http.MethodGet, "/api/v1/services/sample/backend?workspaceId="+registered.ID)
	if service.Code != http.StatusOK || !strings.Contains(service.Body.String(), `"driver":"process"`) {
		t.Fatalf("GET service = (%d, %q)", service.Code, service.Body.String())
	}
	for _, forbidden := range []string{"arguments", "environment", "workingDirectory", "sensitive-value"} {
		if strings.Contains(service.Body.String(), forbidden) {
			t.Fatalf("service DTO leaked %q: %s", forbidden, service.Body.String())
		}
	}
}

func TestSystemSummaryUsesServerRuntimeState(t *testing.T) {
	record := workspace.Record{
		ID: domain.WorkspaceID("ws_01ARZ3NDEKTSV4RRFFQ69G5FAV"), SystemID: "btc", SystemName: "BTC",
		RootPath: `E:\BTC`, ManifestStatus: workspace.ManifestValid, LastValidDigest: strings.Repeat("a", 64),
		ServiceCount: 2, UpdatedAt: time.Date(2026, 8, 18, 1, 0, 0, 0, time.UTC),
	}
	operationID := "op_01ARZ3NDEKTSV4RRFFQ69G5FAV"
	summary := mapSystem(record, systemRuntimeSummary{state: domain.SystemRunning, ready: 2, activeOperationID: &operationID})
	if summary.State != "running" || summary.ServiceSummary.Ready != 2 || summary.ActiveOperationID == nil || *summary.ActiveOperationID != operationID {
		t.Fatalf("runtime system summary = %+v", summary)
	}
}

func TestServiceSummaryCountsReadyAndCompletedStates(t *testing.T) {
	tests := map[domain.ServiceState]bool{
		domain.ServiceReady: true, domain.ServiceCompleted: true,
		domain.ServiceDegraded: false, domain.ServiceFailed: false,
	}
	for state, want := range tests {
		if got := serviceCountsAsReady(state); got != want {
			t.Fatalf("serviceCountsAsReady(%q) = %t, want %t", state, got, want)
		}
	}
}

func TestWorkspaceAPIRejectsInvalidRequestsWithStableErrors(t *testing.T) {
	handler := newWorkspaceAPIHandler(t)
	root := createAPIWorkspaceFixture(t, validAPIManifest())
	tests := []struct {
		name        string
		contentType string
		body        string
	}{
		{name: "missing content type", body: `{"path":"` + root + `"}`},
		{name: "unknown field", contentType: "application/json", body: `{"path":"` + root + `","command":"unsafe"}`},
		{name: "multiple values", contentType: "application/json", body: `{"path":"` + root + `"}{}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces", strings.NewReader(test.body))
			request.Header.Set("Content-Type", test.contentType)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			assertErrorCode(t, response, http.StatusBadRequest, ErrorRequestValidationFailed)
		})
	}
}

func TestSystemMutationRejectsUnregisteredWorkspace(t *testing.T) {
	handler := newWorkspaceAPIHandler(t)
	response := performJSONRequest(handler, http.MethodPost, "/api/v1/systems/sample/start", map[string]string{
		"workspaceId": "ws_01ARZ3NDEKTSV4RRFFQ69G5FAV",
	})
	assertErrorCode(t, response, http.StatusNotFound, ErrorResourceNotFound)
}

func TestWorkspaceAPIBrowserMutationRequiresExactOriginAndCSRFHeader(t *testing.T) {
	auth := newStubAuthenticator()
	handler := newWorkspaceAPIHandlerWithAuth(t, auth)
	root := createAPIWorkspaceFixture(t, validAPIManifest())
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/v1/workspaces", strings.NewReader(`{"path":"ignored"}`))
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: auth.session})
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://malicious.example")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	assertErrorCode(t, response, http.StatusForbidden, ErrorBrowserRequestRejected)

	accepted := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/v1/workspaces", bytes.NewReader(mustJSON(t, map[string]string{"path": root})))
	accepted.Header.Set("Content-Type", "application/json")
	accepted.Header.Set("Origin", "http://127.0.0.1")
	accepted.Header.Set(browserCSRFHeader, auth.csrf)
	accepted.AddCookie(&http.Cookie{Name: sessionCookieName, Value: auth.session})
	acceptedResponse := httptest.NewRecorder()
	handler.ServeHTTP(acceptedResponse, accepted)
	if acceptedResponse.Code != http.StatusCreated {
		t.Fatalf("same-origin browser registration = (%d, %q)", acceptedResponse.Code, acceptedResponse.Body.String())
	}
}

func TestWorkspaceAPIDuplicateAndManifestErrorsAreSafe(t *testing.T) {
	handler := newWorkspaceAPIHandler(t)
	missing := performJSONRequest(handler, http.MethodPost, "/api/v1/workspaces", map[string]string{"path": filepath.Join(t.TempDir(), "missing")})
	assertErrorCode(t, missing, http.StatusUnprocessableEntity, ErrorWorkspacePathInvalid)

	root := createAPIWorkspaceFixture(t, validAPIManifest())
	postWorkspace(t, handler, root)
	duplicate := performJSONRequest(handler, http.MethodPost, "/api/v1/workspaces", map[string]string{"path": root})
	assertErrorCode(t, duplicate, http.StatusConflict, ErrorWorkspaceAlreadyExists)

	invalidRoot := createAPIWorkspaceFixture(t, strings.Replace(validAPIManifest(),
		"      driver: process", "      driver: process\n      driver: process", 1))
	invalid := performJSONRequest(handler, http.MethodPost, "/api/v1/workspaces", map[string]string{"path": invalidRoot})
	assertErrorCode(t, invalid, http.StatusBadRequest, ErrorManifestDuplicateKey)
	if strings.Contains(invalid.Body.String(), invalidRoot) || strings.Contains(invalid.Body.String(), "sensitive-value") {
		t.Fatalf("manifest error leaked host path or value: %s", invalid.Body.String())
	}
}

func TestWorkspaceAPIUnregisterDoesNotDeleteFiles(t *testing.T) {
	handler := newWorkspaceAPIHandler(t)
	root := createAPIWorkspaceFixture(t, validAPIManifest())
	registered := postWorkspace(t, handler, root)
	response := performRequest(handler, http.MethodDelete, "/api/v1/workspaces/"+registered.ID)
	if response.Code != http.StatusNoContent {
		t.Fatalf("DELETE workspace = (%d, %q)", response.Code, response.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, ".stackpilot", "system.yaml")); err != nil {
		t.Fatalf("workspace file was changed: %v", err)
	}
	missing := performRequest(handler, http.MethodDelete, "/api/v1/workspaces/"+registered.ID)
	assertErrorCode(t, missing, http.StatusNotFound, ErrorWorkspaceNotFound)
}

func TestWorkspaceAPIUnregisterRejectsActiveOperation(t *testing.T) {
	handler, database := newWorkspaceAPIHandlerWithDatabase(t, nil, nil)
	root := createAPIWorkspaceFixture(t, validAPIManifest())
	registered := postWorkspace(t, handler, root)
	_, err := database.Exec(`INSERT INTO operations (
        id, workspace_id, system_id, type, state, idempotency_subject, route_key,
        request_digest, cancellable, created_at
    ) VALUES (?, ?, ?, 'start', 'running', 'test', 'test', ?, 0, ?)`,
		"op_01ARZ3NDEKTSV4RRFFQ69G5FAV", registered.ID, registered.SystemID,
		strings.Repeat("0", 64), "2026-08-23T00:00:00Z")
	if err != nil {
		t.Fatalf("insert active Operation: %v", err)
	}

	response := performRequest(handler, http.MethodDelete, "/api/v1/workspaces/"+registered.ID)
	assertErrorCode(t, response, http.StatusConflict, ErrorWorkspaceUnregisterActive)
}

func TestWorkspaceAPIRefreshesFixedManifest(t *testing.T) {
	handler := newWorkspaceAPIHandler(t)
	root := createAPIWorkspaceFixture(t, validAPIManifest())
	registered := postWorkspace(t, handler, root)
	response := performRequest(handler, http.MethodPost, "/api/v1/workspaces/"+registered.ID+"/refresh")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), registered.ID) {
		t.Fatalf("POST workspace refresh = (%d, %q)", response.Code, response.Body.String())
	}
}

func newWorkspaceAPIHandler(t *testing.T) http.Handler {
	return newWorkspaceAPIHandlerWithAuth(t, nil)
}

func newWorkspaceAPIHandlerWithAuth(t *testing.T, auth Authenticator) http.Handler {
	return newWorkspaceAPIHandlerWithSecurity(t, auth, nil)
}

func newWorkspaceAPIHandlerWithSecurity(t *testing.T, auth Authenticator, audit security.AuditStore) http.Handler {
	handler, _ := newWorkspaceAPIHandlerWithDatabase(t, auth, audit)
	return handler
}

func newWorkspaceAPIHandlerWithDatabase(t *testing.T, auth Authenticator, audit security.AuditStore) (http.Handler, *sql.DB) {
	t.Helper()
	database, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "stackpilot.db"))
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("close test database: %v", err)
		}
	})
	repository, err := storage.NewWorkspaceRepository(database)
	if err != nil {
		t.Fatalf("NewWorkspaceRepository() error = %v", err)
	}
	loader, err := manifest.NewLoader()
	if err != nil {
		t.Fatalf("NewLoader() error = %v", err)
	}
	manager, err := workspace.NewManager(repository, loader, manifest.NewValidator())
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	return newRouter(Config{Workspaces: manager, Auth: auth, Audit: audit}, newTestSPAHandler(t)), database
}

func createAPIWorkspaceFixture(t *testing.T, contents string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".stackpilot"), 0o700); err != nil {
		t.Fatalf("create manifest directory: %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "backend"), 0o700); err != nil {
		t.Fatalf("create service directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".stackpilot", "system.yaml"), []byte(contents), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	return root
}

func validAPIManifest() string {
	return `apiVersion: stackpilot.io/v1alpha1
kind: System
metadata:
  id: sample
  name: Sample
spec:
  services:
    backend:
      driver: process
      runner: java
      workingDirectory: ./backend
      arguments: ["--token=sensitive-value"]
      environment:
        SAMPLE_TOKEN: sensitive-value
      readiness:
        type: process
`
}

func postWorkspace(t *testing.T, handler http.Handler, path string) workspaceDTO {
	t.Helper()
	response := performJSONRequest(handler, http.MethodPost, "/api/v1/workspaces", map[string]string{"path": path})
	if response.Code != http.StatusCreated {
		t.Fatalf("POST workspace = (%d, %q)", response.Code, response.Body.String())
	}
	var result workspaceDTO
	decodeResponse(t, response, &result)
	return result
}

func performJSONRequest(handler http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	encoded, _ := json.Marshal(body)
	request := httptest.NewRequest(method, path, bytes.NewReader(encoded))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode test JSON: %v", err)
	}
	return encoded
}

func assertErrorCode(t *testing.T, response *httptest.ResponseRecorder, status int, code ErrorCode) {
	t.Helper()
	var body errorEnvelope
	decodeResponse(t, response, &body)
	if response.Code != status || body.Error.Code != code {
		t.Fatalf("error response = (%d, %s, %q), want (%d, %s)", response.Code, body.Error.Code, response.Body.String(), status, code)
	}
}
