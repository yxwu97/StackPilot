package importer

import (
	"fmt"
	"path/filepath"
	"strings"
)

type composeCallFact struct {
	file       string
	workingDir string
	build      bool
	path       string
	line       int
}

func parsePS1(path string, contents []byte, working string) ([]composeCallFact, error) {
	lines := strings.Split(strings.ReplaceAll(string(contents), "\r\n", "\n"), "\n")
	if len(lines) > maxBATLogicalLines {
		return nil, ErrScriptTooLarge
	}
	result := make([]composeCallFact, 0, 2)
	for index := 0; index < len(lines); index++ {
		line := strings.TrimSpace(lines[index])
		if line == "" || strings.HasPrefix(line, "#") || fixedPS1Assignment(line) || literalWriteHost(line) {
			continue
		}
		fact, ok := parseComposeCall(line, working, path, index+1)
		if !ok {
			return nil, fmt.Errorf("%w: %s:%d", classifyPS1Error(line), path, index+1)
		}
		result = append(result, fact)
		if guardEnd, guarded := parsePS1ExitGuard(lines, index+1); guarded {
			index = guardEnd
		}
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("%w: no Compose command", ErrScriptUnsupported)
	}
	return result, nil
}

func parsePS1ExitGuard(lines []string, start int) (int, bool) {
	if start+2 >= len(lines) || !fixedPS1ExitCondition(strings.TrimSpace(lines[start])) {
		return start, false
	}
	if !literalPS1ExitThrow(strings.TrimSpace(lines[start+1])) || strings.TrimSpace(lines[start+2]) != "}" {
		return start, false
	}
	return start + 2, true
}

func fixedPS1ExitCondition(line string) bool {
	parts := strings.Fields(line)
	return len(parts) == 5 && strings.EqualFold(parts[0], "if") && strings.EqualFold(parts[1], "($LASTEXITCODE") &&
		strings.EqualFold(parts[2], "-ne") && parts[3] == "0)" && parts[4] == "{"
}

func literalPS1ExitThrow(line string) bool {
	if len(line) < len("throw \"\"") || !strings.EqualFold(line[:len("throw ")], "throw ") {
		return false
	}
	value := strings.TrimSpace(line[len("throw "):])
	if len(value) < 2 || (value[0] != '\'' && value[0] != '"') || value[len(value)-1] != value[0] {
		return false
	}
	content := strings.ReplaceAll(strings.ToLower(value[1:len(value)-1]), "$lastexitcode", "")
	return !strings.ContainsAny(content, "$`\"'{};|><&")
}

func fixedPS1Assignment(line string) bool {
	left, right, ok := strings.Cut(line, "=")
	if !ok {
		return false
	}
	left = strings.TrimSpace(left)
	right = strings.Trim(strings.TrimSpace(right), "'\"")
	return (strings.EqualFold(left, "$ErrorActionPreference") && strings.EqualFold(right, "Stop")) ||
		(strings.EqualFold(left, "$ProgressPreference") && strings.EqualFold(right, "SilentlyContinue"))
}

func literalWriteHost(line string) bool {
	if !strings.HasPrefix(strings.ToLower(line), "write-host ") {
		return false
	}
	value := strings.TrimSpace(line[len("write-host "):])
	if len(value) < 2 || strings.Contains(value, "$") {
		return false
	}
	return (value[0] == '\'' && value[len(value)-1] == '\'') || (value[0] == '"' && value[len(value)-1] == '"')
}

func parseComposeCall(line, working, path string, number int) (composeCallFact, bool) {
	if strings.ContainsAny(line, "|><;&{}") || strings.HasPrefix(strings.TrimSpace(line), ". ") || strings.HasPrefix(strings.TrimSpace(line), "& ") {
		return composeCallFact{}, false
	}
	parts := splitCommandLine(line)
	if len(parts) < 4 || !isDockerLiteral(parts[0]) || !strings.EqualFold(parts[1], "compose") {
		return composeCallFact{}, false
	}
	file, actionIndex, ok := composeFileArgument(parts)
	if !ok || filepath.IsAbs(file) || strings.ContainsAny(file, "$%") {
		return composeCallFact{}, false
	}
	action := strings.ToLower(parts[actionIndex])
	arguments := parts[actionIndex+1:]
	switch action {
	case "up":
		if !exactArgumentSet(arguments, "--build", "-d") {
			return composeCallFact{}, false
		}
		return composeCallFact{file: file, workingDir: working, build: true, path: path, line: number}, true
	case "ps":
		if len(arguments) != 0 {
			return composeCallFact{}, false
		}
		return composeCallFact{file: file, workingDir: working, path: path, line: number}, true
	default:
		return composeCallFact{}, false
	}
}

func isDockerLiteral(value string) bool {
	name := strings.ToLower(filepath.Base(value))
	return name == "docker" || name == "docker.exe"
}

func composeFileArgument(parts []string) (string, int, bool) {
	if len(parts) < 4 {
		return "", 0, false
	}
	if strings.EqualFold(parts[2], "-f") || strings.EqualFold(parts[2], "--file") {
		return parts[3], 4, len(parts) > 4
	}
	if value, found := strings.CutPrefix(parts[2], "--file="); found {
		return value, 3, len(parts) > 3
	}
	return "", 0, false
}

func exactArgumentSet(actual []string, expected ...string) bool {
	if len(actual) != len(expected) {
		return false
	}
	seen := make(map[string]bool, len(actual))
	for _, value := range actual {
		seen[strings.ToLower(value)] = true
	}
	for _, value := range expected {
		if !seen[strings.ToLower(value)] {
			return false
		}
	}
	return true
}

func classifyPS1Error(line string) error {
	lower := strings.ToLower(line)
	for _, token := range []string{"invoke-", "start-process", "import-module", "download", "registry", "http://", "https://", "|", ">", "<", "& ", ". "} {
		if strings.Contains(lower, token) {
			return ErrScriptDangerous
		}
	}
	if strings.Contains(line, "$") || strings.ContainsAny(line, "{};") {
		return ErrScriptDangerous
	}
	return ErrScriptUnsupported
}
