package orchestrator

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"stackpilot/internal/domain"
	"stackpilot/internal/manifest"
	"stackpilot/internal/runner"
	"stackpilot/internal/workspace"
)

func TestResolveSingleServiceExpandsTrustedRuntimeSpec(t *testing.T) {
	root := t.TempDir()
	working := filepath.Join(root, "backend")
	if err := os.Mkdir(working, 0o700); err != nil {
		t.Fatal(err)
	}
	port := availableTestPort(t)
	input := testResolveInput(root, port)
	resolved, err := resolveSingleService(context.Background(), staticRunnerResolver{}, input)
	if err != nil {
		t.Fatalf("resolveSingleService() error = %v", err)
	}
	if resolved.Service.ServiceID != "backend" || resolved.Process.WorkingDirectory != working {
		t.Fatalf("resolved service = %+v", resolved)
	}
	if got := resolved.Process.Environment["SERVER_PORT"]; got != strconv.Itoa(port) {
		t.Fatalf("SERVER_PORT = %q", got)
	}
	if resolved.Readiness.Port != port || resolved.Readiness.Kind != "tcp" {
		t.Fatalf("readiness = %+v", resolved.Readiness)
	}
	if len(resolved.System.ResolvedSpecDigest) != 64 || resolved.System.State != domain.SystemStarting {
		t.Fatalf("system instance = %+v", resolved.System)
	}
}

func TestResolveSingleServicePersistsOnlySecretReference(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "backend"), 0o700); err != nil {
		t.Fatal(err)
	}
	input := testResolveInput(root, availableTestPort(t))
	definition := input.Manifest.Spec.Services["backend"]
	definition.Environment["DATABASE_PASSWORD"] = "${secret.database-password}"
	input.Manifest.Spec.Services["backend"] = definition
	resolved, err := resolveSingleService(context.Background(), staticRunnerResolver{}, input)
	if err != nil {
		t.Fatal(err)
	}
	if got := resolved.Process.Environment["DATABASE_PASSWORD"]; got != "${secret.database-password}" {
		t.Fatalf("persisted environment = %q", got)
	}
	if got := resolved.Process.SecretReferences["DATABASE_PASSWORD"]; got != "database-password" {
		t.Fatalf("Secret reference = %q", got)
	}
}

func TestResolveSingleServiceRejectsOccupiedPortAndMultipleRoots(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "backend"), 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	input := testResolveInput(root, port)
	if _, err := resolveSingleService(context.Background(), staticRunnerResolver{}, input); !errors.Is(err, ErrPortInUse) {
		t.Fatalf("occupied port error = %v", err)
	}
	input.Manifest.Spec.Services["other"] = input.Manifest.Spec.Services["backend"]
	if _, _, err := singleRootService(input.Manifest); !errors.Is(err, ErrSingleServiceScope) {
		t.Fatalf("multiple roots error = %v", err)
	}
}

type staticRunnerResolver struct{}

func (staticRunnerResolver) Resolve(context.Context, runner.ResolveRequest) (*runner.ResolvedCommand, error) {
	return &runner.ResolvedCommand{
		Executable: `C:\tools\mvn.cmd`, Version: "3.9.9", ResolutionKind: runner.ResolutionPath,
		ExecutableDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}, nil
}

func testResolveInput(root string, port int) resolveSingleInput {
	required, threshold := true, 1
	preferred := port
	value := manifest.Manifest{
		Metadata: manifest.Metadata{ID: "btc", Name: "BTC"},
		Spec: manifest.Spec{
			Ports: map[string]manifest.Port{"backend": {Preferred: &preferred}},
			Services: map[string]manifest.Service{"backend": {
				Required: &required, Driver: "process", Mode: "daemon", Runner: "maven", WorkingDirectory: "backend",
				Arguments: []string{"spring-boot:run", "--port=${ports.backend}"}, Environment: map[string]string{"SERVER_PORT": "${ports.backend}"},
				Readiness: &manifest.HealthCheck{Type: "tcp", Host: "127.0.0.1", Port: "${ports.backend}", Timeout: "5s", Interval: "100ms", SuccessThreshold: &threshold, FailureThreshold: &threshold},
				Stop:      manifest.Stop{GracefulTimeout: "1s"},
			}},
		},
	}
	return resolveSingleInput{
		Workspace: workspace.Record{
			ID: "ws_01ARZ3NDEKTSV4RRFFQ69G5FAV", SystemID: "btc", CanonicalPath: root,
			ManifestStatus: workspace.ManifestValid, LastValidDigest: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		},
		Manifest: value, SystemID: "si_01ARZ3NDEKTSV4RRFFQ69G5FAV", ServiceID: "svi_01ARZ3NDEKTSV4RRFFQ69G5FAV",
		DataDir: root, PortOverrides: map[string]int{},
	}
}

func availableTestPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}
