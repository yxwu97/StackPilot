package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"stackpilot/internal/domain"
	"stackpilot/internal/driver"
	"stackpilot/internal/driver/compose"
	"stackpilot/internal/manifest"
	"stackpilot/internal/ports"
	"stackpilot/internal/runner"
	"stackpilot/internal/workspace"
)

const resolverTestDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestResolveSystemSpecExpandsPortsAndRecordsReferences(t *testing.T) {
	root := systemResolverRoot(t)
	events := make([]string, 0)
	input := testSystemResolveInput(root)
	resolved, err := resolveSystemSpec(context.Background(), &recordingResolver{events: &events}, &recordingPreflighter{events: &events}, input)
	if err != nil {
		t.Fatalf("resolveSystemSpec() error = %v", err)
	}
	wantEvents := []string{"resolve:backend", "resolve:web", "preflight:backend", "preflight:web"}
	if !reflect.DeepEqual(events, wantEvents) {
		t.Fatalf("preflight order = %#v, want %#v", events, wantEvents)
	}
	if got := resolved.Services["backend"].Process.Environment["SERVER_PORT"]; got != "8081" {
		t.Fatalf("backend SERVER_PORT = %q", got)
	}
	if got := resolved.Services["web"].Process.Environment["VITE_API_TARGET"]; got != "http://127.0.0.1:8081" {
		t.Fatalf("web VITE_API_TARGET = %q", got)
	}
	if got := resolved.Services["web"].Readiness.URL; got != "http://127.0.0.1:32102" {
		t.Fatalf("web readiness URL = %q", got)
	}
	wantBackendRefs := []string{"services.backend.environment.SERVER_PORT", "services.backend.readiness.url", "services.web.environment.VITE_API_TARGET"}
	if got := resolved.PortReferences["backend"]; !reflect.DeepEqual(got, wantBackendRefs) {
		t.Fatalf("backend references = %#v, want %#v", got, wantBackendRefs)
	}
	wantWebRefs := []string{"services.backend.environment.BIDTRAVEL_CORS_ALLOWED_ORIGINS", "services.web.arguments[3]", "services.web.readiness.url"}
	if got := resolved.PortReferences["web"]; !reflect.DeepEqual(got, wantWebRefs) {
		t.Fatalf("web references = %#v, want %#v", got, wantWebRefs)
	}
	if len(resolved.Digest) != 64 || len(resolved.CanonicalJSON) == 0 || string(resolved.CanonicalJSON) == "{}" {
		t.Fatalf("resolved digest/json = %q, %d bytes", resolved.Digest, len(resolved.CanonicalJSON))
	}
}

func TestResolveSystemSpecIsDeterministic(t *testing.T) {
	root := systemResolverRoot(t)
	input := testSystemResolveInput(root)
	first, err := resolveSystemSpec(context.Background(), &recordingResolver{}, &recordingPreflighter{}, input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := resolveSystemSpec(context.Background(), &recordingResolver{}, &recordingPreflighter{}, input)
	if err != nil {
		t.Fatal(err)
	}
	if first.Digest != second.Digest || !reflect.DeepEqual(first.CanonicalJSON, second.CanonicalJSON) {
		t.Fatalf("resolved spec changed: %s != %s", first.Digest, second.Digest)
	}
}

func TestResolveSystemSpecProcessStopInheritsSystemPolicy(t *testing.T) {
	root := systemResolverRoot(t)
	input := testSystemResolveInput(root)
	input.Manifest.Spec.Policies.StopTimeout = "45s"
	web := input.Manifest.Spec.Services["web"]
	web.Stop = manifest.Stop{}
	input.Manifest.Spec.Services["web"] = web

	resolved, err := resolveSystemSpec(context.Background(), &recordingResolver{}, &recordingPreflighter{}, input)
	if err != nil {
		t.Fatal(err)
	}
	if got := resolved.Services["web"].Process.GracefulTimeout; got != 45*time.Second {
		t.Fatalf("web graceful timeout = %v, want 45s", got)
	}
}

func TestResolveSystemSpecCompletesRunnerResolutionBeforeDriverPreflight(t *testing.T) {
	root := systemResolverRoot(t)
	events := make([]string, 0)
	runnerFailure := errors.New("npm unavailable")
	_, err := resolveSystemSpec(context.Background(), &recordingResolver{events: &events, failService: "web", failure: runnerFailure}, &recordingPreflighter{events: &events}, testSystemResolveInput(root))
	if !errors.Is(err, runnerFailure) {
		t.Fatalf("resolveSystemSpec() error = %v", err)
	}
	want := []string{"resolve:backend", "resolve:web"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %#v, want %#v", events, want)
	}
}

func TestResolveSystemSpecUsesDedicatedComposeBranch(t *testing.T) {
	root, dataDir := t.TempDir(), t.TempDir()
	composeFile := filepath.Join(root, "compose.yaml")
	if err := os.WriteFile(composeFile, []byte("services:\n  database: {image: scratch}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	overrides, err := compose.NewOverrideGenerator(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	required, threshold := true, 1
	input := resolveSystemInput{
		Workspace: workspace.Record{ID: "ws_01ARZ3NDEKTSV4RRFFQ69G5FAV", SystemID: "btc", CanonicalPath: root, ManifestStatus: workspace.ManifestValid, LastValidDigest: resolverTestDigest},
		Manifest: manifest.Manifest{Metadata: manifest.Metadata{ID: "btc"}, Spec: manifest.Spec{Policies: manifest.Policies{StartTimeout: "1m", StopTimeout: "30s"}, Ports: map[string]manifest.Port{"database": {Protocol: "tcp"}}, Services: map[string]manifest.Service{"infrastructure": {
			Required: &required, Driver: "compose", Mode: "daemon", Compose: &manifest.ComposeService{File: "compose.yaml", Services: []string{"database"}, Ports: map[string]manifest.ComposePort{"database": {Service: "database", Target: 5432}}, Environment: map[string]map[string]string{"database": {"DATABASE_PORT": "${ports.database}"}}},
			Readiness: &manifest.HealthCheck{Type: "compose", Timeout: "10s", Interval: "100ms", SuccessThreshold: &threshold, FailureThreshold: &threshold}, Stop: manifest.Stop{GracefulTimeout: "20s"},
		}}}},
		InstanceID: "si_01ARZ3NDEKTSV4RRFFQ69G5FAV", OperationID: "op_01ARZ3NDEKTSV4RRFFQ69G5FAV", DataDir: dataDir, Overrides: overrides,
		PortPlan: &ports.Plan{ID: "pp_01ARZ3NDEKTSV4RRFFQ69G5FAV", WorkspaceID: "ws_01ARZ3NDEKTSV4RRFFQ69G5FAV", Assignments: map[string]ports.Assignment{"database": {LogicalName: "database", Port: 15432, Source: "preferred", LeaseID: "pl_01ARZ3NDEKTSV4RRFFQ69G5FAV"}}},
	}
	events := make([]string, 0)
	resolved, err := resolveSystemSpec(context.Background(), &recordingResolver{events: &events}, &recordingPreflighter{events: &events}, input)
	if err != nil {
		t.Fatal(err)
	}
	service := resolved.Services["infrastructure"]
	if len(events) != 0 || service.Driver != domain.DriverCompose || service.Compose == nil || service.Process.Command.Executable != "" || service.Readiness.Kind != "compose" {
		t.Fatalf("resolved Compose service = %#v; runner events=%#v", service, events)
	}
	if service.Compose.ComposeFile != composeFile || service.Compose.StopTimeout.String() != "20s" {
		t.Fatalf("resolved Compose lifecycle = %#v", service.Compose)
	}
	contents, err := os.ReadFile(service.Compose.OverrideFile)
	if err != nil || !strings.Contains(string(contents), `published: "15432"`) || !strings.Contains(string(contents), "DATABASE_PORT: \"15432\"") {
		t.Fatalf("Compose override = %q, %v", contents, err)
	}
	if got := resolved.PortReferences["database"]; !reflect.DeepEqual(got, []string{"services.infrastructure.compose.environment.database.DATABASE_PORT", "services.infrastructure.compose.ports.database"}) {
		t.Fatalf("Compose references = %#v", got)
	}
}

func TestResolveSystemSpecAppliesComposeHealthDurationDefaults(t *testing.T) {
	root, dataDir := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "compose.yaml"), []byte("services:\n  database: {image: scratch}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	overrides, err := compose.NewOverrideGenerator(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	required, threshold := true, 1
	input := resolveSystemInput{
		Workspace: workspace.Record{ID: "ws_01ARZ3NDEKTSV4RRFFQ69G5FAV", SystemID: "btc", CanonicalPath: root, ManifestStatus: workspace.ManifestValid, LastValidDigest: resolverTestDigest},
		Manifest: manifest.Manifest{Metadata: manifest.Metadata{ID: "btc"}, Spec: manifest.Spec{Policies: manifest.Policies{StartTimeout: "3m", StopTimeout: "30s"}, Services: map[string]manifest.Service{"infrastructure": {
			Required: &required, Driver: "compose", Mode: "daemon", Compose: &manifest.ComposeService{File: "compose.yaml", Services: []string{"database"}},
			Readiness: &manifest.HealthCheck{Type: "compose", SuccessThreshold: &threshold, FailureThreshold: &threshold}, Stop: manifest.Stop{GracefulTimeout: "20s"},
		}}}},
		InstanceID: "si_01ARZ3NDEKTSV4RRFFQ69G5FAV", OperationID: "op_01ARZ3NDEKTSV4RRFFQ69G5FAV", DataDir: dataDir, Overrides: overrides,
		PortPlan: &ports.Plan{ID: "pp_01ARZ3NDEKTSV4RRFFQ69G5FAV", WorkspaceID: "ws_01ARZ3NDEKTSV4RRFFQ69G5FAV", Assignments: map[string]ports.Assignment{}},
	}

	resolved, err := resolveSystemSpec(context.Background(), &recordingResolver{}, &recordingPreflighter{}, input)
	if err != nil {
		t.Fatal(err)
	}
	readiness := resolved.Services["infrastructure"].Readiness
	if readiness.ReadinessTimeout != 3*time.Minute || readiness.Interval != 2*time.Second || readiness.CheckTimeout != 2*time.Second {
		t.Fatalf("resolved Compose readiness = %#v", readiness)
	}
}

func systemResolverRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, directory := range []string{"backend", "web"} {
		if err := os.Mkdir(filepath.Join(root, directory), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func testSystemResolveInput(root string) resolveSystemInput {
	required, threshold := true, 1
	manifestValue := manifest.Manifest{Metadata: manifest.Metadata{ID: "btc", Name: "BTC"}, Spec: manifest.Spec{
		Policies: manifest.Policies{},
		Ports: map[string]manifest.Port{
			"backend": {Protocol: "tcp", ConflictPolicy: "auto", Exposure: "loopback"},
			"web":     {Protocol: "tcp", ConflictPolicy: "auto", Exposure: "loopback"},
		},
		Services: map[string]manifest.Service{
			"backend": {
				Required: &required, Driver: "process", Mode: "daemon", Runner: "maven", WorkingDirectory: "backend",
				Arguments: []string{"spring-boot:run"}, Environment: map[string]string{
					"SERVER_PORT": "${ports.backend}", "BIDTRAVEL_CORS_ALLOWED_ORIGINS": "http://127.0.0.1:${ports.web}",
				},
				Readiness: &manifest.HealthCheck{Type: "http", URL: "http://127.0.0.1:${ports.backend}/actuator/health", Timeout: "5s", Interval: "100ms", SuccessThreshold: &threshold, FailureThreshold: &threshold},
				Stop:      manifest.Stop{GracefulTimeout: "1s"},
			},
			"web": {
				Required: &required, Driver: "process", Mode: "daemon", Runner: "npm", WorkingDirectory: "web",
				Arguments: []string{"run", "dev", "--port", "${ports.web}"}, Environment: map[string]string{"VITE_API_TARGET": "http://127.0.0.1:${ports.backend}"},
				DependsOn: map[string]string{"backend": "ready"},
				Readiness: &manifest.HealthCheck{Type: "http", URL: "http://127.0.0.1:${ports.web}", Timeout: "5s", Interval: "100ms", SuccessThreshold: &threshold, FailureThreshold: &threshold},
				Stop:      manifest.Stop{GracefulTimeout: "1s"},
			},
		},
	}}
	plan := &ports.Plan{
		ID: "pp_01ARZ3NDEKTSV4RRFFQ69G5FAV", WorkspaceID: "ws_01ARZ3NDEKTSV4RRFFQ69G5FAV",
		Assignments: map[string]ports.Assignment{
			"backend": {LogicalName: "backend", Port: 8081, Source: "preferred", LeaseID: "pl_01ARZ3NDEKTSV4RRFFQ69G5FAV"},
			"web":     {LogicalName: "web", Port: 32102, Source: "preferred", LeaseID: "pl_01ARZ3NDEKTSV4RRFFQ69G5FAW"},
		},
	}
	return resolveSystemInput{
		Workspace: workspace.Record{ID: "ws_01ARZ3NDEKTSV4RRFFQ69G5FAV", SystemID: "btc", CanonicalPath: root, ManifestStatus: workspace.ManifestValid, LastValidDigest: resolverTestDigest},
		Manifest:  manifestValue, InstanceID: "si_01ARZ3NDEKTSV4RRFFQ69G5FAV", DataDir: root, PortPlan: plan,
	}
}

type recordingResolver struct {
	events      *[]string
	failService string
	failure     error
}

func (resolver *recordingResolver) Resolve(_ context.Context, request runner.ResolveRequest) (*runner.ResolvedCommand, error) {
	serviceID := filepath.Base(request.WorkingDirectory)
	if resolver.events != nil {
		*resolver.events = append(*resolver.events, "resolve:"+serviceID)
	}
	if resolver.failService == serviceID {
		return nil, resolver.failure
	}
	return &runner.ResolvedCommand{Executable: `C:\tools\` + string(request.Runner) + `.cmd`, ExecutableDigest: resolverTestDigest}, nil
}

type recordingPreflighter struct{ events *[]string }

func (preflighter *recordingPreflighter) Preflight(_ context.Context, spec driver.ResolvedServiceSpec) error {
	if preflighter.events != nil {
		*preflighter.events = append(*preflighter.events, "preflight:"+spec.ServiceID.String())
	}
	return nil
}
