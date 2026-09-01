package revision

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"

	"stackpilot/internal/domain"
)

// Canonicalize validates, sorts, and hashes a revision snapshot deterministically.
func Canonicalize(snapshot Snapshot) ([]byte, string, error) {
	normalizeSnapshot(&snapshot)
	if err := validateSnapshot(snapshot); err != nil {
		return nil, "", err
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return nil, "", fmt.Errorf("encode revision snapshot: %w", err)
	}
	if len(encoded) > MaxSnapshotBytes {
		return nil, "", ErrSourceTooLarge
	}
	digest := sha256.Sum256(encoded)
	return encoded, hex.EncodeToString(digest[:]), nil
}

func normalizeSnapshot(snapshot *Snapshot) {
	if snapshot.SchemaVersion == "" {
		snapshot.SchemaVersion = SchemaVersion
	}
	if snapshot.Files == nil {
		snapshot.Files = []FileFact{}
	}
	if snapshot.Services == nil {
		snapshot.Services = []ServiceFact{}
	}
	if snapshot.Ports == nil {
		snapshot.Ports = []PortFact{}
	}
	if snapshot.Runners == nil {
		snapshot.Runners = []RunnerFact{}
	}
	if snapshot.Secrets == nil {
		snapshot.Secrets = []SecretFact{}
	}
	sort.Slice(snapshot.Files, func(i, j int) bool { return snapshot.Files[i].Path < snapshot.Files[j].Path })
	sort.Slice(snapshot.Ports, func(i, j int) bool { return snapshot.Ports[i].Name < snapshot.Ports[j].Name })
	sort.Slice(snapshot.Services, func(i, j int) bool { return snapshot.Services[i].ServiceID < snapshot.Services[j].ServiceID })
	for index := range snapshot.Services {
		sort.Slice(snapshot.Services[index].Dependencies, func(i, j int) bool {
			return snapshot.Services[index].Dependencies[i].ServiceID < snapshot.Services[index].Dependencies[j].ServiceID
		})
		sort.Slice(snapshot.Services[index].Images, func(i, j int) bool {
			return snapshot.Services[index].Images[i].ComposeService < snapshot.Services[index].Images[j].ComposeService
		})
	}
	sort.Slice(snapshot.Runners, func(i, j int) bool { return snapshot.Runners[i].ServiceID < snapshot.Runners[j].ServiceID })
	sort.Slice(snapshot.Secrets, func(i, j int) bool {
		left, right := snapshot.Secrets[i], snapshot.Secrets[j]
		if left.ServiceID != right.ServiceID {
			return left.ServiceID < right.ServiceID
		}
		return left.EnvironmentName < right.EnvironmentName
	})
}

func validateSnapshot(snapshot Snapshot) error {
	if snapshot.SchemaVersion != SchemaVersion || snapshot.Kind.Validate() != nil {
		return ErrInvalidInput
	}
	if _, err := domainIDs(snapshot); err != nil || !validDigest(snapshot.ManifestDigest) {
		return ErrInvalidInput
	}
	if snapshot.Kind == "running" {
		if snapshot.SystemInstanceID == nil || !validDigest(snapshot.ResolvedSpecDigest) {
			return ErrInvalidInput
		}
	} else if snapshot.SystemInstanceID != nil || snapshot.ResolvedSpecDigest != "" {
		return ErrInvalidInput
	}
	return validateFacts(snapshot)
}

func domainIDs(snapshot Snapshot) (bool, error) {
	if _, err := domain.ParseWorkspaceID(snapshot.WorkspaceID.String()); err != nil {
		return false, err
	}
	if _, err := domain.ParseSystemID(snapshot.SystemID.String()); err != nil {
		return false, err
	}
	if snapshot.SystemInstanceID != nil {
		_, err := domain.ParseSystemInstanceID(snapshot.SystemInstanceID.String())
		return err == nil, err
	}
	return true, nil
}

func validateFacts(snapshot Snapshot) error {
	for _, file := range snapshot.Files {
		if !safeRelativePath(file.Path) || file.Kind == "" || file.Size < 0 || !validDigest(file.Digest) {
			return ErrInvalidInput
		}
	}
	for _, port := range snapshot.Ports {
		if _, err := domain.ParseServiceID(port.Name); err != nil || port.Protocol == "" || port.ConflictPolicy == "" || port.Exposure == "" ||
			port.Preferred != nil && (*port.Preferred < 1024 || *port.Preferred > 65535) || len(port.FallbackRange) > 32 {
			return ErrInvalidInput
		}
	}
	for _, service := range snapshot.Services {
		if _, err := domain.ParseServiceID(service.ServiceID.String()); err != nil {
			return ErrInvalidInput
		}
		if service.Driver.Validate() != nil || service.Mode.Validate() != nil || service.HealthCoverage.Validate() != nil {
			return ErrInvalidInput
		}
		if service.State != "" && service.State.Validate() != nil {
			return ErrInvalidInput
		}
		for _, dependency := range service.Dependencies {
			if _, err := domain.ParseServiceID(dependency.ServiceID.String()); err != nil || dependency.Condition.Validate() != nil {
				return ErrInvalidInput
			}
		}
		for _, image := range service.Images {
			if image.ComposeService == "" || !validSourceStatus(image.Status) || image.ReferenceDigest != "" && !validDigest(image.ReferenceDigest) || image.ImageDigest != "" && !validDigest(image.ImageDigest) {
				return ErrInvalidInput
			}
		}
		for _, digest := range []string{service.DefinitionDigest, service.CommandDigest, service.ComposeDigest} {
			if digest != "" && !validDigest(digest) {
				return ErrInvalidInput
			}
		}
	}
	for _, runner := range snapshot.Runners {
		if _, err := domain.ParseServiceID(runner.ServiceID.String()); err != nil || !validSourceStatus(runner.Status) ||
			runner.ExecutableDigest != "" && !validDigest(runner.ExecutableDigest) {
			return ErrInvalidInput
		}
	}
	if !validSourceStatus(snapshot.Git.Status) || len(snapshot.Git.Revision) > 128 || len(snapshot.Git.Branch) > 256 || len(snapshot.Git.Reason) > 128 {
		return ErrInvalidInput
	}
	return nil
}

func validSourceStatus(value SourceStatus) bool {
	return value == SourceAvailable || value == SourceUnavailable || value == SourceNotRepo || value == SourceUnsafe
}

func safeRelativePath(value string) bool {
	if value == "" || strings.Contains(value, "\\") || strings.HasPrefix(value, "/") {
		return false
	}
	clean := path.Clean(value)
	return clean == value && clean != "." && clean != ".." && !strings.HasPrefix(clean, "../")
}

func validDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
