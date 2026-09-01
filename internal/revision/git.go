package revision

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"stackpilot/internal/security"
)

const (
	defaultGitTimeout = 3 * time.Second
	maxGitOutput      = 256 << 10
)

// GitProbe executes a closed set of bounded, read-only Git identity probes.
type GitProbe struct {
	executable string
	timeout    time.Duration
}

// NewGitProbe resolves and validates the trusted Git executable.
func NewGitProbe(executable string) (*GitProbe, error) {
	explicit := executable != ""
	resolved, err := resolveGitExecutable(executable)
	if err != nil {
		if explicit {
			return nil, err
		}
		return &GitProbe{}, nil
	}
	return &GitProbe{executable: resolved, timeout: defaultGitTimeout}, nil
}

// Collect returns safe Git identity facts for a canonical workspace root.
func (probe *GitProbe) Collect(ctx context.Context, root string) GitFact {
	if probe == nil || probe.executable == "" {
		return GitFact{Status: SourceUnavailable, Reason: "GIT_UNAVAILABLE"}
	}
	canonicalRoot, err := security.CanonicalExistingPath(root)
	if err != nil {
		return GitFact{Status: SourceUnsafe, Reason: "WORKSPACE_PATH_UNSAFE"}
	}
	revision, failure := probe.run(ctx, canonicalRoot, "rev-parse", "--verify", "HEAD")
	if failure != nil {
		return classifyGitFailure(failure)
	}
	branch, branchFailure := probe.run(ctx, canonicalRoot, "symbolic-ref", "--short", "-q", "HEAD")
	if branchFailure != nil && !branchFailure.exitFailure {
		return classifyGitFailure(branchFailure)
	}
	status, failure := probe.run(ctx, canonicalRoot, "status", "--porcelain=v1", "-uno")
	if failure != nil {
		return classifyGitFailure(failure)
	}
	return GitFact{Status: SourceAvailable, Revision: strings.TrimSpace(revision), Branch: strings.TrimSpace(branch), Dirty: strings.TrimSpace(status) != ""}
}

type gitFailure struct {
	err         error
	stderr      string
	exitFailure bool
}

func (probe *GitProbe) run(parent context.Context, root string, arguments ...string) (string, *gitFailure) {
	timeout := probe.timeout
	if timeout <= 0 {
		timeout = defaultGitTimeout
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	fixed := []string{"-c", "core.fsmonitor=false", "-c", "core.untrackedCache=false", "-c", "core.preloadIndex=false"}
	command := exec.CommandContext(ctx, probe.executable, append(fixed, arguments...)...)
	command.Dir = root
	command.Env = gitEnvironment()
	var stdout, stderr boundedBuffer
	stdout.limit, stderr.limit = maxGitOutput, maxGitOutput
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "", &gitFailure{err: ctx.Err(), stderr: stderr.String()}
	}
	if errors.Is(err, errOutputLimit) {
		return "", &gitFailure{err: ErrSourceTooLarge, stderr: stderr.String()}
	}
	if err != nil {
		var exitError *exec.ExitError
		return "", &gitFailure{err: err, stderr: stderr.String(), exitFailure: errors.As(err, &exitError)}
	}
	return stdout.String(), nil
}

func classifyGitFailure(failure *gitFailure) GitFact {
	message := strings.ToLower(failure.stderr)
	switch {
	case strings.Contains(message, "dubious ownership") || strings.Contains(message, "unsafe repository"):
		return GitFact{Status: SourceUnsafe, Reason: "GIT_REPOSITORY_UNSAFE"}
	case strings.Contains(message, "not a git repository") || strings.Contains(message, "unknown revision") || strings.Contains(message, "needed a single revision"):
		return GitFact{Status: SourceNotRepo, Reason: "GIT_NOT_REPOSITORY"}
	case errors.Is(failure.err, context.DeadlineExceeded):
		return GitFact{Status: SourceUnavailable, Reason: "GIT_PROBE_TIMEOUT"}
	case errors.Is(failure.err, ErrSourceTooLarge):
		return GitFact{Status: SourceUnavailable, Reason: "GIT_OUTPUT_TOO_LARGE"}
	default:
		return GitFact{Status: SourceUnavailable, Reason: "GIT_PROBE_FAILED"}
	}
}

func resolveGitExecutable(value string) (string, error) {
	if value == "" {
		name := "git"
		if runtime.GOOS == "windows" {
			name = "git.exe"
		}
		resolved, err := exec.LookPath(name)
		if err != nil {
			return "", err
		}
		value = resolved
	}
	canonical, err := security.CanonicalExistingPath(value)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.Mode().IsRegular() || !strings.EqualFold(filepath.Base(canonical), "git.exe") && runtime.GOOS == "windows" {
		return "", ErrSourceUnsafe
	}
	return canonical, nil
}

func gitEnvironment() []string {
	keys := []string{"SystemRoot", "WINDIR", "TEMP", "TMP"}
	globalConfig := "/dev/null"
	if runtime.GOOS == "windows" {
		globalConfig = "NUL"
	}
	values := []string{"GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + globalConfig, "LC_ALL=C"}
	for _, key := range keys {
		if value, ok := os.LookupEnv(key); ok {
			values = append(values, key+"="+value)
		}
	}
	return values
}

var errOutputLimit = errors.New("command output exceeds its limit")

type boundedBuffer struct {
	buffer bytes.Buffer
	limit  int
}

func (buffer *boundedBuffer) Write(value []byte) (int, error) {
	remaining := buffer.limit - buffer.buffer.Len()
	if remaining <= 0 {
		return 0, errOutputLimit
	}
	if len(value) > remaining {
		_, _ = buffer.buffer.Write(value[:remaining])
		return remaining, errOutputLimit
	}
	return buffer.buffer.Write(value)
}

func (buffer *boundedBuffer) String() string { return buffer.buffer.String() }

var _ io.Writer = (*boundedBuffer)(nil)
