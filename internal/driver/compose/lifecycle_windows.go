//go:build windows

package compose

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"stackpilot/internal/security"
)

// NewLifecycle constructs the Windows Docker Compose lifecycle adapter.
func NewLifecycle(config LifecycleConfig) (*Lifecycle, error) {
	environment := normalizedEnvironment(config.Environment)
	if config.Environment == nil {
		environment = normalizedEnvironment(currentEnvironment())
	}
	run := config.Run
	if run == nil {
		run = runCommand
	}
	preflight := config.Preflight
	if preflight == nil {
		value, err := NewPreflighter(Config{DockerExecutable: config.DockerExecutable, Environment: environment, Run: run})
		if err != nil {
			return nil, err
		}
		preflight = value.Preflight
	}
	startLog := config.StartLog
	if startLog == nil {
		startLog = startLogCommand
	}
	return &Lifecycle{docker: config.DockerExecutable, environment: environment, run: run, preflight: preflight, startLog: startLog}, nil
}

// Preflight verifies Docker availability and one resolved Compose definition without creating containers.
func (lifecycle *Lifecycle) Preflight(ctx context.Context, request PreflightRequest) (*PreflightResult, error) {
	return lifecycle.preflight(ctx, request)
}

// Start runs the explicit build policy and then starts the declared services with --no-build.
func (lifecycle *Lifecycle) Start(ctx context.Context, request LifecycleRequest) (ProjectIdentity, error) {
	identity, err := lifecycle.Prepare(ctx, request)
	if err != nil {
		return ProjectIdentity{}, err
	}
	if identity.BuildPolicy == "always" {
		if err := lifecycle.Build(ctx, identity); err != nil {
			return ProjectIdentity{}, err
		}
	}
	if err := lifecycle.Up(ctx, identity); err != nil {
		return ProjectIdentity{}, err
	}
	return identity, nil
}

// StartWithoutBuild is used by service restart and always skips image build.
func (lifecycle *Lifecycle) StartWithoutBuild(ctx context.Context, request LifecycleRequest) (ProjectIdentity, error) {
	identity, err := lifecycle.Prepare(ctx, request)
	if err != nil {
		return ProjectIdentity{}, err
	}
	if err := lifecycle.Up(ctx, identity); err != nil {
		return ProjectIdentity{}, err
	}
	return identity, nil
}

// Prepare validates a resolved request and returns its immutable preflight identity.
func (lifecycle *Lifecycle) Prepare(ctx context.Context, request LifecycleRequest) (ProjectIdentity, error) {
	request = normalizedLifecycleRequest(request)
	_, resolved, err := lifecycle.validateRequest(request, false)
	if err != nil {
		return ProjectIdentity{}, err
	}
	result, err := lifecycle.preflight(ctx, PreflightRequest{WorkspaceRoot: resolved.WorkspaceRoot, ComposeFile: resolved.ComposeFile, Services: resolved.Services, BuildPolicy: resolved.BuildPolicy, Readiness: resolved.Readiness})
	if err != nil {
		return ProjectIdentity{}, err
	}
	resolved.BuildServices = append([]string(nil), result.BuildServices...)
	if len(result.Readiness) != 0 {
		resolved.Readiness = cloneRequirements(result.Readiness, resolved.Services)
	}
	resolved = normalizedLifecycleRequest(resolved)
	return newProjectIdentity(resolved)
}

// Build executes only the sorted build services from a verified project identity.
func (lifecycle *Lifecycle) Build(ctx context.Context, identity ProjectIdentity) error {
	docker, resolved, err := lifecycle.validateIdentity(identity)
	if err != nil {
		return err
	}
	if resolved.BuildPolicy != "always" || len(resolved.BuildServices) == 0 {
		return ErrBuildConfigInvalid
	}
	buildContext, cancel := context.WithTimeout(ctx, resolved.StartTimeout)
	defer cancel()
	arguments := append(projectArguments(identity), "build")
	arguments = append(arguments, identity.BuildServices...)
	if _, err := lifecycle.run(buildContext, docker, arguments, filepath.Dir(identity.ComposeFile), lifecycle.environment); err != nil {
		if errors.Is(buildContext.Err(), context.DeadlineExceeded) {
			return ErrComposeBuildTimeout
		}
		if contextErr := buildContext.Err(); contextErr != nil {
			return contextErr
		}
		return ErrComposeBuildFailed
	}
	return nil
}

// Up starts only declared services and never builds implicitly.
func (lifecycle *Lifecycle) Up(ctx context.Context, identity ProjectIdentity) error {
	docker, resolved, err := lifecycle.validateIdentity(identity)
	if err != nil {
		return err
	}
	startContext, cancel := context.WithTimeout(ctx, resolved.StartTimeout)
	defer cancel()
	arguments := append(projectArguments(identity), "up", "-d", "--wait", "--no-deps", "--no-build", "--wait-timeout", durationSeconds(resolved.StartTimeout))
	arguments = append(arguments, identity.Services...)
	if _, err := lifecycle.run(startContext, docker, arguments, filepath.Dir(identity.ComposeFile), lifecycle.environment); err != nil {
		return lifecycleCommandFailure(startContext, ErrComposeStartFailed)
	}
	observation, err := lifecycle.inspectWithDocker(ctx, docker, identity)
	if err != nil || observation.State != "running" {
		return ErrComposeStartFailed
	}
	return nil
}

// Inspect returns structured status scoped to the verified project identity.
func (lifecycle *Lifecycle) Inspect(ctx context.Context, identity ProjectIdentity) (ProjectObservation, error) {
	docker, _, err := lifecycle.validateIdentity(identity)
	if err != nil {
		return ProjectObservation{}, err
	}
	return lifecycle.inspectWithDocker(ctx, docker, identity)
}

func (lifecycle *Lifecycle) inspectWithDocker(ctx context.Context, docker string, identity ProjectIdentity) (ProjectObservation, error) {
	inspectContext, cancel := context.WithTimeout(ctx, defaultInspectTimeout)
	defer cancel()
	arguments := append(projectArguments(identity), "ps", "--all", "--format", "json", "--no-trunc")
	arguments = append(arguments, identity.Services...)
	output, err := lifecycle.run(inspectContext, docker, arguments, filepath.Dir(identity.ComposeFile), lifecycle.environment)
	if err != nil {
		return ProjectObservation{}, lifecycleCommandFailure(inspectContext, ErrComposeInspectFailed)
	}
	rows, err := decodeComposePS(output.Stdout)
	if err != nil {
		return ProjectObservation{}, err
	}
	return buildProjectObservation(identity, rows)
}

// Stop performs a non-destructive Compose stop and never invokes down or volume deletion.
func (lifecycle *Lifecycle) Stop(ctx context.Context, identity ProjectIdentity) error {
	docker, resolved, err := lifecycle.validateIdentity(identity)
	if err != nil {
		return err
	}
	stopContext, cancel := context.WithTimeout(ctx, resolved.StopTimeout+10*time.Second)
	defer cancel()
	arguments := append(projectArguments(identity), "stop", "--timeout", durationSeconds(resolved.StopTimeout))
	arguments = append(arguments, identity.Services...)
	if _, err := lifecycle.run(stopContext, docker, arguments, filepath.Dir(identity.ComposeFile), lifecycle.environment); err != nil {
		return lifecycleCommandFailure(stopContext, ErrComposeStopFailed)
	}
	return nil
}

func (lifecycle *Lifecycle) validateIdentity(identity ProjectIdentity) (string, LifecycleRequest, error) {
	if err := verifyProjectIdentity(identity); err != nil {
		return "", LifecycleRequest{}, err
	}
	request := LifecycleRequest{
		WorkspaceRoot: identity.WorkspaceRoot, DataDir: identity.DataDir, ComposeFile: identity.ComposeFile,
		OverrideFile: identity.OverrideFile, SystemID: identity.SystemID, WorkspaceID: identity.WorkspaceID,
		InstanceID: identity.InstanceID, Services: identity.Services,
		BuildPolicy: identity.BuildPolicy, BuildServices: identity.BuildServices, Readiness: identity.Readiness,
		StartTimeout: identity.StartTimeout, StopTimeout: identity.StopTimeout,
	}
	request = normalizedLifecycleRequest(request)
	return lifecycle.validateRequest(request, true)
}

func (lifecycle *Lifecycle) validateRequest(request LifecycleRequest, requireResolvedBuild bool) (string, LifecycleRequest, error) {
	if err := validateLifecycleDurations(request); err != nil {
		return "", LifecycleRequest{}, err
	}
	docker, composeFile, err := preflightInputs(lifecycle.docker, PreflightRequest{WorkspaceRoot: request.WorkspaceRoot, ComposeFile: request.ComposeFile, Services: request.Services})
	if err != nil {
		return "", LifecycleRequest{}, ErrLifecycleInvalid
	}
	workspace, _ := security.CanonicalExistingPath(request.WorkspaceRoot)
	dataDir, err := security.CanonicalExistingPath(request.DataDir)
	if err != nil {
		return "", LifecycleRequest{}, ErrLifecycleInvalid
	}
	overrideFile, err := canonicalContainedFile(dataDir, request.OverrideFile)
	if err != nil {
		return "", LifecycleRequest{}, err
	}
	request.WorkspaceRoot, request.DataDir = workspace, dataDir
	request.ComposeFile, request.OverrideFile = composeFile, overrideFile
	if err := validateLifecycleServices(request, requireResolvedBuild); err != nil {
		return "", LifecycleRequest{}, err
	}
	return docker, request, nil
}

func canonicalContainedFile(root, path string) (string, error) {
	canonical, err := security.CanonicalExistingPath(path)
	if err != nil {
		return "", ErrLifecycleInvalid
	}
	inside, pathErr := security.PathWithinRoot(root, canonical)
	info, statErr := os.Stat(canonical)
	if pathErr != nil || !inside || statErr != nil || !info.Mode().IsRegular() {
		return "", ErrLifecycleInvalid
	}
	return canonical, nil
}

func validateLifecycleServices(request LifecycleRequest, requireResolvedBuild bool) error {
	if len(serviceSet(request.Services)) != len(request.Services) {
		return ErrLifecycleInvalid
	}
	managed := serviceSet(request.Services)
	if (request.BuildPolicy != "always" && request.BuildPolicy != "never" && request.BuildPolicy != "") ||
		(requireResolvedBuild && request.BuildPolicy == "always" && len(request.BuildServices) == 0) ||
		(request.BuildPolicy != "always" && len(request.BuildServices) != 0) {
		return ErrLifecycleInvalid
	}
	for _, name := range request.BuildServices {
		if _, exists := managed[name]; !exists {
			return ErrLifecycleInvalid
		}
	}
	if len(request.Readiness) != 0 && len(request.Readiness) != len(request.Services) {
		return ErrLifecycleInvalid
	}
	for name, requirement := range request.Readiness {
		if _, exists := managed[name]; !exists || (requirement != "healthy" && requirement != "running") {
			return ErrLifecycleInvalid
		}
	}
	document, err := readOverrideDocument(request.OverrideFile)
	if err != nil || len(document.Services) != len(request.Services) {
		return ErrLifecycleInvalid
	}
	for _, name := range request.Services {
		service, exists := document.Services[name]
		if !exists || !reflect.DeepEqual(service.Labels, runtimeLabels(OverrideRequest{
			SystemID: request.SystemID, WorkspaceID: request.WorkspaceID, InstanceID: request.InstanceID,
		}, name)) {
			return ErrLifecycleInvalid
		}
	}
	return nil
}

func projectArguments(identity ProjectIdentity) []string {
	return []string{"compose", "--project-name", identity.ProjectName, "--file", identity.ComposeFile, "--file", identity.OverrideFile}
}

func lifecycleCommandFailure(ctx context.Context, fallback error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return ErrLifecycleTimeout
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return fallback
}
