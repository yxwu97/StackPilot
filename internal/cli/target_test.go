package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveWorkspaceMatchesManifestAncestor(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".stackpilot"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".stackpilot", "system.yaml"), []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(root, "service", "nested")
	if err := os.MkdirAll(child, 0o700); err != nil {
		t.Fatal(err)
	}
	previous, _ := os.Getwd()
	if err := os.Chdir(child); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"items":[{"id":"ws_test","systemId":"sample","path":` + quotedJSON(root) + `}]}`))
	}))
	defer server.Close()
	client, _ := newAPIClient(server.URL, []byte("token"))
	defer client.Close()
	workspace, err := resolveWorkspace(context.Background(), client, "")
	if err != nil || workspace.ID != "ws_test" || workspace.SystemID != "sample" {
		t.Fatalf("resolveWorkspace() = (%+v, %v)", workspace, err)
	}
}

func quotedJSON(value string) string {
	result, _ := json.Marshal(value)
	return string(result)
}
