package manifest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoaderAcceptsManifestExamples(t *testing.T) {
	loader := newTestLoader(t)
	paths, err := filepath.Glob(filepath.Join("..", "..", "schemas", "examples", "*.yaml"))
	if err != nil {
		t.Fatalf("list examples: %v", err)
	}
	if len(paths) < 2 {
		t.Fatalf("example count = %d, want at least 2", len(paths))
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			document, err := loader.Load(context.Background(), path)
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if document.Manifest.APIVersion != "stackpilot.io/v1alpha1" || len(document.Manifest.Spec.Services) == 0 || len(document.JSON) == 0 {
				t.Fatalf("document = %#v, want typed and normalized manifest", document)
			}
		})
	}
}

func TestLoaderRejectsUnsafeYAMLShapes(t *testing.T) {
	loader := newTestLoader(t)
	tests := []struct {
		name     string
		contents string
		want     error
	}{
		{name: "duplicate root key", contents: validManifest() + "kind: System\n", want: ErrDuplicateKey},
		{name: "duplicate nested key", contents: strings.Replace(validManifest(), "      driver: process", "      driver: process\n      driver: process", 1), want: ErrDuplicateKey},
		{name: "unknown root field", contents: validManifest() + "command: arbitrary\n", want: ErrUnknownField},
		{name: "unknown service field", contents: strings.Replace(validManifest(), "      arguments: []", "      arguments: []\n      command: arbitrary", 1), want: ErrUnknownField},
		{name: "multiple documents", contents: validManifest() + "---\n" + validManifest(), want: ErrMultipleDocuments},
		{name: "malformed YAML", contents: "apiVersion: [", want: ErrMalformedYAML},
		{name: "schema invalid", contents: "apiVersion: stackpilot.io/v1alpha1\nkind: System\nmetadata:\n  id: example\n  name: Example\nspec:\n  services: {}\n", want: ErrSchemaInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := loader.Parse([]byte(test.contents))
			if !errors.Is(err, test.want) {
				t.Fatalf("Parse() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestLoaderEnforcesFixedFileSize(t *testing.T) {
	loader := newTestLoader(t)
	base := validManifest()
	padding := strings.Repeat("#", MaxFileSize-len(base)-1) + "\n"
	if _, err := loader.Parse([]byte(base + padding)); err != nil {
		t.Fatalf("Parse(maximum size) error = %v", err)
	}
	if _, err := loader.Parse([]byte(base + padding + "#")); !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("Parse(oversized) error = %v, want ErrFileTooLarge", err)
	}

	path := filepath.Join(t.TempDir(), "system.yaml")
	if err := os.WriteFile(path, []byte(base+padding+"#"), 0o600); err != nil {
		t.Fatalf("write oversized fixture: %v", err)
	}
	if _, err := loader.Load(context.Background(), path); !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("Load(oversized) error = %v, want ErrFileTooLarge", err)
	}
}

func TestLoaderRejectsNonFileAndCancelledContext(t *testing.T) {
	loader := newTestLoader(t)
	if _, err := loader.Load(context.Background(), t.TempDir()); !errors.Is(err, ErrNotRegularFile) {
		t.Fatalf("Load(directory) error = %v, want ErrNotRegularFile", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := loader.Load(ctx, "unused"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Load(cancelled) error = %v, want context cancellation", err)
	}
}

func TestValidationErrorDoesNotExposeManifestValue(t *testing.T) {
	loader := newTestLoader(t)
	contents := strings.Replace(validManifest(), "      arguments: []", "      arguments: []\n      secretValue: do-not-expose", 1)
	_, err := loader.Parse([]byte(contents))
	if err == nil || strings.Contains(err.Error(), "do-not-expose") {
		t.Fatalf("safe validation error = %q", err)
	}
}

func newTestLoader(t *testing.T) *Loader {
	t.Helper()
	loader, err := NewLoader()
	if err != nil {
		t.Fatalf("NewLoader() error = %v", err)
	}
	return loader
}

func validManifest() string {
	return "apiVersion: stackpilot.io/v1alpha1\n" +
		"kind: System\n" +
		"metadata:\n  id: example\n  name: Example\n" +
		"spec:\n  services:\n    app:\n" +
		"      driver: process\n      runner: java\n" +
		"      workingDirectory: ./app\n      arguments: []\n" +
		"      readiness:\n        type: process\n"
}
