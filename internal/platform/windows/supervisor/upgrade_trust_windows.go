//go:build windows

package supervisor

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"stackpilot/internal/security"
)

const installationMarkerLimit = 64 * 1024

type installationMarker struct {
	SchemaVersion  int       `json:"schemaVersion"`
	Mode           string    `json:"mode"`
	InstallationID string    `json:"installationId"`
	InstallDir     string    `json:"installDir"`
	DataDir        string    `json:"dataDir"`
	TaskName       string    `json:"taskName"`
	ExecutablePath string    `json:"executablePath"`
	Version        string    `json:"version"`
	SHA256         string    `json:"sha256"`
	Port           int       `json:"port"`
	InstalledAt    time.Time `json:"installedAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

func trustedInstalledControl(currentExecutable, candidateExecutable string) bool {
	installRoot, versionsRoot, ok := installedVersionRoot(currentExecutable)
	if !ok {
		return false
	}
	candidate, err := security.CanonicalExistingPath(candidateExecutable)
	if err != nil || !candidateVersionPath(versionsRoot, candidate) {
		return false
	}
	marker, err := readInstallationMarker(filepath.Join(installRoot, "installation.json"))
	if err != nil || !validInstallationMarker(marker) {
		return false
	}
	markerRoot, rootErr := security.CanonicalExistingPath(marker.InstallDir)
	markerExecutable, executableErr := security.CanonicalExistingPath(marker.ExecutablePath)
	if rootErr != nil || executableErr != nil || !strings.EqualFold(markerRoot, installRoot) || !strings.EqualFold(markerExecutable, candidate) {
		return false
	}
	digest, err := executableSHA256(candidate)
	return err == nil && strings.EqualFold(digest, marker.SHA256) && strings.EqualFold(filepath.Base(filepath.Dir(candidate)), digest)
}

func installedVersionRoot(executable string) (string, string, bool) {
	current, err := security.CanonicalExistingPath(executable)
	if err != nil || !strings.EqualFold(filepath.Base(current), "stackpilot.exe") {
		return "", "", false
	}
	versionDir := filepath.Dir(current)
	versionsRoot := filepath.Dir(versionDir)
	if !strings.EqualFold(filepath.Base(versionsRoot), "versions") || filepath.Dir(versionsRoot) == versionsRoot {
		return "", "", false
	}
	return filepath.Dir(versionsRoot), versionsRoot, true
}

func candidateVersionPath(versionsRoot, candidate string) bool {
	inside, err := security.PathWithinRoot(versionsRoot, candidate)
	return err == nil && inside && strings.EqualFold(filepath.Base(candidate), "stackpilot.exe") &&
		strings.EqualFold(filepath.Dir(filepath.Dir(candidate)), versionsRoot)
}

func readInstallationMarker(path string) (installationMarker, error) {
	file, err := os.Open(path)
	if err != nil {
		return installationMarker{}, err
	}
	defer file.Close()
	payload, err := io.ReadAll(io.LimitReader(file, installationMarkerLimit+1))
	if err != nil || len(payload) > installationMarkerLimit {
		return installationMarker{}, fmt.Errorf("installation marker is unreadable")
	}
	var marker installationMarker
	if err := strictJSON(payload, &marker); err != nil {
		return installationMarker{}, err
	}
	return marker, nil
}

func validInstallationMarker(marker installationMarker) bool {
	return marker.SchemaVersion == 1 && marker.Mode == "user-process" && validHex(marker.InstallationID, 32) && validHex(marker.SHA256, 64)
}

func executableSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func validHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
