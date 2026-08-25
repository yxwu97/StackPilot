package compose

import (
	"errors"
	"strings"
	"testing"
	"time"

	"stackpilot/internal/domain"
)

func TestProjectNameIsDeterministicBoundedAndValid(t *testing.T) {
	workspace := domain.WorkspaceID("ws_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	instance := domain.SystemInstanceID("si_01BX5ZZKBKACTAV9WEVGEMMVRZ")
	short, err := ProjectName(domain.SystemID("btc"), workspace, instance)
	if err != nil || short != "sp-btc-01arz3nd-01bx5zzk" {
		t.Fatalf("ProjectName(short) = %q, %v", short, err)
	}
	longID := domain.SystemID("a" + strings.Repeat("b", 62))
	first, err := ProjectName(longID, workspace, instance)
	if err != nil {
		t.Fatalf("ProjectName(long) error = %v", err)
	}
	second, _ := ProjectName(longID, workspace, instance)
	if len(first) > maximumProjectNameLength || first != second || !strings.HasPrefix(first, "sp-") {
		t.Fatalf("invalid bounded project name %q", first)
	}
	if _, err := ProjectName("INVALID", workspace, instance); !errors.Is(err, ErrLifecycleInvalid) {
		t.Fatalf("ProjectName(invalid) error = %v", err)
	}
}

func TestDecodeComposePSSupportsArrayAndNDJSON(t *testing.T) {
	row := `{"ID":"abc","Name":"project-db-1","Project":"project","Service":"database","State":"running","Health":"healthy","ExitCode":0}`
	row = strings.ReplaceAll(row, "\\\"", "\"")
	for _, input := range []string{"[" + row + "]", row + "\n" + strings.Replace(row, "project-db-1", "project-db-2", 1)} {
		rows, err := decodeComposePS([]byte(input))
		if err != nil || len(rows) == 0 || rows[0].Service != "database" {
			t.Fatalf("decodeComposePS() = %#v, %v", rows, err)
		}
	}
	if _, err := decodeComposePS([]byte("not-json")); !errors.Is(err, ErrComposeInspectFailed) {
		t.Fatalf("decodeComposePS(invalid) error = %v", err)
	}
}

func TestBuildProjectObservationChecksProjectAndServices(t *testing.T) {
	identity := ProjectIdentity{ProjectName: "project", Services: []string{"database"}}
	rows := []composePSRow{{ID: "abc", Name: "project-database-1", Project: "project", Service: "database", State: "running", Health: "healthy"}}
	observation, err := buildProjectObservation(identity, rows)
	if err != nil || observation.State != "running" || len(observation.Containers) != 1 {
		t.Fatalf("buildProjectObservation() = %#v, %v", observation, err)
	}
	rows[0].Project = "other"
	if _, err := buildProjectObservation(identity, rows); !errors.Is(err, ErrProjectIdentityMismatch) {
		t.Fatalf("project mismatch error = %v", err)
	}
	if _, err := buildProjectObservation(identity, nil); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("missing project error = %v", err)
	}
}

func TestProjectIdentityEncodingIsStrictAndDetectsTampering(t *testing.T) {
	identity, err := newProjectIdentity(normalizedLifecycleRequest(LifecycleRequest{
		WorkspaceRoot: `C:\workspace`, DataDir: `C:\data`, ComposeFile: `C:\workspace\compose.yaml`,
		OverrideFile: `C:\data\override.yaml`, SystemID: domain.SystemID("btc"),
		WorkspaceID: domain.WorkspaceID("ws_01ARZ3NDEKTSV4RRFFQ69G5FAV"),
		InstanceID:  domain.SystemInstanceID("si_01ARZ3NDEKTSV4RRFFQ69G5FAV"), Services: []string{"database"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	token, err := EncodeProjectIdentity(identity)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeProjectIdentity(token)
	if err != nil || decoded.DefinitionDigest != identity.DefinitionDigest {
		t.Fatalf("DecodeProjectIdentity() = %#v, %v", decoded, err)
	}
	identity.ProjectName = "tampered"
	if _, err := EncodeProjectIdentity(identity); !errors.Is(err, ErrProjectIdentityMismatch) {
		t.Fatalf("EncodeProjectIdentity(tampered) error = %v", err)
	}
}

func TestProjectIdentityDecodesLegacyTokenWithoutStartTimeout(t *testing.T) {
	identity, err := newProjectIdentity(normalizedLifecycleRequest(LifecycleRequest{
		WorkspaceRoot: `C:\workspace`, DataDir: `C:\data`, ComposeFile: `C:\workspace\compose.yaml`,
		OverrideFile: `C:\data\override.yaml`, SystemID: domain.SystemID("btc"),
		WorkspaceID: domain.WorkspaceID("ws_01ARZ3NDEKTSV4RRFFQ69G5FAV"),
		InstanceID:  domain.SystemInstanceID("si_01ARZ3NDEKTSV4RRFFQ69G5FAV"), Services: []string{"database"},
		StopTimeout: 7 * time.Second,
	}))
	if err != nil {
		t.Fatal(err)
	}
	identity.StartTimeout = 0
	identity.DefinitionDigest, err = projectIdentityDigest(identity)
	if err != nil {
		t.Fatal(err)
	}
	token, err := EncodeProjectIdentity(identity)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeProjectIdentity(token)
	if err != nil || decoded.StartTimeout != 0 {
		t.Fatalf("DecodeProjectIdentity(legacy) = %#v, %v", decoded, err)
	}
}
