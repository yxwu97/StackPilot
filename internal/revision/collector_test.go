package revision

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"stackpilot/internal/domain"
	"stackpilot/internal/runner"
	"stackpilot/internal/security"
	"stackpilot/internal/workspace"
)

func TestCollectorBuildsWorkspaceSnapshotWithoutSensitiveInputs(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "api"), 0o700); err != nil {
		t.Fatalf("create working directory: %v", err)
	}
	writeRevisionFixture(t, root, "api/package.json", `{"name":"fixture"}`)
	manifestJSON := `{"apiVersion":"stackpilot.io/v1alpha1","kind":"System","metadata":{"id":"sample","name":"Sample"},"spec":{"services":{"api":{"driver":"process","mode":"daemon","runner":"node","workingDirectory":"./api","arguments":["--token=SECRET_VALUE"],"environment":{"PASSWORD":"SECRET_VALUE"},"readiness":{"type":"process"},"liveness":{"type":"http"},"restart":{"policy":"never"}}}}}`
	source := &collectorWorkspaceSource{
		record: workspace.Record{ID: "ws_01ARZ3NDEKTSV4RRFFQ69G5FAV", SystemID: "sample", CanonicalPath: root},
		snapshot: workspace.Snapshot{SystemID: "sample", Digest: digestOf(manifestJSON), ParsedJSON: manifestJSON,
			Services: []workspace.ServiceDefinition{{ID: "api", Driver: domain.DriverProcess, Mode: domain.ProcessDaemon, Required: true, DefinitionDigest: digestOf("definition")}}},
	}
	collector := newCollectorFixture(t, source)
	snapshot, err := collector.Collect(context.Background(), source.record.ID, domain.RevisionWorkspace)
	if err != nil {
		t.Fatalf("Collect(workspace) error = %v", err)
	}
	encoded, _, err := Canonicalize(snapshot)
	if err != nil {
		t.Fatalf("Canonicalize() error = %v", err)
	}
	for _, forbidden := range []string{root, "SECRET_VALUE", "--token"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("workspace snapshot contains forbidden value %q: %s", forbidden, encoded)
		}
	}
	if len(snapshot.Files) != 1 || snapshot.Files[0].Path != "api/package.json" || len(snapshot.Runners) != 1 {
		t.Fatalf("workspace facts = %#v / %#v", snapshot.Files, snapshot.Runners)
	}
}

func TestCollectorRejectsManifestChangeDuringWorkspaceCollection(t *testing.T) {
	root := t.TempDir()
	source := &collectorWorkspaceSource{
		record:           workspace.Record{ID: "ws_01ARZ3NDEKTSV4RRFFQ69G5FAV", SystemID: "sample", CanonicalPath: root},
		snapshot:         workspace.Snapshot{SystemID: "sample", Digest: digestOf("one"), ParsedJSON: `{"apiVersion":"stackpilot.io/v1alpha1","kind":"System","metadata":{"id":"sample"},"spec":{"services":{}}}`},
		changeAfterFirst: true,
	}
	collector := newCollectorFixture(t, source)
	if _, err := collector.Collect(context.Background(), source.record.ID, domain.RevisionWorkspace); !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("Collect(changed) error = %v", err)
	}
}

func TestCollectorBuildsRunningSnapshotFromLaunchFactsOnly(t *testing.T) {
	instanceID := domain.SystemInstanceID("si_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	serviceID := domain.ServiceInstanceID("svi_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	workspaceID := domain.WorkspaceID("ws_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	resolved := map[string]any{"services": map[string]any{"api": map[string]any{
		"driver": "process", "required": true, "dependencies": map[string]string{},
		"process":  map[string]any{"Command": map[string]any{"Executable": `C:\\secret\\node.exe`, "Version": "22.1.0", "ResolutionKind": "path", "ExecutableDigest": digestOf("node")}, "Arguments": []string{"SECRET_ARGUMENT"}},
		"liveness": map[string]any{"Type": "process"}, "restart": map[string]any{"policy": "never"},
	}}}
	encodedResolved, _ := json.Marshal(resolved)
	config := CollectorConfig{
		Workspaces: &collectorWorkspaceSource{snapshot: workspace.Snapshot{Digest: digestOf("manifest"), ParsedJSON: `{"spec":{"ports":{}}}`}},
		Runtime: collectorRuntimeSource{
			instance: domain.SystemInstance{ID: instanceID, WorkspaceID: workspaceID, SystemID: "sample", ManifestDigest: digestOf("manifest"), ResolvedSpecDigest: digestOfBytes(encodedResolved), State: domain.SystemRunning},
			services: []domain.ServiceInstance{{ID: serviceID, SystemInstanceID: instanceID, ServiceID: "api", Driver: domain.DriverProcess, ProcessMode: domain.ProcessDaemon, State: domain.ServiceReady,
				Identity: &domain.ProcessIdentity{PID: 42, ExecutablePath: `C:\secret\node.exe`, CommandDigest: digestOf("command"), PlatformToken: "SECRET_PLATFORM_TOKEN"}}},
		},
		ResolvedSpecs:  collectorResolvedSource{value: encodedResolved},
		SecretVersions: collectorSecretSource{values: []security.ServiceSecretVersion{{ServiceInstanceID: serviceID, EnvironmentName: "DB_PASSWORD", Key: security.SecretKey{SystemID: "sample", Name: "db-password"}, Provider: security.SecretProviderDPAPIFile, Version: 3, ResolvedAt: time.Now().UTC()}}},
		Runners:        collectorRunner{}, Files: NewFileCollector(),
	}
	collector, err := NewCollector(config)
	if err != nil {
		t.Fatalf("NewCollector() error = %v", err)
	}
	snapshot, err := collector.Collect(context.Background(), workspaceID, domain.RevisionRunning)
	if err != nil {
		t.Fatalf("Collect(running) error = %v", err)
	}
	encoded, _, err := Canonicalize(snapshot)
	if err != nil {
		t.Fatalf("Canonicalize(running) error = %v", err)
	}
	for _, forbidden := range []string{`C:\secret`, "SECRET_ARGUMENT", "SECRET_PLATFORM_TOKEN"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("running snapshot contains forbidden value %q: %s", forbidden, encoded)
		}
	}
	if snapshot.Git.Reason != "LAUNCH_GIT_FACT_NOT_RECORDED" || len(snapshot.Secrets) != 1 || snapshot.Secrets[0].Version != 3 {
		t.Fatalf("running metadata = %#v / %#v", snapshot.Git, snapshot.Secrets)
	}
}

func TestWorkspaceComposeDigestUsesResolvedDefaultBuildPolicy(t *testing.T) {
	root := t.TempDir()
	writeRevisionFixture(t, root, "compose.yaml", "services: {}")
	manifestJSON := `{"apiVersion":"stackpilot.io/v1alpha1","kind":"System","metadata":{"id":"sample","name":"Sample"},"spec":{"services":{"infrastructure":{"driver":"compose","mode":"daemon","required":true,"compose":{"file":"compose.yaml","services":["database"]},"readiness":{"type":"compose"},"liveness":{"type":"compose"},"restart":{"policy":"never"}}}}}`
	source := &collectorWorkspaceSource{
		record: workspace.Record{ID: "ws_01ARZ3NDEKTSV4RRFFQ69G5FAV", SystemID: "sample", CanonicalPath: root},
		snapshot: workspace.Snapshot{SystemID: "sample", Digest: digestOf(manifestJSON), ParsedJSON: manifestJSON,
			Services: []workspace.ServiceDefinition{{ID: "infrastructure", Driver: domain.DriverCompose, Mode: domain.ProcessDaemon, Required: true, DefinitionDigest: digestOf("definition")}}},
	}
	collector := newCollectorFixture(t, source)
	snapshot, err := collector.Collect(context.Background(), source.record.ID, domain.RevisionWorkspace)
	if err != nil {
		t.Fatalf("Collect(workspace Compose) error = %v", err)
	}
	want := digestJSON(struct {
		Services    []string `json:"services"`
		BuildPolicy string   `json:"buildPolicy"`
	}{[]string{"database"}, "never"})
	if len(snapshot.Services) != 1 || snapshot.Services[0].ComposeDigest != want {
		t.Fatalf("workspace Compose digest = %#v, want default policy digest %s", snapshot.Services, want)
	}
}

type collectorWorkspaceSource struct {
	record           workspace.Record
	snapshot         workspace.Snapshot
	calls            int
	changeAfterFirst bool
}

func (source *collectorWorkspaceSource) Get(context.Context, domain.WorkspaceID) (*workspace.Record, error) {
	copy := source.record
	return &copy, nil
}

func (source *collectorWorkspaceSource) CurrentSnapshot(context.Context, string) (workspace.Snapshot, error) {
	source.calls++
	result := source.snapshot
	if source.changeAfterFirst && source.calls > 1 {
		result.Digest = digestOf("two")
	}
	return result, nil
}

func (source *collectorWorkspaceSource) ManifestByDigest(context.Context, string) (workspace.ManifestView, error) {
	return workspace.ManifestView{Digest: source.snapshot.Digest, ParsedJSON: source.snapshot.ParsedJSON}, nil
}

type collectorRuntimeSource struct {
	instance domain.SystemInstance
	services []domain.ServiceInstance
}

func (source collectorRuntimeSource) GetActive(context.Context, domain.WorkspaceID) (*domain.SystemInstance, bool, error) {
	copy := source.instance
	return &copy, true, nil
}

func (source collectorRuntimeSource) ListServices(context.Context, domain.SystemInstanceID) ([]domain.ServiceInstance, error) {
	return append([]domain.ServiceInstance(nil), source.services...), nil
}

type collectorResolvedSource struct{ value []byte }

func (source collectorResolvedSource) LoadResolvedSpec(context.Context, string) ([]byte, error) {
	return append([]byte(nil), source.value...), nil
}

type collectorSecretSource struct {
	values []security.ServiceSecretVersion
}

func (source collectorSecretSource) ListServiceSecretVersions(context.Context, domain.ServiceInstanceID) ([]security.ServiceSecretVersion, error) {
	return append([]security.ServiceSecretVersion(nil), source.values...), nil
}

type collectorRunner struct{}

func (collectorRunner) Resolve(context.Context, runner.ResolveRequest) (*runner.ResolvedCommand, error) {
	return &runner.ResolvedCommand{Executable: `C:\\tools\\node.exe`, Version: "22.1.0", ResolutionKind: runner.ResolutionPath, ExecutableDigest: digestOf("node")}, nil
}

func newCollectorFixture(t *testing.T, source *collectorWorkspaceSource) *Collector {
	t.Helper()
	collector, err := NewCollector(CollectorConfig{
		Workspaces: source, Runtime: collectorRuntimeSource{}, ResolvedSpecs: collectorResolvedSource{},
		SecretVersions: collectorSecretSource{}, Runners: collectorRunner{}, Files: NewFileCollector(),
	})
	if err != nil {
		t.Fatalf("NewCollector() error = %v", err)
	}
	return collector
}

func digestOfBytes(value []byte) string {
	return digestOf(string(value))
}
