//go:build windows

package compose

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRealAIWSComposePreflight(t *testing.T) {
	root := os.Getenv("STACKPILOT_AIWS_WORKSPACE")
	if root == "" {
		t.Skip("set STACKPILOT_AIWS_WORKSPACE for the real AIWS Compose Gate")
	}
	preflight, err := NewPreflighter(Config{Timeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"clamav", "keycloak", "minio", "otel-collector", "postgres", "qdrant"}
	result, err := preflight.Preflight(context.Background(), PreflightRequest{
		WorkspaceRoot: root,
		ComposeFile:   filepath.Join(root, "deploy", "stackpilot-compose.yml"),
		Services:      want,
	})
	if err != nil {
		t.Fatalf("preflight real AIWS Compose: %v", err)
	}
	if !reflect.DeepEqual(result.Services, want) {
		t.Fatalf("AIWS Compose services = %#v, want %#v", result.Services, want)
	}
	t.Logf("docker=%s daemon=%s compose=%s services=%v", result.DockerClientVersion,
		result.DockerServerVersion, result.ComposeVersion, result.Services)
}

func TestRealAIWSComposeLifecycle(t *testing.T) {
	root := os.Getenv("STACKPILOT_AIWS_WORKSPACE")
	if root == "" || os.Getenv("STACKPILOT_AIWS_INFRASTRUCTURE_INTEGRATION") != "1" {
		t.Skip("set AIWS workspace and infrastructure integration variables for the real Gate")
	}
	docker, err := exec.LookPath("docker.exe")
	if err != nil {
		t.Fatal(err)
	}
	dataDir := t.TempDir()
	services := []string{"postgres", "keycloak", "minio", "clamav", "qdrant", "otel-collector"}
	overrideRequest := aiwsOverrideRequest(t, services)
	generator, err := NewOverrideGenerator(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	override, err := generator.Generate(overrideRequest)
	if err != nil {
		t.Fatal(err)
	}
	request := LifecycleRequest{
		WorkspaceRoot: root, DataDir: dataDir,
		ComposeFile: filepath.Join(root, "deploy", "stackpilot-compose.yml"), OverrideFile: override.Path,
		SystemID: overrideRequest.SystemID, WorkspaceID: overrideRequest.WorkspaceID,
		InstanceID: overrideRequest.InstanceID, Services: services,
		StartTimeout: 10 * time.Minute, StopTimeout: 30 * time.Second,
	}
	project, _ := ProjectName(request.SystemID, request.WorkspaceID, request.InstanceID)
	t.Cleanup(func() { cleanupAIWSProject(t, docker, root, project, request) })
	lifecycle, err := NewLifecycle(LifecycleConfig{DockerExecutable: docker})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := lifecycle.Start(context.Background(), request)
	if err != nil {
		t.Fatalf("start real AIWS infrastructure: %v", err)
	}
	observation, err := lifecycle.Inspect(context.Background(), identity)
	if err != nil || observation.State != "running" || len(observation.Containers) != len(services) {
		t.Fatalf("inspect real AIWS infrastructure = %#v, %v", observation, err)
	}
	if err := lifecycle.Stop(context.Background(), identity); err != nil {
		t.Fatalf("stop real AIWS infrastructure: %v", err)
	}
	assertAIWSVolumesSurviveStop(t, docker, root, project)
	t.Logf("project=%s containers=%d health=healthy volumes=preserved", project, len(observation.Containers))
}

func aiwsOverrideRequest(t *testing.T, services []string) OverrideRequest {
	t.Helper()
	ports := availableAIWSPorts(t, 10)
	names := []struct {
		logical string
		service string
		target  int
	}{
		{"postgres", "postgres", 5432}, {"keycloak", "keycloak", 18180},
		{"minio-api", "minio", 9000}, {"minio-console", "minio", 9001},
		{"clamav", "clamav", 3310}, {"qdrant-http", "qdrant", 6333},
		{"qdrant-grpc", "qdrant", 6334}, {"otlp-grpc", "otel-collector", 4317},
		{"otlp-http", "otel-collector", 4318}, {"otel-metrics", "otel-collector", 8889},
	}
	mappings := make(map[string]PortOverride, len(names))
	for index, value := range names {
		mappings[value.logical] = PortOverride{Service: value.service, Target: value.target, Published: ports[index]}
	}
	return OverrideRequest{
		OperationID: "op_01ARZ3NDEKTSV4RRFFQ69G5FAV", SystemID: "aiws",
		WorkspaceID: "ws_01ARZ3NDEKTSV4RRFFQ69G5FAV", InstanceID: "si_01ARZ3NDEKTSV4RRFFQ69G5FAV",
		Services: services, Ports: mappings,
	}
}

func availableAIWSPorts(t *testing.T, count int) []int {
	t.Helper()
	listeners := make([]net.Listener, 0, count)
	ports := make([]int, 0, count)
	for range count {
		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		listeners = append(listeners, listener)
		ports = append(ports, listener.Addr().(*net.TCPAddr).Port)
	}
	for _, listener := range listeners {
		_ = listener.Close()
	}
	return ports
}

func assertAIWSVolumesSurviveStop(t *testing.T, docker, root, project string) {
	t.Helper()
	output, err := runCommand(context.Background(), docker,
		[]string{"volume", "ls", "--quiet", "--filter", "label=com.docker.compose.project=" + project},
		root, normalizedEnvironment(currentEnvironment()))
	if err != nil || strings.TrimSpace(string(output.Stdout)) == "" {
		t.Fatalf("AIWS volumes were removed by ordinary stop: output=%q err=%v", output.Stdout, err)
	}
}

func cleanupAIWSProject(t *testing.T, docker, root, project string, request LifecycleRequest) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	base := []string{"compose", "--project-name", project, "--file", request.ComposeFile, "--file", request.OverrideFile}
	if _, err := runCommand(ctx, docker, append(base, "down", "--volumes", "--remove-orphans"),
		root, normalizedEnvironment(currentEnvironment())); err != nil {
		t.Errorf("clean real AIWS Compose Gate: %v", err)
	}
}
