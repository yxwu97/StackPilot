package buildinfo

import "testing"

func TestCurrentUsesCompiledValues(t *testing.T) {
	originalVersion, originalCommit, originalBuildTime := Version, Commit, BuildTime
	t.Cleanup(func() {
		Version, Commit, BuildTime = originalVersion, originalCommit, originalBuildTime
	})

	Version = "1.2.3"
	Commit = "abc123"
	BuildTime = "2026-08-17T12:00:00Z"

	got := Current()
	if got.Version != Version || got.Commit != Commit || got.BuildTime != BuildTime {
		t.Fatalf("Current() = %#v, want compiled values", got)
	}
}
