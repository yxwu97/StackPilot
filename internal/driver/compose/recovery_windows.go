//go:build windows

package compose

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"

	"stackpilot/internal/domain"
)

const maximumDiscoveredContainers = 64

var containerIDPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

type dockerInspectRecord struct {
	ID     string `json:"Id"`
	Name   string `json:"Name"`
	Config struct {
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
	State struct {
		Status   string `json:"Status"`
		ExitCode int    `json:"ExitCode"`
		Health   *struct {
			Status string `json:"Status"`
		} `json:"Health"`
	} `json:"State"`
}

// Recover verifies a persisted project identity and reattaches to its structured observation.
func (lifecycle *Lifecycle) Recover(ctx context.Context, encodedIdentity string) (ProjectIdentity, ProjectObservation, error) {
	identity, err := DecodeProjectIdentity(encodedIdentity)
	if err != nil {
		return ProjectIdentity{}, ProjectObservation{}, err
	}
	observation, err := lifecycle.Inspect(ctx, identity)
	return identity, observation, err
}

// Discover rebuilds identity in the process-created/database-commit crash window using fixed labels.
func (lifecycle *Lifecycle) Discover(ctx context.Context, request LifecycleRequest) (ProjectIdentity, ProjectObservation, error) {
	identity, err := lifecycle.Prepare(ctx, request)
	if err != nil {
		return ProjectIdentity{}, ProjectObservation{}, err
	}
	docker, _, err := lifecycle.validateIdentity(identity)
	if err != nil {
		return ProjectIdentity{}, ProjectObservation{}, err
	}
	discoveryContext, cancel := context.WithTimeout(ctx, defaultInspectTimeout)
	defer cancel()
	ids, err := lifecycle.discoverContainerIDs(discoveryContext, docker, identity)
	if err != nil {
		return ProjectIdentity{}, ProjectObservation{}, err
	}
	rows, err := lifecycle.inspectDiscoveredContainers(discoveryContext, docker, identity, ids)
	if err != nil {
		return ProjectIdentity{}, ProjectObservation{}, err
	}
	observation, err := buildProjectObservation(identity, rows)
	return identity, observation, err
}

func (lifecycle *Lifecycle) discoverContainerIDs(ctx context.Context, docker string, identity ProjectIdentity) ([]string, error) {
	arguments := []string{"ps", "--all", "--quiet", "--no-trunc"}
	for _, filter := range discoveryFilters(identity) {
		arguments = append(arguments, "--filter", filter)
	}
	output, err := lifecycle.run(ctx, docker, arguments, identity.WorkspaceRoot, lifecycle.environment)
	if err != nil {
		return nil, lifecycleCommandFailure(ctx, ErrDiscoveryFailed)
	}
	ids := strings.Fields(string(output.Stdout))
	if len(ids) == 0 {
		return nil, ErrProjectNotFound
	}
	if len(ids) > maximumDiscoveredContainers {
		return nil, ErrDiscoveryFailed
	}
	for _, id := range ids {
		if !containerIDPattern.MatchString(id) {
			return nil, ErrDiscoveryFailed
		}
	}
	return ids, nil
}

func discoveryFilters(identity ProjectIdentity) []string {
	return []string{
		"label=stackpilot.system=" + identity.SystemID.String(),
		"label=stackpilot.workspace=" + identity.WorkspaceID.String(),
		"label=stackpilot.instance=" + identity.InstanceID.String(),
	}
}

func (lifecycle *Lifecycle) inspectDiscoveredContainers(ctx context.Context, docker string, identity ProjectIdentity, ids []string) ([]composePSRow, error) {
	arguments := append([]string{"inspect"}, ids...)
	output, err := lifecycle.run(ctx, docker, arguments, identity.WorkspaceRoot, lifecycle.environment)
	if err != nil {
		return nil, lifecycleCommandFailure(ctx, ErrDiscoveryFailed)
	}
	var records []dockerInspectRecord
	if json.Unmarshal(output.Stdout, &records) != nil || len(records) != len(ids) {
		return nil, ErrDiscoveryFailed
	}
	rows := make([]composePSRow, 0, len(records))
	for _, record := range records {
		row, err := discoveredRow(identity, record)
		if err != nil {
			return nil, err
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func discoveredRow(identity ProjectIdentity, record dockerInspectRecord) (composePSRow, error) {
	labels := record.Config.Labels
	service := labels["stackpilot.service"]
	if record.ID == "" || labels["stackpilot.system"] != identity.SystemID.String() ||
		labels["stackpilot.workspace"] != identity.WorkspaceID.String() || labels["stackpilot.instance"] != identity.InstanceID.String() ||
		labels["com.docker.compose.project"] != identity.ProjectName || labels["com.docker.compose.service"] != service {
		return composePSRow{}, ErrProjectIdentityMismatch
	}
	if _, err := domain.ParseServiceID(service); err != nil {
		return composePSRow{}, ErrProjectIdentityMismatch
	}
	health := ""
	if record.State.Health != nil {
		health = record.State.Health.Status
	}
	return composePSRow{
		ID: record.ID, Name: strings.TrimPrefix(record.Name, "/"), Project: identity.ProjectName,
		Service: service, State: record.State.Status, Health: health, ExitCode: record.State.ExitCode,
	}, nil
}
