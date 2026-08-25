// Package runner resolves trusted process runners and performs bounded version preflight.
package runner

import (
	"context"
	"errors"
	"time"
)

// Kind identifies a supported tool family.
type Kind string

const (
	Maven      Kind = "maven"
	NPM        Kind = "npm"
	Java       Kind = "java"
	Node       Kind = "node"
	Go         Kind = "go"
	PythonVenv Kind = "python-venv"
)

// ResolutionKind records how an executable was selected.
type ResolutionKind string

const (
	ResolutionExplicit  ResolutionKind = "explicit"
	ResolutionWorkspace ResolutionKind = "workspace"
	ResolutionPath      ResolutionKind = "path"
	ResolutionVenv      ResolutionKind = "venv"
)

var (
	// ErrRunnerNotFound indicates that no trusted executable matched the requested runner.
	ErrRunnerNotFound = errors.New("runner executable not found")
	// ErrRunnerUnsupported indicates an unknown runner kind.
	ErrRunnerUnsupported = errors.New("runner kind is unsupported")
	// ErrRunnerPathUnsafe indicates an executable outside configured trust roots.
	ErrRunnerPathUnsafe = errors.New("runner executable path is unsafe")
	// ErrVersionProbeFailed indicates a failed or unparseable version command.
	ErrVersionProbeFailed = errors.New("runner version probe failed")
	// ErrVersionProbeTimeout indicates a version command that exceeded its bound.
	ErrVersionProbeTimeout = errors.New("runner version probe timed out")
	// ErrPlatformUnsupported indicates that process resolution is unavailable on this OS.
	ErrPlatformUnsupported = errors.New("runner resolution is unsupported on this platform")
)

// ResolveRequest contains server-resolved workspace paths and the requested runner kind.
type ResolveRequest struct {
	Runner             Kind
	WorkspaceRoot      string
	WorkingDirectory   string
	VirtualEnvironment string
}

// ResolvedCommand is the immutable result used by the process driver.
type ResolvedCommand struct {
	Executable       string
	ArgsPrefix       []string
	Version          string
	ResolutionKind   ResolutionKind
	ExecutableDigest string
}

// ProbeFunc runs the fixed version command and returns bounded output.
type ProbeFunc func(context.Context, Kind, string, map[string]string) (string, error)

// Config controls trusted server-side resolution inputs.
type Config struct {
	Environment         map[string]string
	ExplicitExecutables map[Kind]string
	AllowedToolRoots    []string
	ProbeTimeout        time.Duration
	Probe               ProbeFunc
}
