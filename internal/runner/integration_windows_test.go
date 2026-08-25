//go:build windows

package runner

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestInstalledRunnerToolchain(t *testing.T) {
	if os.Getenv("STACKPILOT_RUNNER_INTEGRATION") != "1" {
		t.Skip("set STACKPILOT_RUNNER_INTEGRATION=1 to verify the installed Windows toolchain")
	}
	resolver, err := NewResolver(Config{})
	if err != nil {
		t.Fatalf("NewResolver() error = %v", err)
	}
	root := t.TempDir()
	working := filepath.Join(root, "service")
	if err := os.Mkdir(working, 0o700); err != nil {
		t.Fatalf("create integration working directory: %v", err)
	}
	expected := map[Kind]string{Maven: "3.9.14", NPM: "11.12.1", Java: "21.0.10", Go: "1.26.6"}
	for kind, version := range expected {
		t.Run(string(kind), func(t *testing.T) {
			resolved, err := resolver.Resolve(context.Background(), ResolveRequest{
				Runner: kind, WorkspaceRoot: root, WorkingDirectory: working,
			})
			if err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}
			if resolved.Version != version {
				t.Fatalf("version = %q, want locked %q", resolved.Version, version)
			}
		})
	}
}
