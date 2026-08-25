package manifest

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestRealGNMarketManifest(t *testing.T) {
	root := os.Getenv("STACKPILOT_GNMARKET_PATH")
	if root == "" {
		t.Skip("set STACKPILOT_GNMARKET_PATH for the real GNMarket manifest Gate")
	}
	loader, err := NewLoader()
	if err != nil {
		t.Fatal(err)
	}
	document, err := loader.Load(context.Background(), filepath.Join(root, ".stackpilot", "system.yaml"))
	if err != nil {
		t.Fatalf("load real GNMarket manifest: %v", err)
	}
	validated, err := NewValidator().Validate(context.Background(), document, root)
	if err != nil {
		t.Fatalf("validate real GNMarket manifest: %v", err)
	}
	assertGNMarketManifest(t, validated.Manifest)
}

func assertGNMarketManifest(t *testing.T, value Manifest) {
	t.Helper()
	wantServices := []string{"frontend", "job", "web"}
	if value.Metadata.ID != "gnmarket" || !reflect.DeepEqual(sortedServiceIDs(value.Spec.Services), wantServices) {
		t.Fatalf("GNMarket identity/services = %q %#v", value.Metadata.ID, sortedServiceIDs(value.Spec.Services))
	}
	web := value.Spec.Services["web"]
	if web.Environment["SERVER_PORT"] != "${ports.backend}" ||
		web.Environment["DB_URL"] != "${secret.db-url}" ||
		web.Environment["DB_USERNAME"] != "${secret.db-username}" ||
		web.Environment["DB_PASSWORD"] != "${secret.db-password}" {
		t.Fatalf("GNMarket Web environment = %#v", web.Environment)
	}
	job := value.Spec.Services["job"]
	frontend := value.Spec.Services["frontend"]
	if job.DependsOn["web"] != "ready" || frontend.DependsOn["web"] != "ready" ||
		frontend.Environment["GNMARKET_FRONTEND_PORT"] != "${ports.web}" ||
		frontend.Environment["GNMARKET_API_PROXY_TARGET"] != "http://127.0.0.1:${ports.backend}" {
		t.Fatal("GNMarket dependency or dynamic port propagation is incomplete")
	}
}
