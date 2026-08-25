package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"stackpilot/internal/api"
	webassets "stackpilot/web"
)

const address = "127.0.0.1:32144"

var analysisCount atomic.Int64

func main() {
	assets, err := webassets.Dist()
	if err != nil {
		log.Fatal(err)
	}
	spa, err := api.NewSPAHandler(assets)
	if err != nil {
		log.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/version", func(response http.ResponseWriter, _ *http.Request) {
		writeJSON(response, map[string]any{"version": "0.1.0", "commit": "ui-fixture", "buildTime": "2026-08-22T00:00:00Z", "apiVersion": "v1", "capabilities": []string{"phase2.compose", "phase2.compose-build", "workspace.runner.node"}})
	})
	mux.HandleFunc("/api/v1/", handleAPI)
	mux.Handle("/", spa)
	server := &http.Server{Addr: address, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	log.Printf("Phase 2E UI fixture available at http://%s", address)
	log.Fatal(server.ListenAndServe())
}

func handleAPI(response http.ResponseWriter, request *http.Request) {
	path := request.URL.Path
	switch {
	case path == "/api/v1/auth/session" && request.Method == http.MethodGet:
		writeJSON(response, map[string]any{"csrf": "fixture-csrf", "expiresAt": "2026-08-23T12:00:00Z"})
	case path == "/api/v1/workspaces" && request.Method == http.MethodGet:
		writeJSON(response, map[string]any{"items": []any{workspaceDTO()}})
	case path == "/api/v1/workspaces/"+workspaceID() && request.Method == http.MethodGet:
		writeJSON(response, workspaceDetailDTO())
	case path == "/api/v1/workspaces/probe" && request.Method == http.MethodPost:
		writeJSON(response, map[string]any{"state": "initialization_required", "path": `E:\WFGame`, "candidates": []any{map[string]any{"path": "run.bat", "size": 2048}}})
	case path == "/api/v1/workspace-imports/analyze" && request.Method == http.MethodPost:
		response.WriteHeader(http.StatusCreated)
		writeJSON(response, workspaceDraftDTO("import"))
	case strings.Contains(path, "/workspace-imports/drafts/") && strings.HasSuffix(path, "/corrections") && request.Method == http.MethodPost:
		response.WriteHeader(http.StatusCreated)
		writeJSON(response, workspaceDraftDTO("import-confirmed"))
	case strings.Contains(path, "/workspace-imports/drafts/") && strings.HasSuffix(path, "/apply") && request.Method == http.MethodPost:
		response.WriteHeader(http.StatusAccepted)
		writeJSON(response, map[string]any{"operationId": importOperationID(), "state": "queued"})
	case path == "/api/v1/workspace-imports/operations/"+importOperationID() && request.Method == http.MethodGet:
		writeJSON(response, workspaceImportOperationDTO())
	case path == "/api/v1/workspaces/"+workspaceID()+"/edit-drafts" && request.Method == http.MethodPost:
		response.WriteHeader(http.StatusCreated)
		writeJSON(response, workspaceDraftDTO("edit"))
	case path == "/api/v1/workspaces/"+workspaceID()+"/relink-drafts" && request.Method == http.MethodPost:
		response.WriteHeader(http.StatusCreated)
		writeJSON(response, workspaceDraftDTO("relink"))
	case path == "/api/v1/systems" && request.Method == http.MethodGet:
		writeJSON(response, map[string]any{"items": []any{}})
	case path == "/api/v1/incidents" && request.Method == http.MethodGet:
		writeJSON(response, map[string]any{"items": []any{incidentDTO()}})
	case path == "/api/v1/incidents/inc_01ARZ3NDEKTSV4RRFFQ69G5FAV" && request.Method == http.MethodGet:
		writeJSON(response, incidentDetailDTO())
	case path == "/api/v1/incidents/inc_01ARZ3NDEKTSV4RRFFQ69G5FAV/analyze" && request.Method == http.MethodPost:
		analysisCount.Add(1)
		response.WriteHeader(http.StatusAccepted)
		writeJSON(response, map[string]any{"operationId": "op_01ARZ3NDEKTSV4RRFFQ69G5FAV", "state": "queued"})
	case path == "/api/v1/operations/op_01ARZ3NDEKTSV4RRFFQ69G5FAV" && request.Method == http.MethodGet:
		writeJSON(response, map[string]any{"id": "op_01ARZ3NDEKTSV4RRFFQ69G5FAV", "workspaceId": workspaceID(), "systemId": "btc", "type": "analyze", "state": "succeeded", "cancellable": false, "createdAt": "2026-08-18T08:00:00Z", "steps": []any{}})
	case path == "/api/v1/events":
		response.WriteHeader(http.StatusNoContent)
	default:
		writeError(response, http.StatusNotFound)
	}
}

func workspaceDTO() map[string]any {
	return map[string]any{
		"id": workspaceID(), "systemId": "btc", "systemName": "BidTravel Cloud", "path": `E:\BidTravelCloud`,
		"manifestStatus": "valid", "manifestDigest": strings.Repeat("a", 64),
		"serviceCount": 2, "createdAt": "2026-08-18T07:00:00Z", "updatedAt": "2026-08-18T07:00:00Z",
	}
}

func workspaceDetailDTO() map[string]any {
	return map[string]any{"workspace": workspaceDTO(), "source": map[string]any{"type": "bat-import", "entryScript": "run.bat", "sourceDigest": strings.Repeat("b", 64), "analyzedAt": "2026-08-18T07:00:00Z"},
		"manifest": map[string]any{"digest": strings.Repeat("a", 64), "apiVersion": "stackpilot.io/v1alpha1", "description": "BidTravel local stack", "yaml": manifestPreview(), "createdAt": "2026-08-18T07:00:00Z"},
		"services": []any{map[string]any{"id": "backend", "displayName": "Backend", "driver": "process", "mode": "daemon", "runner": "maven", "workingDirectory": "backend", "required": true, "dependsOn": map[string]string{}, "readiness": "http"}, map[string]any{"id": "web", "displayName": "Web", "driver": "process", "mode": "daemon", "runner": "npm", "workingDirectory": "web", "required": true, "dependsOn": map[string]string{"backend": "ready"}, "readiness": "http"}},
		"ports":    []any{map[string]any{"name": "backend", "protocol": "tcp", "preferred": 8080, "conflictPolicy": "auto", "exposure": "loopback"}, map[string]any{"name": "web", "protocol": "tcp", "preferred": 5173, "conflictPolicy": "auto", "exposure": "loopback"}},
		"runtime":  map[string]any{"state": "stopped", "activeOperationId": nil}}
}

func workspaceDraftDTO(kind string) map[string]any {
	id, name, description := "serve-existing", "Serve existing build", "Run the discovered Node service."
	services := []any{map[string]any{"id": "web", "displayName": "Web", "driver": "process", "runner": "node", "mode": "daemon", "workingDirectory": ".", "readinessType": "http", "readinessTarget": "http://127.0.0.1:${ports.web}/", "confidence": "confirmed"}}
	ports := []any{map[string]any{"name": "web", "preferred": 7460, "exposure": "loopback", "confidence": "confirmed"}}
	findings := []any{}
	applyable := true
	requiredCapabilities := []string{"workspace.runner.node"}
	manifest := manifestPreview()
	if kind == "import" || kind == "import-confirmed" {
		id, name, description = "run-compose-project", "Run Compose project", "Build and run the discovered Compose services through the controlled Compose driver."
		services = []any{map[string]any{
			"id": "compose", "displayName": "Compose services", "driver": "compose", "runner": "", "mode": "daemon", "workingDirectory": "", "readinessType": "compose", "confidence": "confirmed",
			"compose": map[string]any{"file": "compose.yaml", "services": []string{"frontend", "gateway", "job", "mysql", "web"}, "buildPolicy": "always", "buildServices": []string{"frontend", "job", "web"},
				"readiness": map[string]string{"frontend": "healthy", "gateway": "running", "job": "running", "mysql": "healthy", "web": "healthy"}, "ports": map[string]any{"gateway": map[string]any{"service": "gateway", "target": 8443}}},
		}}
		ports = []any{map[string]any{"name": "gateway", "preferred": 8443, "exposure": "all_interfaces", "confidence": "confirmed"}}
		requiredCapabilities = []string{"phase2.compose", "phase2.compose-build"}
		manifest = composeManifestPreview()
		if kind == "import" {
			applyable = false
			findings = []any{
				map[string]any{"code": "WORKSPACE_IMPORT_BUILD_UNCONFIRMED", "severity": "blocking", "message": "Local Dockerfile execution requires confirmation.", "confidence": "confirmed", "evidence": []any{map[string]any{"path": "compose.yaml"}}},
				map[string]any{"code": "WORKSPACE_IMPORT_READINESS_UNCONFIRMED", "severity": "blocking", "message": "Running readiness requires confirmation.", "field": "job", "confidence": "confirmed", "evidence": []any{map[string]any{"path": "compose.yaml"}}},
				map[string]any{"code": "WORKSPACE_IMPORT_READINESS_UNCONFIRMED", "severity": "blocking", "message": "Running readiness requires confirmation.", "field": "gateway", "confidence": "confirmed", "evidence": []any{map[string]any{"path": "compose.yaml"}}},
			}
		}
	}
	if kind == "edit" {
		id, name, description = "edit", "Structured edit", "Preview current structured changes."
	}
	if kind == "relink" {
		id, name, description = "relink", "Relink workspace", "Use the validated manifest at the new root."
	}
	return map[string]any{"id": "draft_0123456789abcdef0123456789abcdef", "state": "active", "path": `E:\GNMarket-Fixture`, "expiresAt": "2026-08-23T00:00:00Z",
		"draft": map[string]any{"systemId": "gnmarket-fixture", "systemName": "GNMarket Fixture", "description": "Fictional Compose import fixture", "sourceScript": "start.bat", "sourceDigest": strings.Repeat("c", 64), "analyzedAt": "2026-08-22T00:00:00Z",
			"candidates": []any{map[string]any{"id": id, "name": name, "description": description, "applyable": applyable, "requiredCapabilities": requiredCapabilities, "services": services, "ports": ports, "findings": findings, "manifestYaml": manifest, "manifestDigest": strings.Repeat("d", 64)}}}}
}

func workspaceImportOperationDTO() map[string]any {
	steps := []any{}
	for index, key := range []string{"verify-source", "validate-draft", "stage-manifest", "publish-manifest", "register-workspace", "record-source"} {
		steps = append(steps, map[string]any{"number": index + 1, "key": key, "state": "succeeded"})
	}
	return map[string]any{"id": importOperationID(), "workspaceId": workspaceID(), "type": "workspace-import-apply", "state": "succeeded", "createdAt": "2026-08-22T00:00:00Z", "steps": steps}
}

func manifestPreview() string {
	return "apiVersion: stackpilot.io/v1alpha1\nkind: System\nmetadata:\n  id: wfgame\n  name: WFGame\nspec:\n  services:\n    web:\n      driver: process\n      runner: node\n"
}

func composeManifestPreview() string {
	return "apiVersion: stackpilot.io/v1alpha1\nkind: System\nmetadata:\n  id: gnmarket-fixture\n  name: GNMarket Fixture\nspec:\n  ports:\n    gateway: {protocol: tcp, preferred: 8443, exposure: loopback}\n  services:\n    compose:\n      driver: compose\n      compose:\n        file: compose.yaml\n        services: [frontend, gateway, job, mysql, web]\n        buildPolicy: always\n        readiness: {frontend: healthy, gateway: running, job: running, mysql: healthy, web: healthy}\n      readiness: {type: compose}\n"
}

func importOperationID() string { return "op_01ARZ3NDEKTSV4RRFFQ69G5FAA" }

func incidentDTO() map[string]any {
	return map[string]any{
		"id": "inc_01ARZ3NDEKTSV4RRFFQ69G5FAV", "workspaceId": workspaceID(),
		"systemInstanceId": "si_01ARZ3NDEKTSV4RRFFQ69G5FAV", "serviceInstanceId": "svi_01ARZ3NDEKTSV4RRFFQ69G5FAV",
		"serviceId": "backend", "kind": "readiness-timeout", "severity": "critical", "state": "open", "occurrenceCount": 2,
		"context": incidentContext(), "firstSeenAt": "2026-08-18T08:00:00Z", "lastSeenAt": "2026-08-18T08:01:00Z",
	}
}

func incidentDetailDTO() map[string]any {
	analyses := []any{analysisDTO(1)}
	if analysisCount.Load() > 0 {
		analyses = append(analyses, analysisDTO(2))
	}
	return map[string]any{"incident": incidentDTO(), "analyses": analyses}
}

func incidentContext() map[string]any {
	return map[string]any{
		"schemaVersion": "1", "workspaceId": workspaceID(), "systemInstanceId": "si_01ARZ3NDEKTSV4RRFFQ69G5FAV",
		"serviceInstanceId": "svi_01ARZ3NDEKTSV4RRFFQ69G5FAV", "serviceId": "backend", "kind": "readiness-timeout",
		"triggerCode": "HEALTH_READINESS_TIMEOUT", "windowStart": "2026-08-18T07:58:00Z", "windowEnd": "2026-08-18T08:02:00Z",
		"dependencies": map[string]string{}, "ports": map[string]int{"backend": 8081},
		"evidence": []any{map[string]any{"type": "health", "healthResultId": 42, "serviceInstanceId": "svi_01ARZ3NDEKTSV4RRFFQ69G5FAV"}, map[string]any{"type": "log", "logSequence": 7, "serviceInstanceId": "svi_01ARZ3NDEKTSV4RRFFQ69G5FAV"}},
		"logs":     []any{map[string]any{"serviceInstanceId": "svi_01ARZ3NDEKTSV4RRFFQ69G5FAV", "sequence": 7, "timestamp": "2026-08-18T08:00:00Z", "stream": "stderr", "message": "Connection refused"}},
	}
}

func analysisDTO(id int) map[string]any {
	return map[string]any{
		"id": id, "engine": "rules", "schemaVersion": "1", "createdAt": "2026-08-18T08:01:00Z",
		"result": map[string]any{"results": []any{map[string]any{
			"ruleId": "readiness-timeout", "title": "Readiness timed out", "cause": "The service did not satisfy its readiness threshold before the configured deadline.", "confidence": 100,
			"evidence":    []any{map[string]any{"type": "health", "healthResultId": 42}},
			"suggestions": []any{map[string]any{"action": "inspect-readiness-evidence", "description": "Readiness timed out", "automatic": false}},
		}}},
	}
}

func workspaceID() string { return "ws_01ARZ3NDEKTSV4RRFFQ69G5FAV" }

func writeJSON(response http.ResponseWriter, value any) {
	response.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(response).Encode(value); err != nil {
		log.Printf("encode fixture response: %v", err)
	}
}

func writeError(response http.ResponseWriter, status int) {
	response.WriteHeader(status)
	writeJSON(response, map[string]any{"error": map[string]any{"code": "RESOURCE_NOT_FOUND", "message": "not found", "details": map[string]any{}, "traceId": "fixture"}})
}
