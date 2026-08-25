//go:build windows

package runner

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"stackpilot/internal/security"
)

const defaultProbeTimeout = 10 * time.Second

// Resolver resolves enabled Windows process runners.
type Resolver struct {
	environment      map[string]string
	explicit         map[Kind]string
	allowedToolRoots []string
	probeTimeout     time.Duration
	probe            ProbeFunc
}

// NewResolver constructs a resolver from a service-account environment snapshot.
func NewResolver(config Config) (*Resolver, error) {
	environment := normalizedEnvironment(config.Environment)
	if config.Environment == nil {
		environment = normalizedEnvironment(currentEnvironment())
	}
	timeout := config.ProbeTimeout
	if timeout <= 0 {
		timeout = defaultProbeTimeout
	}
	probe := config.Probe
	if probe == nil {
		probe = probeVersion
	}
	return &Resolver{
		environment: environment, explicit: cloneExecutableMap(config.ExplicitExecutables),
		allowedToolRoots: append([]string(nil), config.AllowedToolRoots...), probeTimeout: timeout, probe: probe,
	}, nil
}

// Resolve selects, verifies, hashes, and version-probes one executable.
func (resolver *Resolver) Resolve(ctx context.Context, request ResolveRequest) (*ResolvedCommand, error) {
	if err := validateRequest(request); err != nil {
		return nil, err
	}
	executable, resolution, err := resolver.resolveExecutable(request)
	if err != nil {
		return nil, err
	}
	digest, err := executableDigest(ctx, executable)
	if err != nil {
		return nil, err
	}
	probeContext, cancel := context.WithTimeout(ctx, resolver.probeTimeout)
	defer cancel()
	output, err := resolver.probe(probeContext, request.Runner, executable, resolver.environment)
	if err != nil {
		if errors.Is(probeContext.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("%w: %s", ErrVersionProbeTimeout, request.Runner)
		}
		return nil, fmt.Errorf("%w: %s: %v", ErrVersionProbeFailed, request.Runner, err)
	}
	version, err := parseVersion(request.Runner, output)
	if err != nil {
		return nil, err
	}
	return &ResolvedCommand{
		Executable: executable, Version: version, ResolutionKind: resolution, ExecutableDigest: digest,
	}, nil
}

func validateRequest(request ResolveRequest) error {
	switch request.Runner {
	case Maven, NPM, Java, Node, Go:
		if request.VirtualEnvironment != "" {
			return fmt.Errorf("%w: virtual environment is only valid for python-venv", ErrRunnerPathUnsafe)
		}
	case PythonVenv:
		if !filepath.IsAbs(request.VirtualEnvironment) {
			return fmt.Errorf("%w: virtual environment must be absolute", ErrRunnerPathUnsafe)
		}
	default:
		return fmt.Errorf("%w: %s", ErrRunnerUnsupported, request.Runner)
	}
	if !filepath.IsAbs(request.WorkspaceRoot) || !filepath.IsAbs(request.WorkingDirectory) {
		return fmt.Errorf("%w: workspace paths must be absolute", ErrRunnerPathUnsafe)
	}
	inside, err := security.PathWithinRoot(request.WorkspaceRoot, request.WorkingDirectory)
	if err != nil || !inside {
		return fmt.Errorf("%w: working directory", ErrRunnerPathUnsafe)
	}
	if request.Runner == PythonVenv {
		inside, err = security.PathWithinRoot(request.WorkspaceRoot, request.VirtualEnvironment)
		if err != nil || !inside {
			return fmt.Errorf("%w: virtual environment", ErrRunnerPathUnsafe)
		}
	}
	return nil
}

func (resolver *Resolver) resolveExecutable(request ResolveRequest) (string, ResolutionKind, error) {
	if request.Runner == PythonVenv {
		return resolveVenvExecutable(request)
	}
	if explicit := resolver.explicit[request.Runner]; explicit != "" {
		canonical, err := resolver.trustedExplicitPath(request.WorkspaceRoot, explicit)
		if err != nil {
			return "", "", err
		}
		return canonical, ResolutionExplicit, nil
	}
	if request.Runner == Maven {
		for _, directory := range uniquePaths(request.WorkingDirectory, request.WorkspaceRoot) {
			wrapper, found, err := workspaceExecutable(request.WorkspaceRoot, filepath.Join(directory, "mvnw.cmd"))
			if err != nil {
				return "", "", err
			}
			if found {
				return wrapper, ResolutionWorkspace, nil
			}
		}
	}
	if request.Runner == Java {
		if home := resolver.environment["JAVA_HOME"]; home != "" {
			if executable, found := regularExecutable(filepath.Join(home, "bin", "java.exe")); found {
				return executable, ResolutionPath, nil
			}
		}
	}
	name := map[Kind]string{Maven: "mvn.cmd", NPM: "npm.cmd", Java: "java.exe", Node: "node.exe", Go: "go.exe"}[request.Runner]
	if executable, found := findOnPath(name, resolver.environment["PATH"]); found {
		return executable, ResolutionPath, nil
	}
	return "", "", fmt.Errorf("%w: %s", ErrRunnerNotFound, request.Runner)
}

func resolveVenvExecutable(request ResolveRequest) (string, ResolutionKind, error) {
	venvRoot, err := security.CanonicalExistingPath(request.VirtualEnvironment)
	if err != nil {
		return "", "", fmt.Errorf("%w: python-venv", ErrRunnerNotFound)
	}
	info, err := os.Stat(venvRoot)
	if err != nil || !info.IsDir() {
		return "", "", fmt.Errorf("%w: python-venv", ErrRunnerNotFound)
	}
	inside, err := security.PathWithinRoot(request.WorkspaceRoot, venvRoot)
	if err != nil || !inside {
		return "", "", fmt.Errorf("%w: virtual environment", ErrRunnerPathUnsafe)
	}
	executable, found, err := workspaceExecutable(request.WorkspaceRoot, filepath.Join(venvRoot, "Scripts", "python.exe"))
	if err != nil {
		return "", "", err
	}
	if !found {
		return "", "", fmt.Errorf("%w: python-venv", ErrRunnerNotFound)
	}
	inside, err = security.PathWithinRoot(venvRoot, executable)
	if err != nil || !inside {
		return "", "", fmt.Errorf("%w: python-venv executable", ErrRunnerPathUnsafe)
	}
	return executable, ResolutionVenv, nil
}

func (resolver *Resolver) trustedExplicitPath(workspaceRoot, path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("%w: explicit path must be absolute", ErrRunnerPathUnsafe)
	}
	canonical, found := regularExecutable(path)
	if !found {
		return "", fmt.Errorf("%w: explicit executable", ErrRunnerNotFound)
	}
	for _, root := range append([]string{workspaceRoot}, resolver.allowedToolRoots...) {
		canonicalRoot, err := security.CanonicalExistingPath(root)
		if err != nil {
			continue
		}
		inside, err := security.PathWithinRoot(canonicalRoot, canonical)
		if err == nil && inside {
			return canonical, nil
		}
	}
	return "", fmt.Errorf("%w: explicit executable", ErrRunnerPathUnsafe)
}

func workspaceExecutable(root, candidate string) (string, bool, error) {
	canonical, found := regularExecutable(candidate)
	if !found {
		return "", false, nil
	}
	canonicalRoot, err := security.CanonicalExistingPath(root)
	if err != nil {
		return "", false, fmt.Errorf("%w: workspace root", ErrRunnerPathUnsafe)
	}
	inside, err := security.PathWithinRoot(canonicalRoot, canonical)
	if err != nil || !inside {
		return "", false, fmt.Errorf("%w: Maven Wrapper", ErrRunnerPathUnsafe)
	}
	return canonical, true, nil
}

func regularExecutable(path string) (string, bool) {
	canonical, err := security.CanonicalExistingPath(path)
	if err != nil {
		return "", false
	}
	info, err := os.Stat(canonical)
	return canonical, err == nil && info.Mode().IsRegular()
}

func findOnPath(name, pathValue string) (string, bool) {
	for _, directory := range filepath.SplitList(pathValue) {
		if executable, found := regularExecutable(filepath.Join(directory, name)); found {
			return executable, true
		}
	}
	return "", false
}

func executableDigest(ctx context.Context, path string) (result string, err error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("hash runner executable: %w", err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close runner executable: %w", closeErr)
		}
	}()
	digest := sha256.New()
	buffer := make([]byte, 32*1024)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		count, readErr := file.Read(buffer)
		if count > 0 {
			if _, err := digest.Write(buffer[:count]); err != nil {
				return "", fmt.Errorf("hash runner executable: %w", err)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", fmt.Errorf("read runner executable: %w", readErr)
		}
	}
	return fmt.Sprintf("%x", digest.Sum(nil)), nil
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

func cloneExecutableMap(source map[Kind]string) map[Kind]string {
	result := make(map[Kind]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func uniquePaths(values ...string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		key := strings.ToLower(filepath.Clean(value))
		if _, exists := seen[key]; !exists {
			seen[key] = struct{}{}
			result = append(result, value)
		}
	}
	return result
}
