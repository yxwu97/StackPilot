//go:build windows

package manifest

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestValidatorRejectsJunctionDirectoryEscape(t *testing.T) {
	root, document := semanticFixture(t)
	outside := t.TempDir()
	junction := filepath.Join(root, "junction-outside")
	command := exec.CommandContext(context.Background(), "cmd.exe", "/d", "/s", "/c", "mklink", "/J", junction, outside)
	if output, err := command.CombinedOutput(); err != nil {
		t.Skipf("directory junctions are unavailable: %v (%s)", err, output)
	}
	t.Cleanup(func() {
		if err := os.Remove(junction); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Errorf("remove junction fixture: %v", err)
		}
	})
	backend := document.Manifest.Spec.Services["backend"]
	backend.WorkingDirectory = "./junction-outside"
	document.Manifest.Spec.Services["backend"] = backend
	_, err := NewValidator().Validate(context.Background(), document, root)
	if !errors.Is(err, ErrPathOutsideWorkspace) {
		t.Fatalf("Validate(junction escape) error = %v, want ErrPathOutsideWorkspace", err)
	}
}
