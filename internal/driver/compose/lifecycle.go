package compose

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"stackpilot/internal/domain"
)

const (
	maximumProjectNameLength = 63
	projectIDShortLength     = 8
	defaultStartTimeout      = 5 * time.Minute
	maximumStartTimeout      = 30 * time.Minute
	defaultStopTimeout       = 30 * time.Second
	maximumStopTimeout       = 5 * time.Minute
	defaultInspectTimeout    = 30 * time.Second
)

type composePSRow struct {
	ID       string `json:"ID"`
	Name     string `json:"Name"`
	Project  string `json:"Project"`
	Service  string `json:"Service"`
	State    string `json:"State"`
	Health   string `json:"Health"`
	ExitCode int    `json:"ExitCode"`
}

// ProjectName returns the deterministic bounded Compose project identity.
func ProjectName(systemID domain.SystemID, workspaceID domain.WorkspaceID, instanceID domain.SystemInstanceID) (string, error) {
	if _, err := domain.ParseSystemID(systemID.String()); err != nil {
		return "", ErrLifecycleInvalid
	}
	if _, err := domain.ParseWorkspaceID(workspaceID.String()); err != nil {
		return "", ErrLifecycleInvalid
	}
	if _, err := domain.ParseSystemInstanceID(instanceID.String()); err != nil {
		return "", ErrLifecycleInvalid
	}
	workspace := shortRuntimeID(workspaceID.String())
	instance := shortRuntimeID(instanceID.String())
	suffix := "-" + workspace + "-" + instance
	budget := maximumProjectNameLength - len("sp-") - len(suffix)
	system := boundedSystemID(systemID.String(), budget)
	return "sp-" + system + suffix, nil
}

func shortRuntimeID(value string) string {
	_, suffix, _ := strings.Cut(value, "_")
	return strings.ToLower(suffix[:projectIDShortLength])
}

func boundedSystemID(value string, budget int) string {
	if len(value) <= budget {
		return value
	}
	digest := sha256.Sum256([]byte(value))
	hash := hex.EncodeToString(digest[:])[:10]
	return value[:budget-len(hash)-1] + "-" + hash
}

func normalizedLifecycleRequest(request LifecycleRequest) LifecycleRequest {
	request.Services = append([]string(nil), request.Services...)
	sort.Strings(request.Services)
	request.BuildServices = append([]string(nil), request.BuildServices...)
	sort.Strings(request.BuildServices)
	request.Readiness = cloneRequirements(request.Readiness, request.Services)
	if request.BuildPolicy == "" {
		request.BuildPolicy = "never"
	}
	if request.StartTimeout <= 0 {
		request.StartTimeout = defaultStartTimeout
	}
	if request.StopTimeout <= 0 {
		request.StopTimeout = defaultStopTimeout
	}
	return request
}

func validateLifecycleDurations(request LifecycleRequest) error {
	if request.StartTimeout <= 0 || request.StartTimeout > maximumStartTimeout ||
		request.StopTimeout <= 0 || request.StopTimeout > maximumStopTimeout {
		return ErrLifecycleInvalid
	}
	return nil
}

func newProjectIdentity(request LifecycleRequest) (ProjectIdentity, error) {
	projectName, err := ProjectName(request.SystemID, request.WorkspaceID, request.InstanceID)
	if err != nil {
		return ProjectIdentity{}, err
	}
	identity := ProjectIdentity{
		ProjectName: projectName, SystemID: request.SystemID, WorkspaceID: request.WorkspaceID, InstanceID: request.InstanceID,
		WorkspaceRoot: request.WorkspaceRoot, DataDir: request.DataDir, ComposeFile: request.ComposeFile,
		OverrideFile: request.OverrideFile, Services: append([]string(nil), request.Services...),
		StartTimeout: request.StartTimeout, StopTimeout: request.StopTimeout,
		BuildPolicy: request.BuildPolicy, BuildServices: append([]string(nil), request.BuildServices...), Readiness: cloneRequirements(request.Readiness, request.Services),
	}
	digest, err := projectIdentityDigest(identity)
	identity.DefinitionDigest = digest
	return identity, err
}

func projectIdentityDigest(identity ProjectIdentity) (string, error) {
	identity.DefinitionDigest = ""
	encoded, err := json.Marshal(identity)
	if err != nil {
		return "", ErrLifecycleInvalid
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func verifyProjectIdentity(identity ProjectIdentity) error {
	expectedName, err := ProjectName(identity.SystemID, identity.WorkspaceID, identity.InstanceID)
	if err != nil || identity.ProjectName != expectedName || len(identity.Services) == 0 {
		return ErrProjectIdentityMismatch
	}
	digest, err := projectIdentityDigest(identity)
	if err != nil || digest != identity.DefinitionDigest {
		return ErrProjectIdentityMismatch
	}
	return nil
}

// EncodeProjectIdentity serializes a verified non-sensitive identity for health/recovery adapters.
func EncodeProjectIdentity(identity ProjectIdentity) (string, error) {
	if err := verifyProjectIdentity(identity); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(identity)
	if err != nil {
		return "", ErrProjectIdentityMismatch
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}

// DecodeProjectIdentity strictly decodes and verifies a persisted project identity.
func DecodeProjectIdentity(value string) (ProjectIdentity, error) {
	encoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(encoded) > 64*1024 {
		return ProjectIdentity{}, ErrProjectIdentityMismatch
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var identity ProjectIdentity
	if err := decoder.Decode(&identity); err != nil {
		return ProjectIdentity{}, ErrProjectIdentityMismatch
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return ProjectIdentity{}, ErrProjectIdentityMismatch
	}
	if err := verifyProjectIdentity(identity); err != nil {
		return ProjectIdentity{}, err
	}
	return identity, nil
}

func decodeComposePS(contents []byte) ([]composePSRow, error) {
	trimmed := strings.TrimSpace(string(contents))
	if trimmed == "" {
		return nil, nil
	}
	if strings.HasPrefix(trimmed, "[") {
		var rows []composePSRow
		if err := json.Unmarshal([]byte(trimmed), &rows); err != nil {
			return nil, ErrComposeInspectFailed
		}
		return rows, nil
	}
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	rows := make([]composePSRow, 0)
	for {
		var row composePSRow
		if err := decoder.Decode(&row); errors.Is(err, io.EOF) {
			return rows, nil
		} else if err != nil {
			return nil, ErrComposeInspectFailed
		}
		rows = append(rows, row)
	}
}

func buildProjectObservation(identity ProjectIdentity, rows []composePSRow) (ProjectObservation, error) {
	if len(rows) == 0 {
		return ProjectObservation{}, ErrProjectNotFound
	}
	services := serviceSet(identity.Services)
	found := make(map[string]bool, len(services))
	containers := make([]ContainerObservation, 0, len(rows))
	for _, row := range rows {
		if row.Project != "" && row.Project != identity.ProjectName {
			return ProjectObservation{}, ErrProjectIdentityMismatch
		}
		if _, exists := services[row.Service]; !exists || row.ID == "" || row.Name == "" {
			return ProjectObservation{}, ErrProjectIdentityMismatch
		}
		found[row.Service] = true
		containers = append(containers, ContainerObservation{ID: row.ID, Name: row.Name, Service: row.Service, State: strings.ToLower(row.State), Health: strings.ToLower(row.Health), ExitCode: row.ExitCode})
	}
	for service := range services {
		if !found[service] {
			return ProjectObservation{}, ErrProjectNotFound
		}
	}
	sort.Slice(containers, func(left, right int) bool { return containers[left].Name < containers[right].Name })
	return ProjectObservation{State: aggregateProjectState(containers), Containers: containers}, nil
}

func aggregateProjectState(containers []ContainerObservation) string {
	running, stopped := true, true
	for _, container := range containers {
		if container.State != "running" {
			running = false
		}
		if container.State != "exited" && container.State != "stopped" && container.State != "dead" {
			stopped = false
		}
	}
	if running {
		return "running"
	}
	if stopped {
		return "stopped"
	}
	return "degraded"
}

// CheckHealth applies the persisted per-service readiness requirements.
func (lifecycle *Lifecycle) CheckHealth(ctx context.Context, identity ProjectIdentity) HealthObservation {
	started := time.Now()
	result := HealthObservation{CheckedAt: started.UTC()}
	observation, err := lifecycle.Inspect(ctx, identity)
	result.Duration = time.Since(started)
	if err != nil {
		result.ErrorCode, result.Summary = "CONTAINER_UNHEALTHY", "Compose project health could not be verified"
		return result
	}
	ready := 0
	for _, container := range observation.Containers {
		requirement := identity.Readiness[container.Service]
		if container.State == "running" && (requirement == "running" || container.Health == "healthy") {
			ready++
		}
	}
	result.Ready = ready == len(observation.Containers) && ready > 0
	if result.Ready {
		result.Summary = "All managed Compose containers are healthy"
		return result
	}
	result.ErrorCode = "CONTAINER_UNHEALTHY"
	result.Summary = "One or more managed Compose containers are not healthy"
	return result
}

func cloneRequirements(source map[string]string, services []string) map[string]string {
	result := make(map[string]string, len(services))
	for _, service := range services {
		result[service] = "healthy"
	}
	for service, requirement := range source {
		result[service] = requirement
	}
	return result
}

// CheckCompose implements the generic Health Engine Compose adapter.
func (lifecycle *Lifecycle) CheckCompose(ctx context.Context, encodedIdentity string) (bool, string, error) {
	identity, err := DecodeProjectIdentity(encodedIdentity)
	if err != nil {
		return false, "Compose project identity is invalid", err
	}
	result := lifecycle.CheckHealth(ctx, identity)
	return result.Ready, result.Summary, nil
}

func durationSeconds(value time.Duration) string {
	seconds := int64((value + time.Second - 1) / time.Second)
	return strconv.FormatInt(seconds, 10)
}

func readOverrideDocument(path string) (overrideDocument, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return overrideDocument{}, ErrLifecycleInvalid
	}
	var document overrideDocument
	if err := decodeStrictOverride(contents, &document); err != nil {
		return overrideDocument{}, err
	}
	return document, nil
}
