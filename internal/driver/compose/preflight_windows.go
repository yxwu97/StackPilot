//go:build windows

package compose

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/windows"

	"stackpilot/internal/security"
)

const (
	defaultPreflightTimeout   = 2 * time.Minute
	defaultDaemonPollInterval = 500 * time.Millisecond
	maxCommandOutput          = 4 << 20
)

var dockerVersionPattern = regexp.MustCompile(`(?m)^Docker version ([^,\s]+)`)

// NewPreflighter constructs a Windows Docker Compose preflight service.
func NewPreflighter(config Config) (*Preflighter, error) {
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = defaultPreflightTimeout
	}
	environment := normalizedEnvironment(config.Environment)
	if config.Environment == nil {
		environment = normalizedEnvironment(currentEnvironment())
	}
	run := config.Run
	if run == nil {
		run = runCommand
	}
	pollInterval := config.DaemonPollInterval
	if pollInterval <= 0 {
		pollInterval = defaultDaemonPollInterval
	}
	startDockerDesktop := config.StartDockerDesktop
	if startDockerDesktop == nil {
		startDockerDesktop = func(ctx context.Context) error {
			return startTrustedDockerDesktop(ctx, environment)
		}
	}
	return &Preflighter{
		docker: config.DockerExecutable, environment: environment, timeout: timeout,
		daemonPollInterval: pollInterval, run: run, startDockerDesktop: startDockerDesktop,
	}, nil
}

// Preflight verifies CLI versions, daemon access, config syntax, and service references.
func (preflight *Preflighter) Preflight(ctx context.Context, request PreflightRequest) (*PreflightResult, error) {
	docker, file, err := preflightInputs(preflight.docker, request)
	if err != nil {
		return nil, err
	}
	preflightContext, cancel := context.WithTimeout(ctx, preflight.timeout)
	defer cancel()
	clientVersion, err := preflight.dockerClientVersion(preflightContext, docker, filepath.Dir(file))
	if err != nil {
		return nil, err
	}
	composeVersion, err := preflight.composeVersion(preflightContext, docker, filepath.Dir(file))
	if err != nil {
		return nil, err
	}
	serverVersion, err := preflight.daemonVersion(preflightContext, docker, filepath.Dir(file))
	if err != nil {
		return nil, err
	}
	services, buildServices, readiness, err := preflight.configServices(preflightContext, docker, file, request)
	if err != nil {
		return nil, err
	}
	return &PreflightResult{
		DockerClientVersion: clientVersion, DockerServerVersion: serverVersion,
		ComposeVersion: composeVersion, Services: services, BuildServices: buildServices, Readiness: readiness,
	}, nil
}

func (preflight *Preflighter) dockerClientVersion(ctx context.Context, docker, directory string) (string, error) {
	output, err := preflight.run(ctx, docker, []string{"--version"}, directory, preflight.environment)
	if err != nil {
		return "", commandFailure(ctx, ErrDockerNotFound)
	}
	match := dockerVersionPattern.FindSubmatch(output.Stdout)
	if len(match) != 2 || !versionAtLeast(string(match[1]), minimumDockerVersion) {
		return "", ErrDockerVersionUnsupported
	}
	return string(match[1]), nil
}

func (preflight *Preflighter) composeVersion(ctx context.Context, docker, directory string) (string, error) {
	output, err := preflight.run(ctx, docker, []string{"compose", "version", "--format", "json"}, directory, preflight.environment)
	if err != nil {
		return "", commandFailure(ctx, ErrComposeNotFound)
	}
	var value struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(output.Stdout, &value) != nil || !versionAtLeast(value.Version, minimumComposeVersion) {
		return "", ErrComposeVersionUnsupported
	}
	return strings.TrimPrefix(value.Version, "v"), nil
}

func (preflight *Preflighter) daemonVersion(ctx context.Context, docker, directory string) (string, error) {
	version, err := preflight.daemonVersionOnce(ctx, docker, directory)
	if err == nil || !errors.Is(err, ErrDaemonUnavailable) {
		return version, err
	}
	return preflight.startAndWaitForDaemon(ctx, docker, directory)
}

func (preflight *Preflighter) startAndWaitForDaemon(ctx context.Context, docker, directory string) (string, error) {
	preflight.daemonStartMutex.Lock()
	defer preflight.daemonStartMutex.Unlock()
	if version, err := preflight.daemonVersionOnce(ctx, docker, directory); err == nil || !errors.Is(err, ErrDaemonUnavailable) {
		return version, err
	}
	if err := preflight.startDockerDesktop(ctx); err != nil {
		return "", commandFailure(ctx, ErrDaemonUnavailable)
	}
	ticker := time.NewTicker(preflight.daemonPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return "", commandFailure(ctx, ErrDaemonUnavailable)
		case <-ticker.C:
			version, err := preflight.daemonVersionOnce(ctx, docker, directory)
			if err == nil || !errors.Is(err, ErrDaemonUnavailable) {
				return version, err
			}
		}
	}
}

func (preflight *Preflighter) daemonVersionOnce(ctx context.Context, docker, directory string) (string, error) {
	output, runErr := preflight.run(ctx, docker, []string{"version", "--format", "{{json .}}"}, directory, preflight.environment)
	var value struct {
		Server *struct {
			Version string `json:"Version"`
		} `json:"Server"`
	}
	if json.Unmarshal(output.Stdout, &value) != nil || value.Server == nil {
		return "", commandFailure(ctx, ErrDaemonUnavailable)
	}
	if runErr != nil {
		return "", commandFailure(ctx, ErrDaemonUnavailable)
	}
	if !versionAtLeast(value.Server.Version, minimumDockerVersion) {
		return "", ErrDockerVersionUnsupported
	}
	return value.Server.Version, nil
}

func startTrustedDockerDesktop(ctx context.Context, environment map[string]string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	executable, err := resolveDockerDesktop(environment)
	if err != nil {
		return err
	}
	command := exec.Command(executable)
	command.Env = environmentList(environment)
	command.SysProcAttr = &windows.SysProcAttr{
		HideWindow: true, CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP,
	}
	if err := command.Start(); err != nil {
		return ErrDaemonUnavailable
	}
	if err := command.Process.Release(); err != nil {
		return ErrDaemonUnavailable
	}
	return nil
}

func resolveDockerDesktop(environment map[string]string) (string, error) {
	candidates := []struct {
		root     string
		relative string
	}{
		{root: environment["PROGRAMFILES"], relative: filepath.Join("Docker", "Docker", "Docker Desktop.exe")},
		{root: environment["LOCALAPPDATA"], relative: filepath.Join("Programs", "Docker", "Docker", "Docker Desktop.exe")},
	}
	for _, candidate := range candidates {
		if executable, ok := trustedDockerDesktopCandidate(candidate.root, candidate.relative); ok {
			return executable, nil
		}
	}
	return "", ErrDaemonUnavailable
}

func trustedDockerDesktopCandidate(root, relative string) (string, bool) {
	if root == "" {
		return "", false
	}
	canonicalRoot, err := security.CanonicalExistingPath(root)
	if err != nil {
		return "", false
	}
	executable, err := security.CanonicalExistingPath(filepath.Join(canonicalRoot, relative))
	if err != nil {
		return "", false
	}
	inside, err := security.PathWithinRoot(canonicalRoot, executable)
	info, statErr := os.Stat(executable)
	return executable, err == nil && inside && statErr == nil && info.Mode().IsRegular()
}

func (preflight *Preflighter) configServices(ctx context.Context, docker, file string, request PreflightRequest) ([]string, []string, map[string]string, error) {
	arguments := []string{"compose", "--file", file, "config", "--format", "json", "--no-interpolate"}
	output, err := preflight.run(ctx, docker, arguments, filepath.Dir(file), preflight.environment)
	if err != nil {
		return nil, nil, nil, commandFailure(ctx, ErrConfigInvalid)
	}
	var value struct {
		Services map[string]json.RawMessage `json:"services"`
	}
	if json.Unmarshal(output.Stdout, &value) != nil || len(value.Services) == 0 {
		return nil, nil, nil, ErrConfigInvalid
	}
	policy := request.BuildPolicy
	if policy == "" {
		policy = "never"
	}
	readiness := cloneRequirements(request.Readiness, request.Services)
	buildServices := make([]string, 0)
	for _, service := range request.Services {
		definition, exists := value.Services[service]
		build, validateErr := validateManagedComposeService(definition, request, filepath.Dir(file), service, readiness[service])
		if !exists || validateErr != nil {
			return nil, nil, nil, validateErr
		}
		if build {
			buildServices = append(buildServices, service)
		}
	}
	if (policy == "always") != (len(buildServices) > 0) || (policy != "never" && policy != "always") {
		return nil, nil, nil, ErrBuildConfigInvalid
	}
	services := append([]string(nil), request.Services...)
	sort.Strings(services)
	sort.Strings(buildServices)
	return services, buildServices, readiness, nil
}

type managedComposeService struct {
	Privileged  bool                       `json:"privileged"`
	Entrypoint  json.RawMessage            `json:"entrypoint"`
	Build       json.RawMessage            `json:"build"`
	Healthcheck json.RawMessage            `json:"healthcheck"`
	DependsOn   map[string]json.RawMessage `json:"depends_on"`
	Volumes     []struct {
		Type   string `json:"type"`
		Source string `json:"source"`
	} `json:"volumes"`
}

func validateManagedComposeService(encoded json.RawMessage, request PreflightRequest, directory, name, requirement string) (bool, error) {
	var service managedComposeService
	if json.Unmarshal(encoded, &service) != nil || service.Privileged || rawConfigured(service.Entrypoint) {
		return false, ErrConfigInvalid
	}
	managed := serviceSet(request.Services)
	for dependency := range service.DependsOn {
		if _, exists := managed[dependency]; !exists {
			return false, ErrConfigInvalid
		}
	}
	for _, volume := range service.Volumes {
		if volume.Type == "bind" && isHostRoot(volume.Source) {
			return false, ErrConfigInvalid
		}
	}
	if err := validateComposeRequirement(requirement, service.Healthcheck); err != nil {
		return false, err
	}
	if !rawConfigured(service.Build) {
		return false, nil
	}
	if request.BuildPolicy != "always" {
		return false, ErrBuildConfigInvalid
	}
	if err := validateResolvedBuild(request.WorkspaceRoot, directory, service.Build); err != nil {
		return false, fmt.Errorf("%w: service %s", ErrBuildConfigInvalid, name)
	}
	return true, nil
}

func validateComposeRequirement(requirement string, healthcheck json.RawMessage) error {
	hasHealth := rawConfigured(healthcheck)
	switch requirement {
	case "healthy":
		if !hasHealth {
			return ErrConfigInvalid
		}
	case "running":
		if hasHealth {
			return ErrConfigInvalid
		}
	default:
		return ErrConfigInvalid
	}
	return nil
}

func validateResolvedBuild(root, directory string, encoded json.RawMessage) error {
	var value map[string]json.RawMessage
	if json.Unmarshal(encoded, &value) != nil || len(value) == 0 {
		return ErrBuildConfigInvalid
	}
	for key := range value {
		if key != "context" && key != "dockerfile" {
			return ErrBuildConfigInvalid
		}
	}
	contextPath, err := resolvedBuildPath(root, directory, value["context"], true)
	if err != nil {
		return err
	}
	dockerfile := value["dockerfile"]
	if len(dockerfile) == 0 {
		dockerfile = json.RawMessage(`"Dockerfile"`)
	}
	_, err = resolvedBuildPath(root, contextPath, dockerfile, false)
	return err
}

func resolvedBuildPath(root, base string, encoded json.RawMessage, directory bool) (string, error) {
	var value string
	if json.Unmarshal(encoded, &value) != nil || value == "" || remoteBuildContext(value) {
		return "", ErrBuildConfigInvalid
	}
	path := value
	if !filepath.IsAbs(path) {
		path = filepath.Join(base, filepath.FromSlash(path))
	}
	canonical, err := security.CanonicalExistingPath(path)
	if err != nil {
		return "", ErrBuildConfigInvalid
	}
	inside, pathErr := security.PathWithinRoot(root, canonical)
	info, statErr := os.Stat(canonical)
	if pathErr != nil || !inside || statErr != nil || (directory && !info.IsDir()) || (!directory && !info.Mode().IsRegular()) {
		return "", ErrBuildConfigInvalid
	}
	return canonical, nil
}

func remoteBuildContext(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(value))
	return strings.Contains(lower, "://") || strings.HasPrefix(lower, "git@")
}

func rawConfigured(value json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(value))
	return trimmed != "" && trimmed != "null" && trimmed != "[]" && trimmed != `""` && trimmed != "{}"
}

func isHostRoot(path string) bool {
	clean := filepath.Clean(path)
	if clean == string(filepath.Separator) || clean == "/" {
		return true
	}
	volume := filepath.VolumeName(clean)
	return volume != "" && strings.EqualFold(clean, volume+string(filepath.Separator))
}

func preflightInputs(configuredDocker string, request PreflightRequest) (string, string, error) {
	docker, err := resolveDocker(configuredDocker)
	if err != nil {
		return "", "", err
	}
	root, err := security.CanonicalExistingPath(request.WorkspaceRoot)
	if err != nil {
		return "", "", ErrConfigInvalid
	}
	file, err := security.CanonicalExistingPath(request.ComposeFile)
	if err != nil {
		return "", "", ErrConfigInvalid
	}
	inside, err := security.PathWithinRoot(root, file)
	info, statErr := os.Stat(file)
	if err != nil || !inside || statErr != nil || !info.Mode().IsRegular() || len(request.Services) == 0 {
		return "", "", ErrConfigInvalid
	}
	return docker, file, nil
}

func resolveDocker(configured string) (string, error) {
	path := configured
	if path == "" {
		var err error
		path, err = exec.LookPath("docker.exe")
		if err != nil {
			return "", ErrDockerNotFound
		}
	}
	if !filepath.IsAbs(path) {
		return "", ErrDockerNotFound
	}
	canonical, err := security.CanonicalExistingPath(path)
	info, statErr := os.Stat(canonical)
	if err != nil || statErr != nil || !info.Mode().IsRegular() {
		return "", ErrDockerNotFound
	}
	return canonical, nil
}

func runCommand(ctx context.Context, executable string, arguments []string, directory string, environment map[string]string) (CommandOutput, error) {
	command := exec.CommandContext(ctx, executable, arguments...)
	command.Dir = directory
	command.Env = environmentList(environment)
	command.SysProcAttr = &windows.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	stdout, stderr := newLimitedBuffer(maxCommandOutput), newLimitedBuffer(maxCommandOutput)
	command.Stdout, command.Stderr = stdout, stderr
	err := command.Run()
	return CommandOutput{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}, err
}

func commandFailure(ctx context.Context, fallback error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return ErrPreflightTimeout
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return fallback
}

func versionAtLeast(value, minimum string) bool {
	actual, ok := parseVersion(value)
	if !ok {
		return false
	}
	required, _ := parseVersion(minimum)
	for index := range actual {
		if actual[index] != required[index] {
			return actual[index] > required[index]
		}
	}
	return true
}

func parseVersion(value string) ([3]int, bool) {
	value = strings.TrimPrefix(strings.TrimSpace(value), "v")
	parts := strings.SplitN(value, ".", 3)
	if len(parts) != 3 {
		return [3]int{}, false
	}
	patchEnd := strings.IndexFunc(parts[2], func(r rune) bool { return r < '0' || r > '9' })
	if patchEnd == 0 {
		return [3]int{}, false
	}
	if patchEnd > 0 {
		parts[2] = parts[2][:patchEnd]
	}
	var result [3]int
	for index, part := range parts {
		parsed, err := strconv.Atoi(part)
		if err != nil {
			return [3]int{}, false
		}
		result[index] = parsed
	}
	return result, true
}

func normalizedEnvironment(source map[string]string) map[string]string {
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[strings.ToUpper(key)] = value
	}
	return result
}

func currentEnvironment() map[string]string {
	result := make(map[string]string)
	for _, entry := range os.Environ() {
		key, value, found := strings.Cut(entry, "=")
		if found {
			result[key] = value
		}
	}
	return result
}

func environmentList(environment map[string]string) []string {
	keys := make([]string, 0, len(environment))
	for key := range environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+environment[key])
	}
	return result
}

type limitedBuffer struct {
	buffer bytes.Buffer
	limit  int
}

func newLimitedBuffer(limit int) *limitedBuffer { return &limitedBuffer{limit: limit} }

func (buffer *limitedBuffer) Write(value []byte) (int, error) {
	remaining := buffer.limit - buffer.buffer.Len()
	if remaining <= 0 {
		return len(value), nil
	}
	if len(value) > remaining {
		_, _ = buffer.buffer.Write(value[:remaining])
		return len(value), nil
	}
	_, _ = buffer.buffer.Write(value)
	return len(value), nil
}

func (buffer *limitedBuffer) Bytes() []byte { return append([]byte(nil), buffer.buffer.Bytes()...) }
