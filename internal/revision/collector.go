package revision

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"stackpilot/internal/domain"
	"stackpilot/internal/manifest"
	"stackpilot/internal/runner"
	"stackpilot/internal/security"
	"stackpilot/internal/workspace"
)

// WorkspaceSource supplies registered and freshly parsed workspace facts.
type WorkspaceSource interface {
	Get(context.Context, domain.WorkspaceID) (*workspace.Record, error)
	CurrentSnapshot(context.Context, string) (workspace.Snapshot, error)
	ManifestByDigest(context.Context, string) (workspace.ManifestView, error)
}

// RuntimeSource supplies persisted active runtime facts.
type RuntimeSource interface {
	GetActive(context.Context, domain.WorkspaceID) (*domain.SystemInstance, bool, error)
	ListServices(context.Context, domain.SystemInstanceID) ([]domain.ServiceInstance, error)
}

// ResolvedSpecSource supplies immutable launch-time resolved specifications.
type ResolvedSpecSource interface {
	LoadResolvedSpec(context.Context, string) ([]byte, error)
}

// SecretVersionSource supplies value-free Secret version metadata.
type SecretVersionSource interface {
	ListServiceSecretVersions(context.Context, domain.ServiceInstanceID) ([]security.ServiceSecretVersion, error)
}

// RunnerResolver resolves a trusted runner without starting a service.
type RunnerResolver interface {
	Resolve(context.Context, runner.ResolveRequest) (*runner.ResolvedCommand, error)
}

// CollectorConfig declares the bounded sources used for revision collection.
type CollectorConfig struct {
	Workspaces     WorkspaceSource
	Runtime        RuntimeSource
	ResolvedSpecs  ResolvedSpecSource
	SecretVersions SecretVersionSource
	Runners        RunnerResolver
	Git            *GitProbe
	Files          *FileCollector
}

// Collector builds running or workspace revision snapshots from trusted sources.
type Collector struct {
	config CollectorConfig
}

// NewCollector validates dependencies and constructs a revision collector.
func NewCollector(config CollectorConfig) (*Collector, error) {
	if config.Workspaces == nil || config.Runtime == nil || config.ResolvedSpecs == nil || config.SecretVersions == nil || config.Runners == nil {
		return nil, ErrInvalidInput
	}
	if config.Files == nil {
		config.Files = NewFileCollector()
	}
	return &Collector{config: config}, nil
}

// Collect builds one bounded snapshot without lifecycle or workspace write effects.
func (collector *Collector) Collect(ctx context.Context, workspaceID domain.WorkspaceID, kind domain.RevisionKind) (Snapshot, error) {
	if _, err := domain.ParseWorkspaceID(workspaceID.String()); err != nil || kind.Validate() != nil {
		return Snapshot{}, ErrInvalidInput
	}
	if kind == domain.RevisionRunning {
		return collector.collectRunning(ctx, workspaceID)
	}
	return collector.collectWorkspace(ctx, workspaceID)
}

func (collector *Collector) collectWorkspace(ctx context.Context, workspaceID domain.WorkspaceID) (Snapshot, error) {
	record, err := collector.config.Workspaces.Get(ctx, workspaceID)
	if err != nil {
		return Snapshot{}, fmt.Errorf("load revision workspace: %w", err)
	}
	before, value, err := collector.readCurrentManifest(ctx, record)
	if err != nil {
		return Snapshot{}, err
	}
	services, runners, explicit, err := collector.workspaceFacts(ctx, record.CanonicalPath, before, value)
	if err != nil {
		return Snapshot{}, err
	}
	files, err := collector.config.Files.Collect(ctx, record.CanonicalPath, explicit)
	if err != nil {
		return Snapshot{}, err
	}
	after, _, err := collector.readCurrentManifest(ctx, record)
	if err != nil {
		return Snapshot{}, err
	}
	if before.Digest != after.Digest {
		return Snapshot{}, ErrSourceChanged
	}
	return Snapshot{
		SchemaVersion: SchemaVersion, WorkspaceID: record.ID, SystemID: record.SystemID,
		Kind: domain.RevisionWorkspace, ManifestDigest: before.Digest,
		Git: collector.gitFact(ctx, record.CanonicalPath), Files: files, Ports: portFacts(value.Spec.Ports), Services: services, Runners: runners,
	}, nil
}

func (collector *Collector) readCurrentManifest(ctx context.Context, record *workspace.Record) (workspace.Snapshot, manifest.Manifest, error) {
	snapshot, err := collector.config.Workspaces.CurrentSnapshot(ctx, record.CanonicalPath)
	if err != nil {
		return workspace.Snapshot{}, manifest.Manifest{}, fmt.Errorf("collect current manifest: %w", err)
	}
	if snapshot.SystemID != record.SystemID {
		return workspace.Snapshot{}, manifest.Manifest{}, ErrSourceChanged
	}
	var value manifest.Manifest
	if err := json.Unmarshal([]byte(snapshot.ParsedJSON), &value); err != nil {
		return workspace.Snapshot{}, manifest.Manifest{}, fmt.Errorf("decode current manifest: %w", err)
	}
	return snapshot, value, nil
}

func (collector *Collector) workspaceFacts(ctx context.Context, root string, snapshot workspace.Snapshot, value manifest.Manifest) ([]ServiceFact, []RunnerFact, []string, error) {
	definitions := make(map[domain.ServiceID]workspace.ServiceDefinition, len(snapshot.Services))
	for _, definition := range snapshot.Services {
		definitions[definition.ID] = definition
	}
	names := make([]string, 0, len(value.Spec.Services))
	for name := range value.Spec.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	services := make([]ServiceFact, 0, len(names))
	runners := make([]RunnerFact, 0, len(names))
	files := make([]string, 0, len(names)*12)
	for _, name := range names {
		serviceID, _ := domain.ParseServiceID(name)
		definition := value.Spec.Services[name]
		service, runnerFact, serviceFiles, err := collector.workspaceService(ctx, root, serviceID, definition, definitions[serviceID])
		if err != nil {
			return nil, nil, nil, err
		}
		services = append(services, service)
		if runnerFact != nil {
			runners = append(runners, *runnerFact)
		}
		files = append(files, serviceFiles...)
	}
	return services, runners, files, nil
}

func (collector *Collector) workspaceService(ctx context.Context, root string, id domain.ServiceID, value manifest.Service, definition workspace.ServiceDefinition) (ServiceFact, *RunnerFact, []string, error) {
	mode := domain.ProcessMode(value.Mode)
	if mode == "" {
		mode = domain.ProcessDaemon
	}
	service := ServiceFact{
		ServiceID: id, Driver: domain.DriverKind(value.Driver), Mode: mode, Required: boolDefault(value.Required, true),
		DefinitionDigest: definition.DefinitionDigest, Dependencies: dependencyFacts(value.DependsOn),
		HealthCoverage: healthCoverage(value), RestartPolicy: restartPolicy(value.Restart),
	}
	if value.Compose != nil {
		service.ComposeDigest = digestJSON(struct {
			Services    []string `json:"services"`
			BuildPolicy string   `json:"buildPolicy"`
		}{append([]string(nil), value.Compose.Services...), manifest.EffectiveComposeBuildPolicy(*value.Compose)})
		service.Images = unavailableImages(value.Compose.Services, "WORKSPACE_IMAGE_FACT_NOT_PROBED")
		return service, nil, []string{value.Compose.File}, nil
	}
	working, err := safeWorkspaceDirectory(root, value.WorkingDirectory)
	if err != nil {
		return ServiceFact{}, nil, nil, err
	}
	resolved, resolveErr := collector.config.Runners.Resolve(ctx, runner.ResolveRequest{
		Runner: runner.Kind(value.Runner), WorkspaceRoot: root, WorkingDirectory: working,
		VirtualEnvironment: workspaceRelative(root, value.VirtualEnvironment),
	})
	fact := &RunnerFact{ServiceID: id, Kind: value.Runner, Status: SourceUnavailable, Reason: runnerReason(resolveErr)}
	if resolveErr == nil {
		fact.Status, fact.Reason = SourceAvailable, ""
		fact.Version, fact.ResolutionKind, fact.ExecutableDigest = resolved.Version, string(resolved.ResolutionKind), resolved.ExecutableDigest
		service.CommandDigest = digestJSON(struct {
			ExecutableDigest string   `json:"executableDigest"`
			Arguments        []string `json:"arguments"`
		}{resolved.ExecutableDigest, value.Arguments})
	}
	return service, fact, dependencyFiles(root, working), nil
}

func (collector *Collector) collectRunning(ctx context.Context, workspaceID domain.WorkspaceID) (Snapshot, error) {
	instance, found, err := collector.config.Runtime.GetActive(ctx, workspaceID)
	if err != nil {
		return Snapshot{}, fmt.Errorf("load active runtime: %w", err)
	}
	if !found {
		return Snapshot{}, ErrSourceUnavailable
	}
	encoded, err := collector.config.ResolvedSpecs.LoadResolvedSpec(ctx, instance.ResolvedSpecDigest)
	if err != nil {
		return Snapshot{}, fmt.Errorf("load running resolved spec: %w", err)
	}
	var resolved runningResolvedSpec
	if err := json.Unmarshal(encoded, &resolved); err != nil {
		return Snapshot{}, fmt.Errorf("decode running resolved spec: %w", err)
	}
	runtimes, err := collector.config.Runtime.ListServices(ctx, instance.ID)
	if err != nil {
		return Snapshot{}, fmt.Errorf("load running services: %w", err)
	}
	services, runners, secrets, err := collector.runningFacts(ctx, runtimes, resolved)
	if err != nil {
		return Snapshot{}, err
	}
	launchManifest, err := collector.config.Workspaces.ManifestByDigest(ctx, instance.ManifestDigest)
	if err != nil {
		return Snapshot{}, fmt.Errorf("load running manifest snapshot: %w", err)
	}
	var manifestValue manifest.Manifest
	if err := json.Unmarshal([]byte(launchManifest.ParsedJSON), &manifestValue); err != nil {
		return Snapshot{}, fmt.Errorf("decode running manifest snapshot: %w", err)
	}
	instanceID := instance.ID
	return Snapshot{
		SchemaVersion: SchemaVersion, WorkspaceID: instance.WorkspaceID, SystemID: instance.SystemID,
		Kind: domain.RevisionRunning, SystemInstanceID: &instanceID, ManifestDigest: instance.ManifestDigest,
		ResolvedSpecDigest: instance.ResolvedSpecDigest, Git: GitFact{Status: SourceUnavailable, Reason: "LAUNCH_GIT_FACT_NOT_RECORDED"},
		Files: []FileFact{}, Ports: portFacts(manifestValue.Spec.Ports), Services: services, Runners: runners, Secrets: secrets,
	}, nil
}

func portFacts(values map[string]manifest.Port) []PortFact {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]PortFact, 0, len(names))
	for _, name := range names {
		value := values[name]
		result = append(result, PortFact{Name: name, Protocol: value.Protocol, Preferred: copyPort(value.Preferred),
			FallbackRange: value.FallbackRange, ConflictPolicy: value.ConflictPolicy, Exposure: value.Exposure})
	}
	return result
}

func copyPort(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

type runningResolvedSpec struct {
	Services map[string]runningResolvedService `json:"services"`
}

type runningResolvedService struct {
	Driver       domain.DriverKind                     `json:"driver"`
	Required     bool                                  `json:"required"`
	Dependencies map[string]domain.DependencyCondition `json:"dependencies"`
	Process      struct {
		Command   runner.ResolvedCommand `json:"Command"`
		Arguments []string               `json:"Arguments"`
	} `json:"process"`
	Compose *struct {
		Services    []string `json:"services"`
		BuildPolicy string   `json:"buildPolicy"`
	} `json:"compose"`
	Liveness json.RawMessage `json:"liveness"`
	Restart  struct {
		Policy string `json:"policy"`
	} `json:"restart"`
}

func (collector *Collector) runningFacts(ctx context.Context, runtimes []domain.ServiceInstance, resolved runningResolvedSpec) ([]ServiceFact, []RunnerFact, []SecretFact, error) {
	services := make([]ServiceFact, 0, len(runtimes))
	runners := make([]RunnerFact, 0, len(runtimes))
	secrets := make([]SecretFact, 0)
	for _, runtimeService := range runtimes {
		spec, ok := resolved.Services[runtimeService.ServiceID.String()]
		if !ok {
			return nil, nil, nil, ErrSourceChanged
		}
		service, runnerFact := runningServiceFact(runtimeService, spec)
		services = append(services, service)
		if runnerFact != nil {
			runners = append(runners, *runnerFact)
		}
		versions, err := collector.config.SecretVersions.ListServiceSecretVersions(ctx, runtimeService.ID)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("load running Secret metadata: %w", err)
		}
		for _, version := range versions {
			secrets = append(secrets, SecretFact{ServiceID: runtimeService.ServiceID, EnvironmentName: version.EnvironmentName, SystemID: version.Key.SystemID, Name: version.Key.Name, Provider: version.Provider, Version: version.Version})
		}
	}
	return services, runners, secrets, nil
}

func runningServiceFact(runtimeService domain.ServiceInstance, spec runningResolvedService) (ServiceFact, *RunnerFact) {
	service := ServiceFact{
		ServiceID: runtimeService.ServiceID, Driver: runtimeService.Driver, Mode: runtimeService.ProcessMode,
		Required: spec.Required, State: runtimeService.State, Dependencies: dependencyFactsResolved(spec.Dependencies),
		HealthCoverage: runningHealthCoverage(spec), RestartPolicy: spec.Restart.Policy,
	}
	if runtimeService.Identity != nil {
		service.CommandDigest = runtimeService.Identity.CommandDigest
	}
	if spec.Compose != nil {
		service.ComposeDigest = digestJSON(struct {
			Services    []string `json:"services"`
			BuildPolicy string   `json:"buildPolicy"`
		}{append([]string(nil), spec.Compose.Services...), spec.Compose.BuildPolicy})
		service.Images = unavailableImages(spec.Compose.Services, "LAUNCH_IMAGE_FACT_NOT_RECORDED")
		return service, nil
	}
	command := spec.Process.Command
	return service, &RunnerFact{ServiceID: runtimeService.ServiceID, Kind: string(commandKind(command)), Version: command.Version, ResolutionKind: string(command.ResolutionKind), ExecutableDigest: command.ExecutableDigest, Status: SourceAvailable}
}

func unavailableImages(services []string, reason string) []ComposeImageFact {
	result := make([]ComposeImageFact, 0, len(services))
	for _, service := range services {
		result = append(result, ComposeImageFact{ComposeService: service, Status: SourceUnavailable, Reason: reason})
	}
	return result
}

func commandKind(command runner.ResolvedCommand) runner.Kind {
	name := strings.ToLower(filepath.Base(command.Executable))
	name = strings.TrimSuffix(strings.TrimSuffix(name, ".exe"), ".cmd")
	switch name {
	case "mvn", "mvnw", "maven":
		return runner.Maven
	case "npm", "npm-cli":
		return runner.NPM
	case "java":
		return runner.Java
	case "node":
		return runner.Node
	case "go":
		return runner.Go
	case "python", "python3":
		return runner.PythonVenv
	default:
		return "unknown"
	}
}

func dependencyFacts(values map[string]string) []DependencyFact {
	result := make([]DependencyFact, 0, len(values))
	for name, condition := range values {
		id, _ := domain.ParseServiceID(name)
		result = append(result, DependencyFact{ServiceID: id, Condition: domain.DependencyCondition(condition)})
	}
	return result
}

func dependencyFactsResolved(values map[string]domain.DependencyCondition) []DependencyFact {
	result := make([]DependencyFact, 0, len(values))
	for name, condition := range values {
		id, _ := domain.ParseServiceID(name)
		result = append(result, DependencyFact{ServiceID: id, Condition: condition})
	}
	return result
}

func healthCoverage(value manifest.Service) domain.HealthCoverage {
	if value.Liveness == nil {
		return domain.HealthCoverageUnavailable
	}
	if value.Driver == string(domain.DriverCompose) {
		return domain.HealthCoverageContainer
	}
	if value.Liveness.Type == "process" {
		return domain.HealthCoverageProcessOnly
	}
	return domain.HealthCoverageBusiness
}

func runningHealthCoverage(value runningResolvedService) domain.HealthCoverage {
	if len(value.Liveness) == 0 || string(value.Liveness) == "null" {
		return domain.HealthCoverageUnavailable
	}
	if value.Driver == domain.DriverCompose {
		return domain.HealthCoverageContainer
	}
	var check struct {
		Kind string `json:"Kind"`
	}
	_ = json.Unmarshal(value.Liveness, &check)
	if strings.EqualFold(check.Kind, "process") {
		return domain.HealthCoverageProcessOnly
	}
	return domain.HealthCoverageBusiness
}

func restartPolicy(value manifest.Restart) string {
	if value.Policy == "" {
		return "never"
	}
	return value.Policy
}

func runnerReason(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case strings.Contains(err.Error(), "unsafe"):
		return "RUNNER_PATH_UNSAFE"
	case strings.Contains(err.Error(), "not found"):
		return "RUNNER_NOT_FOUND"
	default:
		return "RUNNER_PROBE_FAILED"
	}
}

func safeWorkspaceDirectory(root, relative string) (string, error) {
	if relative == "" {
		relative = "."
	}
	if filepath.IsAbs(relative) {
		return "", ErrSourceUnsafe
	}
	canonical, err := security.CanonicalExistingPath(filepath.Join(root, relative))
	if err != nil {
		return "", fmt.Errorf("%w: working directory", ErrSourceUnsafe)
	}
	inside, err := security.PathWithinRoot(root, canonical)
	if err != nil || !inside {
		return "", fmt.Errorf("%w: working directory", ErrSourceUnsafe)
	}
	return canonical, nil
}

func workspaceRelative(root, relative string) string {
	if relative == "" {
		return ""
	}
	return filepath.Join(root, relative)
}

func dependencyFiles(root, working string) []string {
	relative, err := filepath.Rel(root, working)
	if err != nil {
		return nil
	}
	names := []string{"pom.xml", "package.json", "package-lock.json", "npm-shrinkwrap.json", "pnpm-lock.yaml", "yarn.lock", "pyproject.toml", "poetry.lock", "Pipfile.lock", "go.mod", "go.sum", "requirements.txt", "requirements.lock"}
	result := make([]string, 0, len(names))
	for _, name := range names {
		result = append(result, filepath.Join(relative, name))
	}
	return result
}

func (collector *Collector) gitFact(ctx context.Context, root string) GitFact {
	if collector.config.Git == nil {
		return GitFact{Status: SourceUnavailable, Reason: "GIT_UNAVAILABLE"}
	}
	return collector.config.Git.Collect(ctx, root)
}

func boolDefault(value *bool, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return *value
}

func digestJSON(value any) string {
	encoded, _ := json.Marshal(value)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}
