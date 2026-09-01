package revision

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestGitProbeReportsCleanDirtyAndNonRepository(t *testing.T) {
	git, err := exec.LookPath("git.exe")
	if err != nil {
		git, err = exec.LookPath("git")
	}
	if err != nil {
		t.Skip("git executable is unavailable")
	}
	probe, err := NewGitProbe(git)
	if err != nil || probe.executable == "" {
		t.Skipf("trusted git probe is unavailable: %v", err)
	}
	root := filepath.Join(t.TempDir(), "workspace 中文")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("create Git fixture: %v", err)
	}
	runGitFixture(t, git, root, "init")
	writeRevisionFixture(t, root, "tracked.txt", "initial")
	runGitFixture(t, git, root, "add", "tracked.txt")
	runGitFixture(t, git, root, "-c", "user.name=StackPilot Test", "-c", "user.email=test@example.invalid", "commit", "-m", "fixture")
	clean := probe.Collect(context.Background(), root)
	if clean.Status != SourceAvailable || clean.Dirty || clean.Revision == "" {
		t.Fatalf("clean Git fact = %#v", clean)
	}
	writeRevisionFixture(t, root, "tracked.txt", "changed")
	dirty := probe.Collect(context.Background(), root)
	if dirty.Status != SourceAvailable || !dirty.Dirty {
		t.Fatalf("dirty Git fact = %#v", dirty)
	}
	nonRepo := probe.Collect(context.Background(), t.TempDir())
	if nonRepo.Status != SourceNotRepo {
		t.Fatalf("non-repository Git fact = %#v", nonRepo)
	}
}

func runGitFixture(t *testing.T, executable, root string, arguments ...string) {
	t.Helper()
	command := exec.Command(executable, arguments...)
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", arguments, err, output)
	}
}
