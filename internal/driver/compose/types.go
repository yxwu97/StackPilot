// Package compose implements bounded Docker Compose preflight and lifecycle support.
package compose

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"stackpilot/internal/domain"
)

const (
	minimumDockerVersion  = "24.0.0"
	minimumComposeVersion = "2.20.0"
)

var (
	ErrDockerNotFound            = errors.New("Docker CLI was not found")
	ErrDockerVersionUnsupported  = errors.New("Docker version is unsupported")
	ErrComposeNotFound           = errors.New("Docker Compose v2 was not found")
	ErrComposeVersionUnsupported = errors.New("Docker Compose version is unsupported")
	ErrDaemonUnavailable         = errors.New("Docker daemon is unavailable")
	ErrConfigInvalid             = errors.New("Docker Compose config is invalid")
	ErrBuildConfigInvalid        = errors.New("Docker Compose build config is invalid")
	ErrComposeBuildFailed        = errors.New("Docker Compose build failed")
	ErrComposeBuildTimeout       = errors.New("Docker Compose build timed out")
	ErrPreflightTimeout          = errors.New("Docker Compose preflight timed out")
	ErrOverrideInvalid           = errors.New("Docker Compose override is invalid")
	ErrOverrideConflict          = errors.New("Docker Compose override conflicts with existing output")
	ErrLifecycleInvalid          = errors.New("Docker Compose lifecycle request is invalid")
	ErrProjectIdentityMismatch   = errors.New("Docker Compose project identity mismatch")
	ErrComposeStartFailed        = errors.New("Docker Compose project start failed")
	ErrComposeInspectFailed      = errors.New("Docker Compose project inspection failed")
	ErrComposeStopFailed         = errors.New("Docker Compose project stop failed")
	ErrLifecycleTimeout          = errors.New("Docker Compose lifecycle command timed out")
	ErrProjectNotFound           = errors.New("Docker Compose project was not found")
	ErrLogFollowFailed           = errors.New("Docker Compose log follow failed")
	ErrDiscoveryFailed           = errors.New("Docker Compose project discovery failed")
	ErrPlatformUnsupported       = errors.New("Docker Compose is unsupported on this platform")
	ErrResourceStatsUnavailable  = errors.New("Docker Compose resource stats are unavailable")
)

// CommandOutput holds bounded command streams that are never persisted by preflight.
type CommandOutput struct {
	Stdout []byte
	Stderr []byte
}

// CommandRunner executes one fixed Docker command without a shell.
type CommandRunner func(context.Context, string, []string, string, map[string]string) (CommandOutput, error)

// DockerDesktopStarter starts the trusted local Docker Desktop application.
type DockerDesktopStarter func(context.Context) error

// PreflightFunc verifies the fixed Compose definition before a lifecycle start.
type PreflightFunc func(context.Context, PreflightRequest) (*PreflightResult, error)

// LogProcess is the owned streaming Docker command surface.
type LogProcess interface {
	Wait() error
}

// LogStarter starts one fixed streaming command without a shell.
type LogStarter func(context.Context, string, []string, string, map[string]string, io.Writer, io.Writer) (LogProcess, error)

// Config contains only server-trusted Docker resolution and execution settings.
type Config struct {
	DockerExecutable   string
	Environment        map[string]string
	Timeout            time.Duration
	DaemonPollInterval time.Duration
	Run                CommandRunner
	StartDockerDesktop DockerDesktopStarter
}

// PreflightRequest identifies one canonical workspace Compose definition.
type PreflightRequest struct {
	WorkspaceRoot string
	ComposeFile   string
	Services      []string
	BuildPolicy   string
	Readiness     map[string]string
}

// PreflightResult contains only non-sensitive tool and service identity.
type PreflightResult struct {
	DockerClientVersion string
	DockerServerVersion string
	ComposeVersion      string
	Services            []string
	BuildServices       []string
	Readiness           map[string]string
}

// Preflighter owns immutable server-side Docker command settings.
type Preflighter struct {
	docker             string
	environment        map[string]string
	timeout            time.Duration
	daemonPollInterval time.Duration
	run                CommandRunner
	startDockerDesktop DockerDesktopStarter
	daemonStartMutex   sync.Mutex
}

// LifecycleConfig contains only server-owned Docker execution dependencies.
type LifecycleConfig struct {
	DockerExecutable string
	Environment      map[string]string
	Run              CommandRunner
	Preflight        PreflightFunc
	StartLog         LogStarter
}

// LifecycleRequest contains one resolved immutable Compose project launch.
type LifecycleRequest struct {
	WorkspaceRoot string
	DataDir       string
	ComposeFile   string
	OverrideFile  string
	SystemID      domain.SystemID
	WorkspaceID   domain.WorkspaceID
	InstanceID    domain.SystemInstanceID
	Services      []string
	BuildPolicy   string
	BuildServices []string
	Readiness     map[string]string
	StartTimeout  time.Duration
	StopTimeout   time.Duration
}

// ProjectIdentity is the persisted identity required for later inspection and stop.
type ProjectIdentity struct {
	ProjectName      string
	SystemID         domain.SystemID
	WorkspaceID      domain.WorkspaceID
	InstanceID       domain.SystemInstanceID
	WorkspaceRoot    string
	DataDir          string
	ComposeFile      string
	OverrideFile     string
	Services         []string
	BuildPolicy      string            `json:"BuildPolicy,omitempty"`
	BuildServices    []string          `json:"BuildServices,omitempty"`
	Readiness        map[string]string `json:"Readiness,omitempty"`
	StartTimeout     time.Duration     `json:"StartTimeout,omitempty"`
	StopTimeout      time.Duration
	DefinitionDigest string
}

// ContainerObservation is one bounded structured Compose container status.
type ContainerObservation struct {
	ID       string
	Name     string
	Service  string
	State    string
	Health   string
	ExitCode int
}

// ProjectObservation is the aggregate structured Compose project status.
type ProjectObservation struct {
	State      string
	Containers []ContainerObservation
}

// ContainerResourceObservation is one exact managed-container measurement.
type ContainerResourceObservation struct {
	ID             string
	ComposeService string
	CPUPercent     float64
	MemoryBytes    int64
}

// ResourceObservation aggregates one strictly identified Compose service group.
type ResourceObservation struct {
	ObservedAt  time.Time
	CPUPercent  float64
	MemoryBytes int64
	Containers  []ContainerResourceObservation
}

// HealthObservation is one bounded persistence-safe Compose health result.
type HealthObservation struct {
	CheckedAt time.Time
	Duration  time.Duration
	Ready     bool
	ErrorCode string
	Summary   string
}

// LogFollowRequest binds a verified project to two existing-runtime spool paths.
type LogFollowRequest struct {
	Identity   ProjectIdentity
	StdoutPath string
	StderrPath string
	Since      time.Time
}

// LogSession owns one Compose logs follow command.
type LogSession struct {
	cancel context.CancelFunc
	done   chan struct{}
	err    error
}

// Lifecycle owns fixed Docker Compose start, inspect, and non-destructive stop commands.
type Lifecycle struct {
	docker      string
	environment map[string]string
	run         CommandRunner
	preflight   PreflightFunc
	startLog    LogStarter
}
