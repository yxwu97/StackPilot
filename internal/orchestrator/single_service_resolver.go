package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"stackpilot/internal/domain"
	"stackpilot/internal/driver"
	processdriver "stackpilot/internal/driver/process"
	"stackpilot/internal/health"
	"stackpilot/internal/manifest"
	"stackpilot/internal/runner"
	"stackpilot/internal/security"
	"stackpilot/internal/workspace"
)

var (
	// ErrSingleServiceScope indicates that the manifest requires Phase 1C DAG orchestration.
	ErrSingleServiceScope = errors.New("manifest requires multi-service orchestration")
	// ErrPortInUse indicates that a selected fixed Phase 1B port cannot be bound.
	ErrPortInUse = errors.New("selected service port is already in use")
	// ErrPortPlanRequired indicates that the manifest needs the Phase 1C port planner.
	ErrPortPlanRequired = errors.New("manifest requires dynamic port planning")
)

type runnerResolver interface {
	Resolve(context.Context, runner.ResolveRequest) (*runner.ResolvedCommand, error)
}

type resolvedSingleService struct {
	System          domain.SystemInstance
	Service         domain.ServiceInstance
	Process         driver.ResolvedServiceSpec
	Readiness       health.ResolvedSpec
	ManifestService manifest.Service
	Policies        manifest.Policies
}

type resolveSingleInput struct {
	Workspace     workspace.Record
	Manifest      manifest.Manifest
	SystemID      domain.SystemInstanceID
	ServiceID     domain.ServiceInstanceID
	DataDir       string
	PortOverrides map[string]int
}

func resolveSingleService(ctx context.Context, resolver runnerResolver, input resolveSingleInput) (resolvedSingleService, error) {
	serviceID, definition, err := singleRootService(input.Manifest)
	if err != nil {
		return resolvedSingleService{}, err
	}
	ports, err := resolvePhase1BPorts(input.Manifest.Spec.Ports, input.PortOverrides)
	if err != nil {
		return resolvedSingleService{}, err
	}
	working, err := canonicalWorkingDirectory(input.Workspace.CanonicalPath, definition.WorkingDirectory)
	if err != nil {
		return resolvedSingleService{}, err
	}
	command, err := resolver.Resolve(ctx, runner.ResolveRequest{
		Runner: runner.Kind(definition.Runner), WorkspaceRoot: input.Workspace.CanonicalPath, WorkingDirectory: working,
		VirtualEnvironment: virtualEnvironmentPath(input.Workspace.CanonicalPath, definition),
	})
	if err != nil {
		return resolvedSingleService{}, err
	}
	instanceDir, err := createInstanceDirectory(input.DataDir, input.SystemID)
	if err != nil {
		return resolvedSingleService{}, err
	}
	processSpec, err := buildProcessSpec(input, serviceID, definition, ports, working, instanceDir, *command)
	if err != nil {
		return resolvedSingleService{}, err
	}
	healthSpec, err := buildReadiness(definition.Readiness, processSpec, ports, input)
	if err != nil {
		return resolvedSingleService{}, err
	}
	return buildResolvedRuntime(input, serviceID, definition, processSpec, healthSpec)
}

func virtualEnvironmentPath(workspaceRoot string, definition manifest.Service) string {
	if definition.Runner != string(runner.PythonVenv) || definition.VirtualEnvironment == "" {
		return ""
	}
	return filepath.Join(workspaceRoot, definition.VirtualEnvironment)
}

func singleRootService(value manifest.Manifest) (domain.ServiceID, manifest.Service, error) {
	roots := make([]string, 0)
	for name, service := range value.Spec.Services {
		if len(service.DependsOn) == 0 {
			roots = append(roots, name)
		}
	}
	sort.Strings(roots)
	if len(roots) != 1 {
		return "", manifest.Service{}, ErrSingleServiceScope
	}
	id, err := domain.ParseServiceID(roots[0])
	return id, value.Spec.Services[roots[0]], err
}

func resolvePhase1BPorts(definitions map[string]manifest.Port, overrides map[string]int) (map[string]int, error) {
	result := make(map[string]int, len(definitions))
	for name, definition := range definitions {
		value := 0
		if definition.Preferred != nil {
			value = *definition.Preferred
		}
		if override, ok := overrides[name]; ok {
			value = override
		}
		if value < 1024 || value > 65535 {
			return nil, ErrPortPlanRequired
		}
		result[name] = value
	}
	for name, value := range overrides {
		if _, ok := definitions[name]; !ok || value < 1024 || value > 65535 {
			return nil, ErrInvalidInput
		}
	}
	return result, probeLoopbackPorts(result)
}

func probeLoopbackPorts(ports map[string]int) error {
	values := make(map[int]struct{}, len(ports))
	for _, port := range ports {
		values[port] = struct{}{}
	}
	for port := range values {
		listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err != nil {
			return fmt.Errorf("%w: %d", ErrPortInUse, port)
		}
		if err := listener.Close(); err != nil {
			return fmt.Errorf("close port probe: %w", err)
		}
	}
	return nil
}

func canonicalWorkingDirectory(root, relative string) (string, error) {
	if filepath.IsAbs(relative) {
		return "", manifest.ErrPathOutsideWorkspace
	}
	canonical, err := security.CanonicalExistingPath(filepath.Join(root, relative))
	if err != nil {
		return "", err
	}
	inside, err := security.PathWithinRoot(root, canonical)
	if err != nil || !inside {
		return "", manifest.ErrPathOutsideWorkspace
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.IsDir() {
		return "", manifest.ErrPathOutsideWorkspace
	}
	return canonical, nil
}

func createInstanceDirectory(dataDir string, id domain.SystemInstanceID) (string, error) {
	path := filepath.Join(dataDir, "instances", id.String())
	if err := os.MkdirAll(path, 0o700); err != nil {
		return "", fmt.Errorf("create instance directory: %w", err)
	}
	canonical, err := security.CanonicalExistingPath(path)
	if err != nil {
		return "", fmt.Errorf("canonicalize instance directory: %w", err)
	}
	inside, err := security.PathWithinRoot(dataDir, canonical)
	if err != nil || !inside {
		return "", fmt.Errorf("instance directory escaped data directory")
	}
	return canonical, nil
}

func buildProcessSpec(input resolveSingleInput, serviceID domain.ServiceID, definition manifest.Service, ports map[string]int, working, instanceDir string, command runner.ResolvedCommand) (driver.ResolvedServiceSpec, error) {
	gracefulValue := definition.Stop.GracefulTimeout
	if gracefulValue == "" {
		gracefulValue = input.Manifest.Spec.Policies.StopTimeout
	}
	graceful, err := time.ParseDuration(gracefulValue)
	if err != nil {
		return driver.ResolvedServiceSpec{}, err
	}
	expand := templateExpander(input, ports)
	arguments := make([]string, len(definition.Arguments))
	for index, value := range definition.Arguments {
		arguments[index] = expand(value)
	}
	environment := make(map[string]string, len(definition.Environment))
	secretReferences := make(map[string]string)
	for name, value := range definition.Environment {
		environment[name] = expand(value)
		if secretName, ok := manifest.SecretReference(value); ok {
			secretReferences[name] = secretName
		}
	}
	return driver.ResolvedServiceSpec{
		ServiceID: serviceID, Driver: domain.DriverKind(definition.Driver), Mode: domain.ProcessMode(definition.Mode),
		WorkspaceRoot: input.Workspace.CanonicalPath, InstanceDir: instanceDir, WorkingDirectory: working,
		Command: command, Arguments: arguments, Environment: environment, SecretReferences: secretReferences,
		StdoutPath: filepath.Join(instanceDir, serviceID.String()+".stdout.spool"),
		StderrPath: filepath.Join(instanceDir, serviceID.String()+".stderr.spool"), GracefulTimeout: graceful,
	}, nil
}

func templateExpander(input resolveSingleInput, ports map[string]int) func(string) string {
	return func(value string) string {
		value = strings.ReplaceAll(value, "${workspace.root}", input.Workspace.CanonicalPath)
		value = strings.ReplaceAll(value, "${instance.id}", input.SystemID.String())
		value = strings.ReplaceAll(value, "${system.id}", input.Workspace.SystemID.String())
		for name, port := range ports {
			value = strings.ReplaceAll(value, "${ports."+name+"}", strconv.Itoa(port))
		}
		return value
	}
}

func buildReadiness(check *manifest.HealthCheck, processSpec driver.ResolvedServiceSpec, ports map[string]int, input resolveSingleInput) (health.ResolvedSpec, error) {
	if check == nil {
		if processSpec.Mode == domain.ProcessOneshot {
			return health.ResolvedSpec{}, nil
		}
		return health.ResolvedSpec{}, health.ErrInvalidSpec
	}
	timeout, err := time.ParseDuration(manifest.EffectiveHealthTimeout(*check, input.Manifest.Spec.Policies))
	if err != nil {
		return health.ResolvedSpec{}, err
	}
	interval, err := time.ParseDuration(manifest.EffectiveHealthInterval(*check))
	if err != nil {
		return health.ResolvedSpec{}, err
	}
	spec := health.ResolvedSpec{
		Kind: health.Kind(check.Type), CheckTimeout: interval, ReadinessTimeout: timeout, Interval: interval,
		SuccessThreshold: *check.SuccessThreshold, FailureThreshold: *check.FailureThreshold,
	}
	expand := templateExpander(input, ports)
	spec.Host, spec.URL = check.Host, expand(check.URL)
	if check.Type == string(health.KindProcess) {
		spec.Identity = domain.ProcessIdentity{}
	}
	if check.Type == string(health.KindTCP) {
		spec.Port, err = resolvedHealthPort(check.Port, ports)
	}
	_ = processSpec
	return spec, err
}

func resolvedHealthPort(value any, ports map[string]int) (int, error) {
	switch typed := value.(type) {
	case float64:
		return int(typed), nil
	case int:
		return typed, nil
	case string:
		name := strings.TrimSuffix(strings.TrimPrefix(typed, "${ports."), "}")
		port, ok := ports[name]
		if ok {
			return port, nil
		}
	}
	return 0, health.ErrInvalidSpec
}

func buildResolvedRuntime(input resolveSingleInput, serviceID domain.ServiceID, definition manifest.Service, processSpec driver.ResolvedServiceSpec, healthSpec health.ResolvedSpec) (resolvedSingleService, error) {
	encoded, err := json.Marshal(struct {
		Process driver.ResolvedServiceSpec
		Health  health.ResolvedSpec
	}{Process: processSpec, Health: healthSpec})
	if err != nil {
		return resolvedSingleService{}, fmt.Errorf("encode resolved service digest: %w", err)
	}
	digest := sha256.Sum256(encoded)
	now := time.Now().UTC()
	system := domain.SystemInstance{
		ID: input.SystemID, WorkspaceID: input.Workspace.ID, SystemID: input.Workspace.SystemID,
		ManifestDigest: input.Workspace.LastValidDigest, ResolvedSpecDigest: hex.EncodeToString(digest[:]),
		State: domain.SystemStarting, StartedAt: now,
	}
	service := domain.ServiceInstance{
		ID: input.ServiceID, SystemInstanceID: input.SystemID, ServiceID: serviceID,
		Driver: domain.DriverProcess, ProcessMode: processSpec.Mode, State: domain.ServiceStarting, GracefulTimeout: processSpec.GracefulTimeout,
		StateVersion: 1, CreatedAt: now, UpdatedAt: now,
	}
	return resolvedSingleService{
		System: system, Service: service, Process: processSpec, Readiness: healthSpec,
		ManifestService: definition, Policies: input.Manifest.Spec.Policies,
	}, nil
}

func resolutionErrorCode(err error) string {
	switch {
	case errors.Is(err, ErrSingleServiceScope), errors.Is(err, ErrPortPlanRequired):
		return "FEATURE_NOT_ENABLED"
	case errors.Is(err, ErrPortInUse):
		return "PORT_CONFLICT"
	case errors.Is(err, runner.ErrRunnerNotFound):
		return "RUNNER_NOT_FOUND"
	case errors.Is(err, runner.ErrVersionProbeTimeout):
		return "RUNNER_VERSION_CHECK_TIMEOUT"
	case errors.Is(err, runner.ErrVersionProbeFailed):
		return "RUNNER_VERSION_CHECK_FAILED"
	case errors.Is(err, runner.ErrRunnerPathUnsafe):
		return "RUNNER_PATH_UNSAFE"
	case errors.Is(err, processdriver.ErrPlatformUnsupported):
		return "PLATFORM_NOT_SUPPORTED"
	default:
		return "PROCESS_START_FAILED"
	}
}
