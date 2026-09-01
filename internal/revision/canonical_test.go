package revision

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"stackpilot/internal/domain"
)

func TestCanonicalizeIsStableAndSortsFacts(t *testing.T) {
	snapshot := validWorkspaceSnapshot()
	snapshot.Files = []FileFact{
		{Path: "z/package.json", Kind: "node", Digest: digestOf("z"), Size: 1},
		{Path: "a/go.mod", Kind: "go", Digest: digestOf("a"), Size: 1},
	}
	firstPort, secondPort := 32101, 32100
	snapshot.Ports = []PortFact{
		{Name: "web", Protocol: "tcp", Preferred: &firstPort, ConflictPolicy: "strict", Exposure: "loopback"},
		{Name: "api", Protocol: "tcp", Preferred: &secondPort, ConflictPolicy: "strict", Exposure: "loopback"},
	}
	snapshot.Services = []ServiceFact{
		{ServiceID: "web", Driver: domain.DriverProcess, Mode: domain.ProcessDaemon, HealthCoverage: domain.HealthCoverageBusiness, RestartPolicy: "never"},
		{ServiceID: "api", Driver: domain.DriverProcess, Mode: domain.ProcessDaemon, HealthCoverage: domain.HealthCoverageBusiness, RestartPolicy: "never"},
	}
	first, firstDigest, err := Canonicalize(snapshot)
	if err != nil {
		t.Fatalf("Canonicalize() error = %v", err)
	}
	second, secondDigest, err := Canonicalize(snapshot)
	if err != nil {
		t.Fatalf("Canonicalize(replay) error = %v", err)
	}
	if firstDigest != secondDigest || !bytes.Equal(first, second) {
		t.Fatal("canonical revision changed for identical input")
	}
	if bytes.Index(first, []byte(`"path":"a/go.mod"`)) > bytes.Index(first, []byte(`"path":"z/package.json"`)) {
		t.Fatal("file facts were not sorted")
	}
	if bytes.Index(first, []byte(`"name":"api"`)) > bytes.Index(first, []byte(`"name":"web"`)) {
		t.Fatal("port facts were not sorted")
	}
}

func TestCanonicalizeRejectsRunningSnapshotWithoutInstance(t *testing.T) {
	snapshot := validWorkspaceSnapshot()
	snapshot.Kind = domain.RevisionRunning
	snapshot.ResolvedSpecDigest = digestOf("resolved")
	if _, _, err := Canonicalize(snapshot); err == nil {
		t.Fatal("running snapshot without instance unexpectedly accepted")
	}
}

func validWorkspaceSnapshot() Snapshot {
	return Snapshot{
		SchemaVersion:  SchemaVersion,
		WorkspaceID:    "ws_01ARZ3NDEKTSV4RRFFQ69G5FAV",
		SystemID:       "sample",
		Kind:           domain.RevisionWorkspace,
		ManifestDigest: digestOf("manifest"),
		Git:            GitFact{Status: SourceNotRepo, Reason: "GIT_NOT_REPOSITORY"},
	}
}

func digestOf(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
