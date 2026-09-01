package revision

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestFileCollectorReadsOnlyWhitelistAndExplicitFiles(t *testing.T) {
	root := t.TempDir()
	writeRevisionFixture(t, root, "package.json", `{}`)
	writeRevisionFixture(t, root, ".env", "DO_NOT_READ=secret")
	writeRevisionFixture(t, root, "compose.yaml", "services: {}")
	facts, err := NewFileCollector().Collect(context.Background(), root, []string{"compose.yaml"})
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	if len(facts) != 2 || facts[0].Path != "compose.yaml" || facts[1].Path != "package.json" {
		t.Fatalf("facts = %#v", facts)
	}
	for _, fact := range facts {
		if fact.Path == ".env" {
			t.Fatal(".env was collected")
		}
	}
}

func TestFileCollectorRejectsEscapeAndLimit(t *testing.T) {
	root := t.TempDir()
	collector := NewFileCollector()
	if _, err := collector.Collect(context.Background(), root, []string{"../outside"}); !errors.Is(err, ErrSourceUnsafe) {
		t.Fatalf("escape error = %v", err)
	}
	writeRevisionFixture(t, root, "package.json", "12345")
	collector.MaxFileBytes = 4
	if _, err := collector.Collect(context.Background(), root, nil); !errors.Is(err, ErrSourceTooLarge) {
		t.Fatalf("large file error = %v", err)
	}
}

func writeRevisionFixture(t *testing.T, root, relative, value string) {
	t.Helper()
	path := filepath.Join(root, relative)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create fixture directory: %v", err)
	}
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
}
