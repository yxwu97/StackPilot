package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"stackpilot/internal/importer"
	"stackpilot/internal/manifest"
	"stackpilot/internal/storage"
	"stackpilot/internal/workspace"
)

func TestWorkspaceImportAPIProbeAnalyzeCorrectAndApply(t *testing.T) {
	handler := newWorkspaceImportAPIHandler(t, nil)
	root := createWorkspaceImportAPIFixture(t)

	probe := performJSONRequest(handler, http.MethodPost, "/api/v1/workspaces/probe", map[string]string{"path": root})
	if probe.Code != http.StatusOK || !strings.Contains(probe.Body.String(), `"state":"initialization_required"`) {
		t.Fatalf("probe = (%d, %s)", probe.Code, probe.Body.String())
	}
	analyze := performJSONRequest(handler, http.MethodPost, "/api/v1/workspace-imports/analyze", map[string]string{"path": root, "script": "run.bat"})
	if analyze.Code != http.StatusCreated {
		t.Fatalf("analyze = (%d, %s)", analyze.Code, analyze.Body.String())
	}
	if strings.Contains(analyze.Body.String(), `"manifest":`) || strings.Contains(analyze.Body.String(), `"environment":`) {
		t.Fatalf("draft leaked internal manifest or environment: %s", analyze.Body.String())
	}
	var draft workspaceDraftDTO
	decodeResponse(t, analyze, &draft)
	correction := performJSONRequest(handler, http.MethodPost, "/api/v1/workspace-imports/drafts/"+draft.ID+"/corrections", workspaceCorrectionRequest{
		CandidateID: "serve-existing", SystemName: "Corrected", Description: "corrected",
		ServiceDisplayNames: map[string]string{"web": "Corrected Web"}, PortPreferred: map[string]int{"web": 7461},
	})
	if correction.Code != http.StatusCreated || !strings.Contains(correction.Body.String(), "Corrected Web") {
		t.Fatalf("correction = (%d, %s)", correction.Code, correction.Body.String())
	}
	var corrected workspaceDraftDTO
	decodeResponse(t, correction, &corrected)
	missingKey := performJSONRequest(handler, http.MethodPost, "/api/v1/workspace-imports/drafts/"+corrected.ID+"/apply", workspaceApplyRequest{CandidateID: "serve-existing"})
	assertErrorCode(t, missingKey, http.StatusBadRequest, ErrorRequestValidationFailed)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/workspace-imports/drafts/"+corrected.ID+"/apply", strings.NewReader(`{"candidateId":"serve-existing"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "api-import")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("apply = (%d, %s)", response.Code, response.Body.String())
	}
}

func TestWorkspaceImportAPIBrowserProbeRequiresOriginAndCSRF(t *testing.T) {
	auth := newStubAuthenticator()
	handler := newWorkspaceImportAPIHandler(t, auth)
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/v1/workspaces/probe", strings.NewReader(`{"path":"ignored"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://malicious.example")
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: auth.session})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	assertErrorCode(t, response, http.StatusForbidden, ErrorBrowserRequestRejected)
}

func TestWorkspaceImportAPIRequiresPerServiceComposeConfirmations(t *testing.T) {
	handler := newWorkspaceImportAPIHandler(t, nil)
	root := createWorkspaceImportAPIComposeFixture(t)
	analyze := performJSONRequest(handler, http.MethodPost, "/api/v1/workspace-imports/analyze", map[string]string{"path": root, "script": "start.bat"})
	if analyze.Code != http.StatusCreated {
		t.Fatalf("analyze Compose = (%d, %s)", analyze.Code, analyze.Body.String())
	}
	var draft workspaceDraftDTO
	decodeResponse(t, analyze, &draft)
	if len(draft.Draft.Candidates) != 1 || len(draft.Draft.Candidates[0].Services) != 1 {
		t.Fatalf("Compose candidates = %#v", draft.Draft.Candidates)
	}
	candidate := draft.Draft.Candidates[0]
	compose := candidate.Services[0].Compose
	if compose == nil || compose.BuildPolicy != "always" {
		t.Fatalf("Compose DTO = %#v", compose)
	}
	if !containsString(candidate.RequiredCapabilities, "phase2.compose-build") || len(compose.BuildServices) != 3 || compose.Readiness["job"] != "running" || compose.Readiness["gateway"] != "running" {
		t.Fatalf("Compose confirmation facts = %#v, capabilities=%#v", compose, candidate.RequiredCapabilities)
	}
	base := workspaceCorrectionRequest{CandidateID: candidate.ID, SystemName: draft.Draft.SystemName,
		ServiceDisplayNames: map[string]string{"compose": "Compose services"}, PortPreferred: map[string]int{"gateway": 8443}}
	missing := performJSONRequest(handler, http.MethodPost, "/api/v1/workspace-imports/drafts/"+draft.ID+"/corrections", base)
	assertErrorCode(t, missing, http.StatusUnprocessableEntity, ErrorWorkspaceImportDependency)
	base.ComposeBuild = true
	base.ComposeRunning = map[string]bool{"job": true}
	missing = performJSONRequest(handler, http.MethodPost, "/api/v1/workspace-imports/drafts/"+draft.ID+"/corrections", base)
	assertErrorCode(t, missing, http.StatusUnprocessableEntity, ErrorWorkspaceImportDependency)
	base.ComposeRunning["gateway"] = true
	corrected := performJSONRequest(handler, http.MethodPost, "/api/v1/workspace-imports/drafts/"+draft.ID+"/corrections", base)
	if corrected.Code != http.StatusCreated {
		t.Fatalf("confirmed Compose correction = (%d, %s)", corrected.Code, corrected.Body.String())
	}
	var result workspaceDraftDTO
	decodeResponse(t, corrected, &result)
	if !result.Draft.Candidates[0].Applyable || strings.Contains(result.Draft.Candidates[0].ManifestYAML, "buildServices") {
		t.Fatalf("confirmed Compose candidate = %#v", result.Draft.Candidates[0])
	}
}

func newWorkspaceImportAPIHandler(t *testing.T, auth Authenticator) http.Handler {
	t.Helper()
	database, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "stackpilot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	workspaceRepository, _ := storage.NewWorkspaceRepository(database)
	loader, _ := manifest.NewLoader()
	manager, err := workspace.NewManager(workspaceRepository, loader, manifest.NewValidatorWithCapabilities("compose", "compose-build", "liveness", "auto-restart"))
	if err != nil {
		t.Fatal(err)
	}
	importRepository, _ := storage.NewWorkspaceImportRepository(database)
	analyzer, _ := importer.NewAnalyzer()
	ctx, cancel := context.WithCancel(context.Background())
	service, err := workspace.NewImportService(workspace.ImportServiceConfig{Context: ctx, Analyzer: analyzer, Repository: importRepository, Workspaces: manager})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); service.Wait() })
	return newRouter(Config{Workspaces: manager, WorkspaceImports: service, Auth: auth}, newTestSPAHandler(t))
}

func createWorkspaceImportAPIFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeAPIImportFile(t, filepath.Join(root, "package.json"), `{"name":"api-import"}`)
	writeAPIImportFile(t, filepath.Join(root, "run.bat"), "@echo off\r\nnode tools\\serve.js build\r\n")
	writeAPIImportFile(t, filepath.Join(root, "tools", "serve.js"), "const PORT = Number('') || 7460;\nconst port = PORT;\nserver.listen(port, '127.0.0.1');\n")
	if err := os.MkdirAll(filepath.Join(root, "build"), 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}

func createWorkspaceImportAPIComposeFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeAPIImportFile(t, filepath.Join(root, "start.bat"), "@echo off\r\npowershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts\\dev-up.ps1\r\n")
	writeAPIImportFile(t, filepath.Join(root, "scripts", "dev-up.ps1"), "$ErrorActionPreference = 'Stop'\ndocker compose -f compose.yaml up --build -d\ndocker compose -f compose.yaml ps\n")
	writeAPIImportFile(t, filepath.Join(root, "compose.yaml"), `services:
  mysql: {image: mysql:8.4, healthcheck: {test: [CMD, mysqladmin, ping]}}
  web: {build: ./web, healthcheck: {test: [CMD, /health]}}
  job: {build: ./job, depends_on: [web]}
  frontend: {build: ./frontend, healthcheck: {test: [CMD, /health]}}
  gateway: {image: nginx:1.27, depends_on: [frontend], ports: ["8443:8443"]}
`)
	for _, directory := range []string{"web", "job", "frontend"} {
		writeAPIImportFile(t, filepath.Join(root, directory, "Dockerfile"), "FROM scratch\n")
	}
	return root
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func writeAPIImportFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}
