//go:build windows

package process

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"stackpilot/internal/domain"
	base "stackpilot/internal/driver"
	"stackpilot/internal/platform/windows/supervisor"
	"stackpilot/internal/security"
)

const maximumStopTimeout = 10 * time.Minute

var environmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type supervisorClient interface {
	Exchange(context.Context, supervisor.MessageType, any, any) error
}

type connector interface {
	connect(context.Context, string) (supervisorClient, supervisor.SupervisorIdentity, error)
}

// Config contains the trusted service-account environment baseline.
type Config struct {
	BaselineEnvironment map[string]string
	connector           connector
}

// Driver implements deterministic Phase 1 process/daemon lifecycle behavior.
type Driver struct {
	baseline  map[string]string
	connector connector
}

var _ base.Driver = (*Driver)(nil)

// New constructs a Process Driver with a copied environment baseline.
func New(config Config) *Driver {
	baseline := config.BaselineEnvironment
	if baseline == nil {
		baseline = currentEnvironment()
	}
	connection := config.connector
	if connection == nil {
		connection = platformConnector()
	}
	return &Driver{baseline: cloneEnvironment(baseline), connector: connection}
}

// Preflight validates a resolved Phase 1 process specification.
func (driver *Driver) Preflight(ctx context.Context, spec base.ResolvedServiceSpec) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := platformCheck(); err != nil {
		return err
	}
	if err := validateEnums(spec); err != nil {
		return err
	}
	if err := validateResolvedPaths(spec); err != nil {
		return err
	}
	if err := validateCommand(ctx, spec); err != nil {
		return err
	}
	environment, err := driver.processEnvironment(spec.Environment)
	if err != nil {
		return err
	}
	if strings.EqualFold(filepath.Ext(spec.Command.Executable), ".cmd") && lookupEnvironment(environment, "COMSPEC") == "" {
		return fmt.Errorf("%w: COMSPEC is required for a command runner", ErrInvalidSpec)
	}
	return nil
}

// Start creates one service through its per-instance Supervisor.
func (driver *Driver) Start(ctx context.Context, request base.StartRequest) (base.RuntimeIdentity, error) {
	if err := driver.Preflight(ctx, request.Spec); err != nil {
		return base.RuntimeIdentity{}, err
	}
	environment, err := driver.processEnvironment(request.Spec.Environment)
	if err != nil {
		return base.RuntimeIdentity{}, err
	}
	client, supervisorIdentity, err := driver.connector.connect(ctx, request.Spec.InstanceDir)
	if err != nil {
		return base.RuntimeIdentity{}, mapSupervisorError(err)
	}
	start := startMessage(request.Spec, environment)
	var status supervisor.ServiceStatus
	if err := client.Exchange(ctx, supervisor.MessageStartService, start, &status); err != nil {
		return base.RuntimeIdentity{}, mapSupervisorError(err)
	}
	return runtimeIdentity(supervisorIdentity, request.Spec.ServiceID, status)
}

// Stop proves ownership, requests graceful termination, then relies on the Supervisor for forced fallback.
func (driver *Driver) Stop(ctx context.Context, request base.StopRequest) error {
	if request.GracefulTimeout < 0 || request.GracefulTimeout > maximumStopTimeout {
		return fmt.Errorf("%w: graceful timeout", ErrInvalidSpec)
	}
	client, token, err := driver.clientForIdentity(ctx, request.Identity)
	if err != nil {
		return err
	}
	status, err := inspectStatus(ctx, client, token.ServiceID)
	if err != nil {
		if errors.Is(err, ErrRuntimeNotFound) {
			if shutdownErr := shutdownIfEmpty(ctx, client); shutdownErr != nil {
				return shutdownErr
			}
		}
		return err
	}
	if err := verifyStatusIdentity(request.Identity, status); err != nil {
		return err
	}
	message := supervisor.StopServiceRequest{
		ServiceID: token.ServiceID, GracefulTimeoutMillis: request.GracefulTimeout.Milliseconds(),
	}
	status = supervisor.ServiceStatus{}
	if err := client.Exchange(ctx, supervisor.MessageStopService, message, &status); err != nil {
		return mapSupervisorError(err)
	}
	if err := verifyStatusIdentity(request.Identity, status); err != nil {
		return err
	}
	return shutdownIfEmpty(ctx, client)
}

func shutdownIfEmpty(ctx context.Context, client supervisorClient) error {
	err := client.Exchange(ctx, supervisor.MessageShutdownIfEmpty, struct{}{}, &struct{}{})
	if err == nil || supervisorStillInUse(err) {
		return nil
	}
	return mapSupervisorError(err)
}

func supervisorStillInUse(err error) bool {
	var remote *supervisor.RemoteError
	return errors.As(err, &remote) && remote.Code == supervisor.ErrorSupervisorNotEmpty
}

// Inspect verifies and reports one currently supervised runtime.
func (driver *Driver) Inspect(ctx context.Context, identity base.RuntimeIdentity) (base.RuntimeObservation, error) {
	client, token, err := driver.clientForIdentity(ctx, identity)
	if err != nil {
		return base.RuntimeObservation{}, err
	}
	status, err := inspectStatus(ctx, client, token.ServiceID)
	if err != nil {
		return base.RuntimeObservation{}, err
	}
	if err := verifyStatusIdentity(identity, status); err != nil {
		return base.RuntimeObservation{}, err
	}
	return base.RuntimeObservation{
		State: status.State, Identity: identity, ExitCode: status.ExitCode, Forced: status.Forced,
	}, nil
}

// ObserveResources verifies the persisted identity and returns full Job Object counters.
func (driver *Driver) ObserveResources(ctx context.Context, identity base.RuntimeIdentity) (base.ResourceObservation, error) {
	client, token, err := driver.clientForIdentity(ctx, identity)
	if err != nil {
		return base.ResourceObservation{}, err
	}
	if token.Supervisor.ProtocolVersion < supervisor.ResourceProtocolVersion {
		return base.ResourceObservation{}, ErrResourceUnsupported
	}
	var observed supervisor.ResourceObservation
	if err := client.Exchange(ctx, supervisor.MessageObserveService,
		supervisor.ServiceRequest{ServiceID: token.ServiceID}, &observed); err != nil {
		return base.ResourceObservation{}, mapSupervisorError(err)
	}
	status := supervisor.ServiceStatus{ServiceID: observed.ServiceID, Identity: observed.Identity}
	if observed.ServiceID != token.ServiceID || verifyStatusIdentity(identity, status) != nil {
		return base.ResourceObservation{}, ErrIdentityMismatch
	}
	if observed.ObservedAt.IsZero() || observed.ObservedAt.Location() != time.UTC || observed.CPUTotalMillis < 0 {
		return base.ResourceObservation{}, ErrSupervisorUnavailable
	}
	return base.ResourceObservation{
		ObservedAt: observed.ObservedAt, CPUTotalMillis: observed.CPUTotalMillis,
		MemoryBytes: observed.MemoryBytes, ActiveProcesses: observed.ActiveProcesses,
	}, nil
}

func inspectStatus(ctx context.Context, client supervisorClient, serviceID string) (supervisor.ServiceStatus, error) {
	var status supervisor.ServiceStatus
	if err := client.Exchange(ctx, supervisor.MessageInspectService,
		supervisor.ServiceRequest{ServiceID: serviceID}, &status); err != nil {
		return supervisor.ServiceStatus{}, mapSupervisorError(err)
	}
	if status.ServiceID != serviceID {
		return supervisor.ServiceStatus{}, ErrIdentityMismatch
	}
	return status, nil
}

// Recover reconnects and performs the same live identity proof as Inspect.
func (driver *Driver) Recover(ctx context.Context, identity base.RuntimeIdentity) (base.RecoveredRuntime, error) {
	observation, err := driver.Inspect(ctx, identity)
	if err != nil {
		return base.RecoveredRuntime{}, err
	}
	return base.RecoveredRuntime{Identity: identity, Observation: observation}, nil
}

// Discover proves a service that started after its database row was created but before identity attachment.
func (driver *Driver) Discover(ctx context.Context, request base.DiscoveryRequest) (base.RecoveredRuntime, error) {
	instanceDir, err := canonicalDirectory(request.InstanceDir)
	if err != nil {
		return base.RecoveredRuntime{}, ErrIdentityMismatch
	}
	if _, err := domain.ParseServiceID(request.ServiceID.String()); err != nil {
		return base.RecoveredRuntime{}, ErrIdentityMismatch
	}
	supervisorIdentity, processIdentity, err := readDiscoveryIdentities(instanceDir, request.ServiceID)
	if err != nil {
		return base.RecoveredRuntime{}, err
	}
	client, err := connectPersistedSupervisor(ctx, supervisorIdentity)
	if err != nil {
		return base.RecoveredRuntime{}, mapSupervisorError(err)
	}
	status, err := inspectStatus(ctx, client, request.ServiceID.String())
	if err != nil {
		return base.RecoveredRuntime{}, err
	}
	if !sameProcessIdentity(processIdentity, status.Identity) {
		return base.RecoveredRuntime{}, ErrIdentityMismatch
	}
	identity, err := runtimeIdentity(supervisorIdentity, request.ServiceID, status)
	if err != nil {
		return base.RecoveredRuntime{}, err
	}
	return base.RecoveredRuntime{
		Identity: identity,
		Observation: base.RuntimeObservation{
			State: status.State, Identity: identity, ExitCode: status.ExitCode, Forced: status.Forced,
		},
	}, nil
}

func readDiscoveryIdentities(instanceDir string, serviceID domain.ServiceID) (supervisor.SupervisorIdentity, supervisor.ProcessIdentity, error) {
	supervisorIdentity, err := supervisor.ReadSupervisorIdentity(filepath.Join(instanceDir, "supervisor.json"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return supervisor.SupervisorIdentity{}, supervisor.ProcessIdentity{}, ErrRuntimeNotFound
		}
		return supervisor.SupervisorIdentity{}, supervisor.ProcessIdentity{}, ErrIdentityMismatch
	}
	serviceIdentity, err := supervisor.ReadProcessIdentity(filepath.Join(instanceDir, "services", serviceID.String(), "identity.json"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return supervisor.SupervisorIdentity{}, supervisor.ProcessIdentity{}, ErrRuntimeNotFound
		}
		return supervisor.SupervisorIdentity{}, supervisor.ProcessIdentity{}, ErrIdentityMismatch
	}
	return supervisorIdentity, serviceIdentity, nil
}

func sameProcessIdentity(expected supervisor.ProcessIdentity, actual *supervisor.ProcessIdentity) bool {
	return actual != nil && expected.PID == actual.PID && expected.CreatedAt.Equal(actual.CreatedAt) &&
		strings.EqualFold(expected.ExecutablePath, actual.ExecutablePath) && expected.AccountSID == actual.AccountSID &&
		expected.CommandDigest == actual.CommandDigest && expected.ProtocolVersion == actual.ProtocolVersion
}

type platformToken struct {
	Supervisor supervisor.SupervisorIdentity `json:"supervisor"`
	ServiceID  string                        `json:"serviceId"`
}

func (driver *Driver) clientForIdentity(ctx context.Context, identity base.RuntimeIdentity) (supervisorClient, platformToken, error) {
	token, err := decodePlatformToken(identity.PlatformToken)
	if err != nil {
		return nil, platformToken{}, err
	}
	client, err := connectPersistedSupervisor(ctx, token.Supervisor)
	if err != nil {
		return nil, platformToken{}, mapSupervisorError(err)
	}
	return client, token, nil
}

func startMessage(spec base.ResolvedServiceSpec, environment map[string]string) supervisor.StartServiceRequest {
	arguments := append(append([]string(nil), spec.Command.ArgsPrefix...), spec.Arguments...)
	return supervisor.StartServiceRequest{
		ServiceID: spec.ServiceID.String(), Executable: spec.Command.Executable, Arguments: arguments,
		WorkingDirectory: spec.WorkingDirectory, Environment: environment, CommandDigest: commandDigest(spec, arguments),
		SecretEnvironmentNames: sortedEnvironmentKeys(spec.SecretReferences),
		StdoutPath:             spec.StdoutPath, StderrPath: spec.StderrPath,
	}
}

func runtimeIdentity(supervisorIdentity supervisor.SupervisorIdentity, serviceID domain.ServiceID, status supervisor.ServiceStatus) (base.RuntimeIdentity, error) {
	if status.Identity == nil || status.ServiceID != serviceID.String() {
		return base.RuntimeIdentity{}, fmt.Errorf("%w: incomplete start identity", ErrSupervisorUnavailable)
	}
	token, err := encodePlatformToken(platformToken{Supervisor: supervisorIdentity, ServiceID: serviceID.String()})
	if err != nil {
		return base.RuntimeIdentity{}, err
	}
	return base.RuntimeIdentity{
		PID: int(status.Identity.PID), StartedAt: status.Identity.CreatedAt.UTC(),
		ExecutablePath: status.Identity.ExecutablePath, CommandDigest: status.Identity.CommandDigest, PlatformToken: token,
	}, nil
}

func verifyStatusIdentity(expected base.RuntimeIdentity, status supervisor.ServiceStatus) error {
	actual := status.Identity
	if actual == nil || int(actual.PID) != expected.PID || !actual.CreatedAt.Equal(expected.StartedAt) ||
		!strings.EqualFold(actual.ExecutablePath, expected.ExecutablePath) || actual.CommandDigest != expected.CommandDigest {
		return ErrIdentityMismatch
	}
	return nil
}

func validateEnums(spec base.ResolvedServiceSpec) error {
	if _, err := domain.ParseServiceID(spec.ServiceID.String()); err != nil || spec.Driver != domain.DriverProcess {
		return fmt.Errorf("%w: service or driver", ErrInvalidSpec)
	}
	if spec.Mode != domain.ProcessDaemon && spec.Mode != domain.ProcessOneshot {
		return fmt.Errorf("%w: process mode", ErrInvalidSpec)
	}
	if spec.GracefulTimeout <= 0 || spec.GracefulTimeout > maximumStopTimeout {
		return fmt.Errorf("%w: graceful timeout", ErrInvalidSpec)
	}
	return nil
}

func validateResolvedPaths(spec base.ResolvedServiceSpec) error {
	workspace, err := canonicalDirectory(spec.WorkspaceRoot)
	if err != nil {
		return err
	}
	working, err := canonicalDirectory(spec.WorkingDirectory)
	if err != nil {
		return err
	}
	inside, err := security.PathWithinRoot(workspace, working)
	if err != nil || !inside {
		return fmt.Errorf("%w: working directory", ErrInvalidSpec)
	}
	instance, err := canonicalDirectory(spec.InstanceDir)
	if err != nil {
		return err
	}
	for _, spool := range []string{spec.StdoutPath, spec.StderrPath} {
		parent, err := canonicalDirectory(filepath.Dir(spool))
		inside, insideErr := security.PathWithinRoot(instance, parent)
		if err != nil || insideErr != nil || !inside || !filepath.IsAbs(spool) {
			return fmt.Errorf("%w: spool path", ErrInvalidSpec)
		}
	}
	return nil
}

func canonicalDirectory(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("%w: path must be absolute", ErrInvalidSpec)
	}
	canonical, err := security.CanonicalExistingPath(path)
	if err != nil {
		return "", fmt.Errorf("%w: canonical path", ErrInvalidSpec)
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("%w: path is not a directory", ErrInvalidSpec)
	}
	return canonical, nil
}

func validateCommand(ctx context.Context, spec base.ResolvedServiceSpec) error {
	if !filepath.IsAbs(spec.Command.Executable) || !isDigest(spec.Command.ExecutableDigest) {
		return fmt.Errorf("%w: resolved command", ErrInvalidSpec)
	}
	if len(spec.Command.ArgsPrefix)+len(spec.Arguments) > 256 {
		return fmt.Errorf("%w: too many arguments", ErrInvalidSpec)
	}
	for _, argument := range append(append([]string(nil), spec.Command.ArgsPrefix...), spec.Arguments...) {
		if strings.IndexByte(argument, 0) >= 0 || len(argument) > 4096 {
			return fmt.Errorf("%w: argument contains NUL", ErrInvalidSpec)
		}
	}
	canonical, err := security.CanonicalExistingPath(spec.Command.Executable)
	if err != nil || !strings.EqualFold(canonical, filepath.Clean(spec.Command.Executable)) {
		return fmt.Errorf("%w: executable path", ErrInvalidSpec)
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("%w: executable is not a regular file", ErrInvalidSpec)
	}
	digest, err := hashExecutable(ctx, canonical)
	if err != nil {
		return err
	}
	if digest != spec.Command.ExecutableDigest {
		return fmt.Errorf("%w: executable digest changed", ErrInvalidSpec)
	}
	return nil
}

func hashExecutable(ctx context.Context, path string) (result string, err error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("%w: open executable", ErrInvalidSpec)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close executable: %w", closeErr))
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
			_, _ = digest.Write(buffer[:count])
		}
		if errors.Is(readErr, io.EOF) {
			return fmt.Sprintf("%x", digest.Sum(nil)), nil
		}
		if readErr != nil {
			return "", fmt.Errorf("%w: read executable", ErrInvalidSpec)
		}
	}
}

func (driver *Driver) processEnvironment(overrides map[string]string) (map[string]string, error) {
	result := make(map[string]string, len(driver.baseline)+len(overrides))
	keys := make(map[string]string, len(driver.baseline)+len(overrides))
	seenBaseline := make(map[string]struct{}, len(driver.baseline))
	for _, name := range sortedEnvironmentKeys(driver.baseline) {
		value := driver.baseline[name]
		if environmentName.MatchString(name) && strings.IndexByte(value, 0) < 0 {
			normalized := strings.ToUpper(name)
			if _, exists := seenBaseline[normalized]; exists {
				return nil, fmt.Errorf("%w: duplicate baseline environment", ErrInvalidSpec)
			}
			seenBaseline[normalized] = struct{}{}
			setEnvironment(result, keys, name, value)
		}
	}
	seenOverrides := make(map[string]struct{}, len(overrides))
	for _, name := range sortedEnvironmentKeys(overrides) {
		value := overrides[name]
		if !environmentName.MatchString(name) || strings.IndexByte(value, 0) >= 0 || len(value) > 16*1024 || reservedEnvironment(name) {
			return nil, fmt.Errorf("%w: environment", ErrInvalidSpec)
		}
		normalized := strings.ToUpper(name)
		if _, exists := seenOverrides[normalized]; exists {
			return nil, fmt.Errorf("%w: duplicate environment", ErrInvalidSpec)
		}
		seenOverrides[normalized] = struct{}{}
		setEnvironment(result, keys, name, value)
	}
	if len(result) > 512 {
		return nil, fmt.Errorf("%w: environment is too large", ErrInvalidSpec)
	}
	return result, nil
}

func setEnvironment(result, keys map[string]string, name, value string) {
	normalized := strings.ToUpper(name)
	if previous := keys[normalized]; previous != "" {
		delete(result, previous)
	}
	keys[normalized] = name
	result[name] = value
}

func reservedEnvironment(name string) bool {
	switch strings.ToUpper(name) {
	case "COMSPEC", "SYSTEMROOT", "WINDIR", "PATH", "PATHEXT":
		return true
	default:
		return false
	}
}

func commandDigest(spec base.ResolvedServiceSpec, arguments []string) string {
	values := append([]string{spec.Command.Executable, spec.Command.ExecutableDigest, spec.WorkingDirectory}, arguments...)
	digest := sha256.New()
	for _, value := range values {
		_, _ = fmt.Fprintf(digest, "%d:%s\x00", len(value), value)
	}
	return fmt.Sprintf("%x", digest.Sum(nil))
}

func encodePlatformToken(token platformToken) (string, error) {
	payload, err := json.Marshal(token)
	if err != nil {
		return "", fmt.Errorf("encode process platform token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func decodePlatformToken(value string) (platformToken, error) {
	payload, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(payload) > 16*1024 {
		return platformToken{}, ErrIdentityMismatch
	}
	var token platformToken
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&token); err != nil || token.ServiceID == "" {
		return platformToken{}, ErrIdentityMismatch
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return platformToken{}, ErrIdentityMismatch
	}
	if _, err := domain.ParseServiceID(token.ServiceID); err != nil {
		return platformToken{}, ErrIdentityMismatch
	}
	if err := supervisor.VerifySupervisorIdentity(token.Supervisor); err != nil {
		if errors.Is(err, supervisor.ErrSupervisorProcessNotFound) {
			return platformToken{}, ErrRuntimeNotFound
		}
		return platformToken{}, ErrIdentityMismatch
	}
	return token, nil
}

func mapSupervisorError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, supervisor.ErrSupervisorProcessNotFound) {
		return ErrRuntimeNotFound
	}
	var remote *supervisor.RemoteError
	if errors.As(err, &remote) {
		switch remote.Code {
		case supervisor.ErrorServiceExists:
			return ErrAlreadyRunning
		case supervisor.ErrorServiceNotFound:
			return ErrRuntimeNotFound
		case supervisor.ErrorIdentityMismatch:
			return ErrIdentityMismatch
		}
	}
	return fmt.Errorf("%w: %v", ErrSupervisorUnavailable, err)
}

func isDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

func currentEnvironment() map[string]string {
	result := make(map[string]string)
	for _, entry := range os.Environ() {
		if key, value, found := strings.Cut(entry, "="); found {
			result[key] = value
		}
	}
	return result
}

func cloneEnvironment(source map[string]string) map[string]string {
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func lookupEnvironment(environment map[string]string, name string) string {
	for key, value := range environment {
		if strings.EqualFold(key, name) {
			return value
		}
	}
	return ""
}

func sortedEnvironmentKeys(environment map[string]string) []string {
	keys := make([]string, 0, len(environment))
	for key := range environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
