package capability

import (
	"slices"
	"testing"
)

func TestPublishedCapabilitiesAreSortedAndKeepUnreleasedPhase3Closed(t *testing.T) {
	t.Parallel()
	got := Published()
	if !slices.IsSorted(got) {
		t.Fatalf("Published() = %#v, want sorted", got)
	}
	for _, name := range []string{Phase3ResourceMonitoring, Phase3ChangePlanning} {
		if !slices.Contains(got, name) {
			t.Fatalf("released capability %q is not published", name)
		}
	}
	for _, name := range []string{Phase3VerifiedRestart} {
		if !Known(name) {
			t.Fatalf("%q is not registered", name)
		}
		if slices.Contains(got, name) {
			t.Fatalf("unreleased capability %q was published", name)
		}
	}
}

func TestPublishedManifestAliasesMatchExecutableFeatures(t *testing.T) {
	t.Parallel()
	want := []string{"auto-restart", "compose", "compose-build", "go", "liveness"}
	if got := PublishedManifestAliases(); !slices.Equal(got, want) {
		t.Fatalf("PublishedManifestAliases() = %#v, want %#v", got, want)
	}
}
