package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"stackpilot/internal/domain"
	"stackpilot/internal/driver"
	"stackpilot/internal/driver/compose"
	"stackpilot/internal/health"
	"stackpilot/internal/manifest"
	"stackpilot/internal/ports"
	"stackpilot/internal/runner"
	"stackpilot/internal/workspace"
)

const resolvedSystemSpecSchema = "stackpilot.resolved/v1alpha1"

type driverPreflighter interface {
	Preflight(context.Context, driver.ResolvedServiceSpec) error
}

// ResolvedSystemSpec is the immutable, persistence-safe runtime snapshot for one start attempt.
type ResolvedSystemSpec struct {
	SchemaVersion  string                     `json:"schemaVersion"`
	WorkspaceID    domain.WorkspaceID         `json:"workspaceId"`
	SystemID       domain.SystemID            `json:"systemId"`
	InstanceID     domain.SystemInstanceID    `json:"instanceId"`
	ManifestDigest string                     `json:"manifestDigest"`
	PortPlanID     domain.PortPlanID          `json:"portPlanId"`
	Ports          map[string]ResolvedPort    `json:"ports"`
	Services       map[string]ResolvedService `json:"services"`
	Topology       [][]string                 `json:"topology"`
	PortReferences map[string][]string        `json:"portReferences"`
	FailurePolicy  FailurePolicy              `json:"failurePolicy"`
	StartTimeout   string                     `json:"startTimeout"`
	Digest         string                     `json:"-"`
	CanonicalJSON  []byte                     `json:"-"`
}

// ResolvedPort is a handle-free copy of one planned endpoint.
type ResolvedPort struct {
	Port         int                `json:"port"`
	Source       string             `json:"source"`
	Replaced     bool               `json:"replaced"`
	ConflictPort *int               `json:"conflictPort,omitempty"`
	LeaseID      domain.PortLeaseID `json:"leaseId"`
}

// ResolvedService contains only trusted expanded runtime values.
type ResolvedService struct {
	ServiceID    domain.ServiceID                      `json:"serviceId"`
	Driver       domain.DriverKind                     `json:"driver"`
	Required     bool                                  `json:"required"`
	Dependencies map[string]domain.DependencyCondition `json:"dependencies"`
	Process      driver.ResolvedServiceSpec            `json:"process,omitempty"`
	Compose      *ResolvedComposeService               `json:"compose,omitempty"`
	Readiness    health.ResolvedSpec                   `json:"readiness"`
	Liveness     *health.ResolvedSpec                  `json:"liveness,omitempty"`
	Restart      ResolvedRestartPolicy                 `json:"restart"`
}

// ResolvedRestartPolicy is the immutable bounded restart policy for one runtime.
type ResolvedRestartPolicy struct {
	Policy         string        `json:"policy"`
	InitialBackoff time.Duration `json:"initialBackoff"`
	MaxBackoff     time.Duration `json:"maxBackoff"`
	MaxAttempts    int           `json:"maxAttempts"`
	StableWindow   time.Duration `json:"stableWindow"`
}

// ResolvedComposeService is the immutable adapter input persisted for lifecycle and recovery.
type ResolvedComposeService struct {
	WorkspaceRoot string            `json:"workspaceRoot"`
	DataDir       string            `json:"dataDir"`
	ComposeFile   string            `json:"composeFile"`
	OverrideFile  string            `json:"overrideFile"`
	Services      []string          `json:"services"`
	BuildPolicy   string            `json:"buildPolicy"`
	Readiness     map[string]string `json:"readiness"`
	StartTimeout  time.Duration     `json:"startTimeout"`
	StopTimeout   time.Duration     `json:"stopTimeout"`
	StdoutPath    string            `json:"stdoutPath"`
	StderrPath    string            `json:"stderrPath"`
}

type resolveSystemInput struct {
	Workspace     workspace.Record
	Manifest      manifest.Manifest
	InstanceID    domain.SystemInstanceID
	DataDir       string
	PortPlan      *ports.Plan
	FailurePolicy FailurePolicyOverride
	OperationID   domain.OperationID
	Overrides     *compose.OverrideGenerator
}

type preparedService struct {
	definition manifest.Service
	working    string
	command    *runner.ResolvedCommand
}

func resolveSystemSpec(ctx context.Context, resolver runnerResolver, preflighter driverPreflighter, input resolveSystemInput) (*ResolvedSystemSpec, error) {
	if input.PortPlan == nil || preflighter == nil {
		return nil, ErrInvalidInput
	}
	graph, err := NewDAG(input.Manifest.Spec.Services)
	if err != nil {
		return nil, err
	}
	prepared, err := prepareAllServices(ctx, resolver, input)
	if err != nil {
		return nil, err
	}
	return resolvePreparedSystemSpec(ctx, preflighter, input, graph, prepared)
}

func resolvePreparedSystemSpec(ctx context.Context, preflighter driverPreflighter, input resolveSystemInput, graph *DAG, prepared map[string]preparedService) (*ResolvedSystemSpec, error) {
	instanceDir, err := createInstanceDirectory(input.DataDir, input.InstanceID)
	if err != nil {
		return nil, err
	}
	resolved, err := expandAllServices(ctx, preflighter, input, prepared, instanceDir)
	if err != nil {
		return nil, err
	}
	spec := assembleResolvedSystem(input, graph, resolved)
	if err := finalizeResolvedSystem(spec); err != nil {
		return nil, err
	}
	return spec, nil
}

func prepareAllServices(ctx context.Context, resolver runnerResolver, input resolveSystemInput) (map[string]preparedService, error) {
	result := make(map[string]preparedService, len(input.Manifest.Spec.Services))
	for _, serviceID := range sortedServiceNames(input.Manifest.Spec.Services) {
		definition := input.Manifest.Spec.Services[serviceID]
		if definition.Driver == string(domain.DriverCompose) {
			result[serviceID] = preparedService{definition: definition}
			continue
		}
		working, err := canonicalWorkingDirectory(input.Workspace.CanonicalPath, definition.WorkingDirectory)
		if err != nil {
			return nil, fmt.Errorf("prepare %s working directory: %w", serviceID, err)
		}
		command, err := resolver.Resolve(ctx, runner.ResolveRequest{
			Runner: runner.Kind(definition.Runner), WorkspaceRoot: input.Workspace.CanonicalPath, WorkingDirectory: working,
			VirtualEnvironment: virtualEnvironmentPath(input.Workspace.CanonicalPath, definition),
		})
		if err != nil {
			return nil, fmt.Errorf("prepare %s runner: %w", serviceID, err)
		}
		result[serviceID] = preparedService{definition: definition, working: working, command: command}
	}
	return result, nil
}

func expandAllServices(ctx context.Context, preflighter driverPreflighter, input resolveSystemInput, prepared map[string]preparedService, instanceDir string) (map[string]ResolvedService, error) {
	portValues := planPortValues(input.PortPlan)
	result := make(map[string]ResolvedService, len(prepared))
	for _, serviceName := range sortedPreparedNames(prepared) {
		service, err := expandService(input, serviceName, prepared[serviceName], instanceDir, portValues)
		if err != nil {
			return nil, fmt.Errorf("expand %s: %w", serviceName, err)
		}
		if service.Driver == domain.DriverProcess {
			if err := preflighter.Preflight(ctx, service.Process); err != nil {
				return nil, fmt.Errorf("preflight %s: %w", serviceName, err)
			}
		}
		result[serviceName] = service
	}
	return result, nil
}

func expandService(input resolveSystemInput, serviceName string, prepared preparedService, instanceDir string, portValues map[string]int) (ResolvedService, error) {
	serviceID, err := domain.ParseServiceID(serviceName)
	if err != nil {
		return ResolvedService{}, err
	}
	dependencies := resolvedDependencies(prepared.definition)
	if prepared.definition.Driver == string(domain.DriverCompose) {
		return expandComposeService(input, serviceID, prepared.definition, instanceDir, portValues, dependencies)
	}
	if prepared.command == nil {
		return ResolvedService{}, ErrInvalidInput
	}
	singleInput := resolveSingleInput{Workspace: input.Workspace, Manifest: input.Manifest, SystemID: input.InstanceID, DataDir: input.DataDir}
	processSpec, err := buildProcessSpec(singleInput, serviceID, prepared.definition, portValues, prepared.working, instanceDir, *prepared.command)
	if err != nil {
		return ResolvedService{}, err
	}
	readiness, err := buildReadiness(prepared.definition.Readiness, processSpec, portValues, singleInput)
	if err != nil {
		return ResolvedService{}, err
	}
	liveness, err := buildOptionalHealth(prepared.definition.Liveness, processSpec, portValues, singleInput)
	if err != nil {
		return ResolvedService{}, err
	}
	restart, err := resolveRestartPolicy(prepared.definition.Restart)
	if err != nil {
		return ResolvedService{}, err
	}
	return ResolvedService{ServiceID: serviceID, Driver: domain.DriverProcess, Required: boolValue(prepared.definition.Required, true), Dependencies: dependencies, Process: processSpec, Readiness: readiness, Liveness: liveness, Restart: restart}, nil
}

func expandComposeService(input resolveSystemInput, serviceID domain.ServiceID, definition manifest.Service, instanceDir string, portValues map[string]int, dependencies map[string]domain.DependencyCondition) (ResolvedService, error) {
	if definition.Compose == nil || input.Overrides == nil {
		return ResolvedService{}, ErrInvalidInput
	}
	composeFile, err := manifest.ResolveWorkspaceFile(input.Workspace.CanonicalPath, definition.Compose.File, "compose.file")
	if err != nil {
		return ResolvedService{}, err
	}
	expand := templateExpander(resolveSingleInput{Workspace: input.Workspace, SystemID: input.InstanceID}, portValues)
	environment := make(map[string]map[string]string, len(definition.Compose.Environment))
	for name, values := range definition.Compose.Environment {
		environment[name] = make(map[string]string, len(values))
		for key, value := range values {
			environment[name][key] = expand(value)
		}
	}
	portOverrides := make(map[string]compose.PortOverride, len(definition.Compose.Ports))
	for logicalName, mapping := range definition.Compose.Ports {
		portOverrides[logicalName] = compose.PortOverride{Service: mapping.Service, Target: mapping.Target, Published: portValues[logicalName]}
	}
	override, err := input.Overrides.Generate(compose.OverrideRequest{OperationID: input.OperationID, SystemID: input.Workspace.SystemID, WorkspaceID: input.Workspace.ID, InstanceID: input.InstanceID, Services: definition.Compose.Services, Ports: portOverrides, Environment: environment})
	if err != nil {
		return ResolvedService{}, err
	}
	startTimeout, stopTimeout, err := composeTimeouts(input.Manifest.Spec.Policies, definition)
	if err != nil {
		return ResolvedService{}, err
	}
	resolved := &ResolvedComposeService{WorkspaceRoot: input.Workspace.CanonicalPath, DataDir: input.DataDir, ComposeFile: composeFile, OverrideFile: override.Path, Services: append([]string(nil), definition.Compose.Services...), BuildPolicy: manifest.EffectiveComposeBuildPolicy(*definition.Compose), Readiness: manifest.EffectiveComposeReadiness(*definition.Compose), StartTimeout: startTimeout, StopTimeout: stopTimeout, StdoutPath: filepath.Join(instanceDir, serviceID.String()+".stdout.spool"), StderrPath: filepath.Join(instanceDir, serviceID.String()+".stderr.spool")}
	healthInput := resolveSingleInput{Workspace: input.Workspace, Manifest: input.Manifest, SystemID: input.InstanceID}
	readiness, err := buildReadiness(definition.Readiness, driver.ResolvedServiceSpec{}, portValues, healthInput)
	if err != nil {
		return ResolvedService{}, err
	}
	liveness, err := buildOptionalHealth(definition.Liveness, driver.ResolvedServiceSpec{}, portValues, healthInput)
	if err != nil {
		return ResolvedService{}, err
	}
	restart, err := resolveRestartPolicy(definition.Restart)
	return ResolvedService{ServiceID: serviceID, Driver: domain.DriverCompose, Required: boolValue(definition.Required, true), Dependencies: dependencies, Compose: resolved, Readiness: readiness, Liveness: liveness, Restart: restart}, err
}

func buildOptionalHealth(check *manifest.HealthCheck, process driver.ResolvedServiceSpec, ports map[string]int, input resolveSingleInput) (*health.ResolvedSpec, error) {
	if check == nil {
		return nil, nil
	}
	resolved, err := buildReadiness(check, process, ports, input)
	return &resolved, err
}

func resolveRestartPolicy(value manifest.Restart) (ResolvedRestartPolicy, error) {
	if value.Policy == "" {
		value.Policy = "never"
	}
	if value.InitialBackoff == "" {
		value.InitialBackoff = "1s"
	}
	if value.MaxBackoff == "" {
		value.MaxBackoff = "1m"
	}
	if value.StableWindow == "" {
		value.StableWindow = "5m"
	}
	if value.MaxAttempts == nil {
		attempts := 3
		value.MaxAttempts = &attempts
	}
	initial, err := time.ParseDuration(value.InitialBackoff)
	if err != nil {
		return ResolvedRestartPolicy{}, err
	}
	maximum, err := time.ParseDuration(value.MaxBackoff)
	if err != nil {
		return ResolvedRestartPolicy{}, err
	}
	stable, err := time.ParseDuration(value.StableWindow)
	if err != nil || value.MaxAttempts == nil {
		return ResolvedRestartPolicy{}, ErrInvalidInput
	}
	return ResolvedRestartPolicy{Policy: value.Policy, InitialBackoff: initial, MaxBackoff: maximum, MaxAttempts: *value.MaxAttempts, StableWindow: stable}, nil
}

func resolvedDependencies(definition manifest.Service) map[string]domain.DependencyCondition {
	result := make(map[string]domain.DependencyCondition, len(definition.DependsOn))
	for dependency, condition := range definition.DependsOn {
		result[dependency] = domain.DependencyCondition(condition)
	}
	return result
}

func composeTimeouts(policies manifest.Policies, definition manifest.Service) (time.Duration, time.Duration, error) {
	start, err := time.ParseDuration(policies.StartTimeout)
	if err != nil {
		return 0, 0, err
	}
	stopValue := definition.Stop.GracefulTimeout
	if stopValue == "" {
		stopValue = policies.StopTimeout
	}
	stop, err := time.ParseDuration(stopValue)
	return start, stop, err
}

func assembleResolvedSystem(input resolveSystemInput, graph *DAG, services map[string]ResolvedService) *ResolvedSystemSpec {
	return &ResolvedSystemSpec{
		SchemaVersion: resolvedSystemSpecSchema, WorkspaceID: input.Workspace.ID, SystemID: input.Workspace.SystemID,
		InstanceID: input.InstanceID, ManifestDigest: input.Workspace.LastValidDigest, PortPlanID: input.PortPlan.ID,
		Ports: copyResolvedPorts(input.PortPlan), Services: services, Topology: graph.Layers(),
		PortReferences: collectPortReferences(input.Manifest), FailurePolicy: ResolveFailurePolicy(input.Manifest.Spec.Policies, input.FailurePolicy),
		StartTimeout: input.Manifest.Spec.Policies.StartTimeout,
	}
}

func finalizeResolvedSystem(spec *ResolvedSystemSpec) error {
	encoded, err := json.Marshal(spec)
	if err != nil {
		return fmt.Errorf("encode resolved system spec: %w", err)
	}
	digest := sha256.Sum256(encoded)
	spec.CanonicalJSON = append([]byte(nil), encoded...)
	spec.Digest = hex.EncodeToString(digest[:])
	return nil
}

func copyResolvedPorts(plan *ports.Plan) map[string]ResolvedPort {
	result := make(map[string]ResolvedPort, len(plan.Assignments))
	for logicalName, assignment := range plan.Assignments {
		result[logicalName] = ResolvedPort{
			Port: assignment.Port, Source: assignment.Source, Replaced: assignment.Replaced,
			ConflictPort: copyIntPointer(assignment.ConflictPort), LeaseID: assignment.LeaseID,
		}
	}
	return result
}

func copyIntPointer(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func planPortValues(plan *ports.Plan) map[string]int {
	result := make(map[string]int, len(plan.Assignments))
	for logicalName, assignment := range plan.Assignments {
		result[logicalName] = assignment.Port
	}
	return result
}

func collectPortReferences(value manifest.Manifest) map[string][]string {
	result := make(map[string][]string, len(value.Spec.Ports))
	for logicalName := range value.Spec.Ports {
		token := "${ports." + logicalName + "}"
		for serviceID, service := range value.Spec.Services {
			collectServicePortReferences(result, logicalName, token, serviceID, service)
		}
		sort.Strings(result[logicalName])
	}
	return result
}

func collectServicePortReferences(result map[string][]string, logicalName, token, serviceID string, service manifest.Service) {
	base := "services." + serviceID
	for index, argument := range service.Arguments {
		appendReference(result, logicalName, token, argument, fmt.Sprintf("%s.arguments[%d]", base, index))
	}
	for name, value := range service.Environment {
		appendReference(result, logicalName, token, value, base+".environment."+name)
	}
	if service.Readiness != nil {
		appendReference(result, logicalName, token, service.Readiness.URL, base+".readiness.url")
		if value, ok := service.Readiness.Port.(string); ok {
			appendReference(result, logicalName, token, value, base+".readiness.port")
		}
	}
	if service.Liveness != nil {
		appendReference(result, logicalName, token, service.Liveness.URL, base+".liveness.url")
		if value, ok := service.Liveness.Port.(string); ok {
			appendReference(result, logicalName, token, value, base+".liveness.port")
		}
	}
	if service.Compose != nil {
		if _, exists := service.Compose.Ports[logicalName]; exists {
			result[logicalName] = append(result[logicalName], base+".compose.ports."+logicalName)
		}
		for composeService, environment := range service.Compose.Environment {
			for name, value := range environment {
				appendReference(result, logicalName, token, value, base+".compose.environment."+composeService+"."+name)
			}
		}
	}
}

func appendReference(result map[string][]string, logicalName, token, value, path string) {
	if strings.Contains(value, token) {
		result[logicalName] = append(result[logicalName], path)
	}
}

func sortedServiceNames(services map[string]manifest.Service) []string {
	result := make([]string, 0, len(services))
	for serviceID := range services {
		result = append(result, serviceID)
	}
	sort.Strings(result)
	return result
}

func sortedPreparedNames(services map[string]preparedService) []string {
	result := make([]string, 0, len(services))
	for serviceID := range services {
		result = append(result, serviceID)
	}
	sort.Strings(result)
	return result
}
