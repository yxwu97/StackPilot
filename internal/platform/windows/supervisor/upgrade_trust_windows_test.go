//go:build windows

package supervisor

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTrustedInstalledControlAcceptsOnlyMarkerSelectedChecksumVersion(t *testing.T) {
	root := t.TempDir()
	current := filepath.Join(root, "versions", strings.Repeat("a", 64), "stackpilot.exe")
	if err := os.MkdirAll(filepath.Dir(current), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(current, []byte("current"), 0o700); err != nil {
		t.Fatal(err)
	}
	candidatePayload := []byte("candidate")
	candidateDigest := digestBytes(candidatePayload)
	candidate := filepath.Join(root, "versions", candidateDigest, "stackpilot.exe")
	if err := os.MkdirAll(filepath.Dir(candidate), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(candidate, candidatePayload, 0o700); err != nil {
		t.Fatal(err)
	}
	writeUpgradeMarker(t, root, candidate, candidateDigest)
	if !trustedInstalledControl(current, candidate) {
		t.Fatal("marker-selected checksum version was rejected")
	}
	if err := os.WriteFile(candidate, []byte("tampered"), 0o700); err != nil {
		t.Fatal(err)
	}
	if trustedInstalledControl(current, candidate) {
		t.Fatal("tampered candidate was trusted")
	}
}

func TestTrustedInstalledControlRejectsUnregisteredAndEscapedVersions(t *testing.T) {
	root := t.TempDir()
	current := filepath.Join(root, "versions", strings.Repeat("a", 64), "stackpilot.exe")
	if err := os.MkdirAll(filepath.Dir(current), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(current, []byte("current"), 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "stackpilot.exe")
	if err := os.WriteFile(outside, []byte("outside"), 0o700); err != nil {
		t.Fatal(err)
	}
	if trustedInstalledControl(current, outside) {
		t.Fatal("outside executable was trusted")
	}
	digest := digestBytes([]byte("unregistered"))
	unregistered := filepath.Join(root, "versions", digest, "stackpilot.exe")
	if err := os.MkdirAll(filepath.Dir(unregistered), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unregistered, []byte("unregistered"), 0o700); err != nil {
		t.Fatal(err)
	}
	if trustedInstalledControl(current, unregistered) {
		t.Fatal("unregistered version was trusted")
	}
}

func TestInstallationMarkerRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "installation.json")
	if err := os.WriteFile(path, []byte(`{"schemaVersion":1,"unexpected":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readInstallationMarker(path); err == nil {
		t.Fatal("unknown installation marker field was accepted")
	}
}

func writeUpgradeMarker(t *testing.T, root, executable, digest string) {
	t.Helper()
	marker := installationMarker{
		SchemaVersion: 1, Mode: "user-process", InstallationID: strings.Repeat("b", 32),
		InstallDir: root, ExecutablePath: executable, SHA256: digest,
	}
	payload, err := json.Marshal(marker)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "installation.json"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
}

func digestBytes(payload []byte) string {
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}
