package manifest

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestRealPMSManifest(t *testing.T) {
	root := os.Getenv("STACKPILOT_PMS_WORKSPACE")
	if root == "" {
		t.Skip("set STACKPILOT_PMS_WORKSPACE for the real PMS manifest Gate")
	}
	loader, err := NewLoader()
	if err != nil {
		t.Fatal(err)
	}
	document, err := loader.Load(context.Background(), filepath.Join(root, ".stackpilot", "system.yaml"))
	if err != nil {
		t.Fatalf("load real PMS manifest: %v", err)
	}
	validated, err := NewValidator().Validate(context.Background(), document, root)
	if err != nil {
		t.Fatalf("validate real PMS manifest: %v", err)
	}
	assertPMSManifest(t, validated.Manifest)
}

func assertPMSManifest(t *testing.T, value Manifest) {
	t.Helper()
	wantServices := []string{"backend", "rag", "web"}
	if value.Metadata.ID != "pms" || !reflect.DeepEqual(sortedServiceIDs(value.Spec.Services), wantServices) {
		t.Fatalf("PMS identity/services = %q %#v", value.Metadata.ID, sortedServiceIDs(value.Spec.Services))
	}
	if len(value.Spec.Services["backend"].DependsOn) != 0 || len(value.Spec.Services["rag"].DependsOn) != 0 {
		t.Fatal("PMS Backend and RAG must remain independent startup roots")
	}
	webDependencies := value.Spec.Services["web"].DependsOn
	if webDependencies["backend"] != "ready" || webDependencies["rag"] != "ready" {
		t.Fatalf("PMS Web dependencies = %#v", webDependencies)
	}
	backendEnvironment := value.Spec.Services["backend"].Environment
	ragEnvironment := value.Spec.Services["rag"].Environment
	if backendEnvironment["SERVER_PORT"] != "${ports.backend}" ||
		backendEnvironment["PM_KNOWLEDGE_RAG_BASE_URL"] != "http://127.0.0.1:${ports.rag}" ||
		backendEnvironment["PM_KNOWLEDGE_RAG_TOKEN"] != "${secret.pms-rag-service-token}" ||
		ragEnvironment["RAG_PORT"] != "${ports.rag}" ||
		ragEnvironment["RAG_SERVICE_TOKEN"] != "${secret.pms-rag-service-token}" {
		t.Fatal("PMS dynamic port or Secret propagation is incomplete")
	}
	if value.Spec.Ports["web"].Preferred == nil || *value.Spec.Ports["web"].Preferred != 32102 {
		t.Fatalf("PMS Web preferred port = %#v", value.Spec.Ports["web"].Preferred)
	}
}
