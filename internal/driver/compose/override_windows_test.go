//go:build windows

package compose

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestOverrideGeneratorRejectsRuntimeJunctionEscape(t *testing.T) {
	dataDir := t.TempDir()
	outside := t.TempDir()
	runtimeDirectory := filepath.Join(dataDir, "runtime")
	command := exec.Command("cmd.exe", "/d", "/s", "/c", "mklink", "/J", runtimeDirectory, outside)
	if output, err := command.CombinedOutput(); err != nil {
		t.Skipf("directory junctions are unavailable: %v (%s)", err, output)
	}
	t.Cleanup(func() { _ = os.Remove(runtimeDirectory) })
	generator, err := NewOverrideGenerator(dataDir)
	if err != nil {
		t.Fatalf("NewOverrideGenerator() error = %v", err)
	}
	if _, err := generator.Generate(validOverrideRequest()); !errors.Is(err, ErrOverrideInvalid) {
		t.Fatalf("Generate(junction escape) error = %v, want ErrOverrideInvalid", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "operations")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("junction escape created an outside directory: %v", err)
	}
}

func TestInstalledComposeOverrideConfig(t *testing.T) {
	if os.Getenv("STACKPILOT_COMPOSE_OVERRIDE_INTEGRATION") != "1" {
		t.Skip("set STACKPILOT_COMPOSE_OVERRIDE_INTEGRATION=1 for the installed Docker Compose Gate")
	}
	docker, err := exec.LookPath("docker.exe")
	if err != nil {
		t.Fatalf("resolve installed docker.exe: %v", err)
	}
	root := t.TempDir()
	base := filepath.Join(root, "compose.yaml")
	contents := []byte("services:\n  database:\n    image: scratch\n  web:\n    image: scratch\n")
	if err := os.WriteFile(base, contents, 0o600); err != nil {
		t.Fatalf("write base Compose fixture: %v", err)
	}
	generator, err := NewOverrideGenerator(root)
	if err != nil {
		t.Fatalf("NewOverrideGenerator() error = %v", err)
	}
	result, err := generator.Generate(validOverrideRequest())
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	config := runComposeConfig(t, docker, base, result.Path)
	assertMergedComposeConfig(t, config)
}

func runComposeConfig(t *testing.T, docker, base, override string) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, docker, "compose", "--file", base, "--file", override, "config", "--format", "json", "--no-interpolate")
	command.Dir = filepath.Dir(base)
	output, err := command.Output()
	if err != nil {
		t.Fatalf("docker compose config failed: %v", err)
	}
	var config map[string]any
	if err := json.Unmarshal(output, &config); err != nil {
		t.Fatalf("decode Compose config JSON: %v", err)
	}
	return config
}

func assertMergedComposeConfig(t *testing.T, config map[string]any) {
	t.Helper()
	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("encode merged config: %v", err)
	}
	var document struct {
		Services map[string]struct {
			Command     any               `json:"command"`
			Environment map[string]string `json:"environment"`
			Labels      map[string]string `json:"labels"`
			Ports       []struct {
				HostIP    string `json:"host_ip"`
				Published string `json:"published"`
				Target    int    `json:"target"`
			} `json:"ports"`
			Privileged bool `json:"privileged"`
			Volumes    any  `json:"volumes"`
		} `json:"services"`
	}
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatalf("decode merged Compose shape: %v", err)
	}
	database := document.Services["database"]
	if len(database.Ports) != 1 || database.Ports[0].HostIP != "127.0.0.1" || database.Ports[0].Published != "15432" || database.Ports[0].Target != 5432 {
		t.Fatalf("unexpected database ports: %#v", database.Ports)
	}
	if database.Environment["DATABASE_NAME"] != "stackpilot" || database.Labels["stackpilot.service"] != "database" {
		t.Fatalf("missing approved environment or labels: environment=%#v labels=%#v", database.Environment, database.Labels)
	}
	for name, service := range document.Services {
		if service.Command != nil || service.Privileged || service.Volumes != nil {
			t.Fatalf("service %q contains a forbidden override field", name)
		}
	}
}
