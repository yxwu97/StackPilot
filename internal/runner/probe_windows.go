//go:build windows

package runner

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"golang.org/x/sys/windows"
)

const maxProbeOutput = 32 * 1024

var (
	mavenVersionPattern   = regexp.MustCompile(`(?m)^Apache Maven ([^\s]+)`)
	javaVersionPattern    = regexp.MustCompile(`(?m)^(?:openjdk|java) version "([^"]+)"`)
	npmVersionPattern     = regexp.MustCompile(`(?m)^([0-9]+\.[0-9]+\.[0-9]+(?:[-+][^\s]+)?)\s*$`)
	nodeVersionPattern    = regexp.MustCompile(`(?m)^v([0-9]+\.[0-9]+\.[0-9]+(?:[-+][^\s]+)?)\s*$`)
	goVersionPattern      = regexp.MustCompile(`(?m)^go version go([^\s]+)\s+[^\s]+/[^\s]+\s*$`)
	pythonVersionPattern  = regexp.MustCompile(`(?m)^Python ([^\s]+)\s*$`)
	quotedSlashPattern    = regexp.MustCompile(`(\\*)"`)
	trailingSlashPattern  = regexp.MustCompile(`(\\*)$`)
	cmdContentMetaPattern = regexp.MustCompile(`([()%!^<>&|])`)
)

func probeVersion(ctx context.Context, kind Kind, executable string, environment map[string]string) (string, error) {
	arguments := map[Kind][]string{Maven: {"--version"}, NPM: {"--version"}, Java: {"-version"}, Node: {"--version"}, Go: {"version"}, PythonVenv: {"--version"}}[kind]
	command, err := versionCommand(ctx, executable, arguments, environment)
	if err != nil {
		return "", err
	}
	output := &boundedOutput{limit: maxProbeOutput}
	command.Stdout, command.Stderr = output, output
	command.Env = environmentList(environment)
	if err := command.Run(); err != nil {
		return "", fmt.Errorf("execute fixed version command: %w", err)
	}
	return output.String(), nil
}

func versionCommand(ctx context.Context, executable string, arguments []string, environment map[string]string) (*exec.Cmd, error) {
	if !strings.EqualFold(filepath.Ext(executable), ".cmd") {
		return exec.CommandContext(ctx, executable, arguments...), nil
	}
	comspec := environment["COMSPEC"]
	if comspec == "" {
		return nil, fmt.Errorf("COMSPEC is unavailable")
	}
	command := exec.CommandContext(ctx, comspec)
	commandLine, err := BuildCmdCommandLine(comspec, executable, arguments)
	if err != nil {
		return nil, err
	}
	command.SysProcAttr = &windows.SysProcAttr{
		CmdLine:       commandLine,
		HideWindow:    true,
		CreationFlags: windows.CREATE_NO_WINDOW,
	}
	return command, nil
}

// BuildCmdCommandLine creates the fixed COMSPEC /d /s /c command line used by
// the Windows process adapter for trusted .cmd runners.
func BuildCmdCommandLine(comspec, executable string, arguments []string) (string, error) {
	if !filepath.IsAbs(comspec) || !filepath.IsAbs(executable) || !strings.EqualFold(filepath.Ext(executable), ".cmd") {
		return "", fmt.Errorf("COMSPEC and .cmd runner must be absolute paths")
	}
	parts := make([]string, 0, len(arguments)+1)
	parts = append(parts, escapeCmdToken(executable))
	for _, argument := range arguments {
		parts = append(parts, escapeCmdToken(argument))
	}
	inner := strings.Join(parts, " ")
	return windows.EscapeArg(comspec) + ` /d /s /c "` + inner + `"`, nil
}

func escapeCmdToken(value string) string {
	escaped := quotedSlashPattern.ReplaceAllString(value, `$1$1\"`)
	escaped = trailingSlashPattern.ReplaceAllString(escaped, `$1$1`)
	escaped = cmdContentMetaPattern.ReplaceAllString(escaped, `^$1`)
	escaped = cmdContentMetaPattern.ReplaceAllString(escaped, `^$1`)
	return `"` + escaped + `"`
}

func parseVersion(kind Kind, output string) (string, error) {
	pattern := map[Kind]*regexp.Regexp{Maven: mavenVersionPattern, NPM: npmVersionPattern, Java: javaVersionPattern, Node: nodeVersionPattern, Go: goVersionPattern, PythonVenv: pythonVersionPattern}[kind]
	match := pattern.FindStringSubmatch(output)
	if len(match) != 2 {
		return "", fmt.Errorf("%w: %s output was not recognized", ErrVersionProbeFailed, kind)
	}
	return match[1], nil
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

type boundedOutput struct {
	mutex sync.Mutex
	data  bytes.Buffer
	limit int
}

func (output *boundedOutput) Write(value []byte) (int, error) {
	output.mutex.Lock()
	defer output.mutex.Unlock()
	remaining := output.limit - output.data.Len()
	if remaining > 0 {
		if len(value) < remaining {
			remaining = len(value)
		}
		_, _ = output.data.Write(value[:remaining])
	}
	return len(value), nil
}

func (output *boundedOutput) String() string {
	output.mutex.Lock()
	defer output.mutex.Unlock()
	return output.data.String()
}
