package manifest

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func TestRealAIWSManifest(t *testing.T) {
	root := os.Getenv("STACKPILOT_AIWS_WORKSPACE")
	if root == "" {
		t.Skip("set STACKPILOT_AIWS_WORKSPACE for the real AIWS manifest Gate")
	}
	loader, err := NewLoader()
	if err != nil {
		t.Fatal(err)
	}
	document, err := loader.Load(context.Background(), filepath.Join(root, ".stackpilot", "system.yaml"))
	if err != nil {
		t.Fatalf("load real AIWS manifest: %v", err)
	}
	validated, err := NewValidatorWithCapabilities("compose").Validate(context.Background(), document, root)
	if err != nil {
		t.Fatalf("validate real AIWS manifest: %v", err)
	}
	assertAIWSManifest(t, validated.Manifest)
}

func assertAIWSManifest(t *testing.T, value Manifest) {
	t.Helper()
	wantServices := []string{"agent-runtime", "infrastructure", "keycloak-configure", "server", "web"}
	if value.Metadata.ID != "aiws" || !reflect.DeepEqual(sortedServiceIDs(value.Spec.Services), wantServices) {
		t.Fatalf("AIWS identity/services = %q %#v", value.Metadata.ID, sortedServiceIDs(value.Spec.Services))
	}
	infrastructure := value.Spec.Services["infrastructure"]
	if infrastructure.Compose == nil || len(infrastructure.Compose.Services) != 6 || len(infrastructure.Compose.Ports) != 10 {
		t.Fatalf("AIWS infrastructure = %#v", infrastructure.Compose)
	}
	configure := value.Spec.Services["keycloak-configure"]
	if configure.Mode != "oneshot" || configure.Runner != "python-venv" || configure.DependsOn["infrastructure"] != "ready" {
		t.Fatalf("AIWS Keycloak Configure = %#v", configure)
	}
	if value.Spec.Services["server"].DependsOn["keycloak-configure"] != "completed" ||
		value.Spec.Services["agent-runtime"].DependsOn["keycloak-configure"] != "completed" {
		t.Fatal("AIWS process services do not wait for Keycloak Configure completion")
	}
	web := value.Spec.Services["web"]
	if web.Environment["VITE_API_TARGET"] != "http://127.0.0.1:${ports.server}" ||
		web.Environment["VITE_OIDC_AUTHORITY"] != "http://127.0.0.1:${ports.keycloak}/realms/aiws" {
		t.Fatalf("AIWS Web propagation = %#v", web.Environment)
	}
}

func sortedServiceIDs(services map[string]Service) []string {
	result := make([]string, 0, len(services))
	for serviceID := range services {
		result = append(result, serviceID)
	}
	sort.Strings(result)
	return result
}
