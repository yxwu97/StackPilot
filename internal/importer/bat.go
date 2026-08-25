package importer

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	maxBATLogicalLines = 4096
	maxBATBlocks       = 256
	maxBATNesting      = 16
	maxBATJumps        = 256
)

var (
	setPattern          = regexp.MustCompile(`(?i)^set\s+"?([A-Z_][A-Z0-9_]*)=(.*)"?$`)
	ifErrorLevelPattern = regexp.MustCompile(`(?i)^if\s+(?:not\s+)?(?:errorlevel\s+[0-9]+|"?%errorlevel%"?\s+(?:equ|neq|lss|leq|gtr|geq|==)\s+[0-9]+)\s+(.+)$`)
	ifExistPattern      = regexp.MustCompile(`(?i)^if\s+(?:not\s+)?exist\s+("[^"]*"|[^\s]+)\s+(.+)$`)
	ifComparePattern    = regexp.MustCompile(`(?i)^if\s+(?:not\s+)?(?:/i\s+)?("[^"]*"|[^\s=]+)\s*==\s*("[^"]*"|[^\s]+)\s+(.+)$`)
	batLabelPattern     = regexp.MustCompile(`(?i)^:([A-Z0-9_.-]+)$`)
	batGotoPattern      = regexp.MustCompile(`(?i)^goto\s+:?([A-Z0-9_.-]+)$`)
)

type commandFact struct {
	runner     string
	workingDir string
	arguments  []string
	path       string
	line       int
}

type batAnalysis struct {
	commands        []commandFact
	references      []referenceFact
	composeProjects []composeProject
	findings        []Finding
	hasCocos        bool
}

type referenceKind string

const (
	referenceBAT referenceKind = "bat"
	referencePS1 referenceKind = "ps1"
)

type referenceFact struct {
	kind       referenceKind
	path       string
	workingDir string
	line       int
}

type batParserState struct {
	variables map[string]string
	working   string
	depth     int
	blocks    int
	labels    map[string]int
	gotos     []batGoto
}

type batGoto struct {
	target string
	line   int
}

func parseBAT(path string, contents []byte, initialWorking string) (batAnalysis, error) {
	lines := strings.Split(strings.ReplaceAll(string(contents), "\r\n", "\n"), "\n")
	if len(lines) > maxBATLogicalLines {
		return batAnalysis{}, ErrScriptTooLarge
	}
	state := batParserState{variables: map[string]string{}, working: initialWorking, labels: map[string]int{}}
	result := batAnalysis{}
	for index, raw := range lines {
		if err := parseBATLine(&result, &state, path, index+1, raw); err != nil {
			return result, err
		}
	}
	if state.depth != 0 {
		return result, fmt.Errorf("%w: unclosed conditional block", ErrScriptUnsupported)
	}
	if err := validateBATControlFlow(state); err != nil {
		return result, err
	}
	sortFindings(result.findings)
	if len(result.commands) == 0 && len(result.references) == 0 && !result.hasCocos {
		return result, emptyBATError(result.findings)
	}
	return result, nil
}

func parseBATLine(result *batAnalysis, state *batParserState, path string, number int, raw string) error {
	line := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), "@"))
	lower := strings.ToLower(line)
	if line == "" || lower == "rem" || strings.HasPrefix(lower, "rem ") || strings.HasPrefix(lower, "rem\t") || strings.HasPrefix(line, "::") {
		return nil
	}
	if handled, err := parseBATStructure(state, line); handled {
		return err
	}
	if handled, err := parseBATControlFlow(state, line, number); handled {
		return err
	}
	if remainder, conditional := parseBATConditional(line); conditional {
		return parseBATConditionalCommand(result, state, path, number, remainder)
	}
	if strings.HasPrefix(lower, "if ") {
		if err := countBATConditional(state); err != nil {
			return err
		}
		if strings.HasSuffix(line, "(") {
			if err := openBATBlock(state); err != nil {
				return err
			}
		}
		result.findings = append(result.findings, blocking("WORKSPACE_SCRIPT_SYNTAX_UNSUPPORTED", "Unsupported BAT conditional syntax was found.", path, number))
		return nil
	}
	if name, value, ok := parseSet(line, state.variables, state.working); ok {
		if state.depth == 0 {
			state.variables[name] = value
		}
		return nil
	}
	expanded := expandBAT(line, state.variables, state.working)
	return parseBATExpandedLine(result, state, path, number, expanded)
}

func parseBATExpandedLine(result *batAnalysis, state *batParserState, path string, number int, expanded string) error {
	if ignoredBATLine(expanded) {
		return nil
	}
	if dangerousBATLine(expanded) {
		result.findings = append(result.findings, blocking("WORKSPACE_SCRIPT_DANGEROUS", "Unsupported command execution syntax was found.", path, number))
		return nil
	}
	if state.depth > 0 {
		result.findings = append(result.findings, blocking("WORKSPACE_SCRIPT_SYNTAX_UNSUPPORTED", "Conditional service or interpreter execution is unsupported.", path, number))
		return nil
	}
	if next, ok := parseDirectory(expanded); ok {
		state.working = next
		return nil
	}
	if reference, ok, dangerous := parseReference(expanded, state.working, number); ok {
		result.references = append(result.references, reference)
		return nil
	} else if dangerous {
		result.findings = append(result.findings, blocking("WORKSPACE_SCRIPT_DANGEROUS", "Unsupported interpreter syntax was found.", path, number))
		return nil
	}
	fact, cocos, ok := parseCommand(expanded, state.working, path, number)
	result.hasCocos = result.hasCocos || cocos
	if ok {
		result.commands = append(result.commands, fact)
	} else if !cocos {
		result.findings = append(result.findings, blocking("WORKSPACE_SCRIPT_SYNTAX_UNSUPPORTED", "Unsupported BAT syntax was found.", path, number))
	}
	return nil
}

func parseBATStructure(state *batParserState, line string) (bool, error) {
	lower := strings.ToLower(line)
	if line == ")" {
		if state.depth == 0 {
			return true, fmt.Errorf("%w: unmatched closing parenthesis", ErrScriptUnsupported)
		}
		state.depth--
		return true, nil
	}
	if lower == ") else (" || lower == ")else(" {
		if state.depth == 0 {
			return true, fmt.Errorf("%w: unmatched else", ErrScriptUnsupported)
		}
		return true, nil
	}
	if strings.HasPrefix(lower, "else") {
		return true, fmt.Errorf("%w: malformed else", ErrScriptUnsupported)
	}
	return false, nil
}

func parseBATConditional(line string) (string, bool) {
	line = strings.TrimSpace(line)
	if match := ifErrorLevelPattern.FindStringSubmatch(line); len(match) == 2 {
		return strings.TrimSpace(match[1]), true
	}
	if match := ifExistPattern.FindStringSubmatch(line); len(match) == 3 && safeBATConditionOperand(match[1], false) {
		return strings.TrimSpace(match[2]), true
	}
	if match := ifComparePattern.FindStringSubmatch(line); len(match) == 4 && safeBATConditionOperand(match[1], true) && safeBATConditionOperand(match[2], true) {
		return strings.TrimSpace(match[3]), true
	}
	return "", false
}

func safeBATConditionOperand(value string, allowEmpty bool) bool {
	value = strings.Trim(value, "\"")
	return (allowEmpty || value != "") && !strings.ContainsAny(value, "&|<>^!`$")
}

func allowedConditionalCommand(line string) bool {
	lower := strings.ToLower(strings.TrimSpace(line))
	return strings.HasPrefix(lower, "echo ") || lower == "pause" || strings.HasPrefix(lower, "exit /b")
}

func parseBATConditionalCommand(result *batAnalysis, state *batParserState, path string, number int, remainder string) error {
	if err := countBATConditional(state); err != nil {
		return err
	}
	if remainder == "(" {
		return openBATBlock(state)
	}
	expanded := expandBAT(remainder, state.variables, state.working)
	if dangerousBATLine(expanded) {
		result.findings = append(result.findings, blocking("WORKSPACE_SCRIPT_DANGEROUS", "Unsupported command execution syntax was found.", path, number))
		return nil
	}
	if handled, err := parseBATControlFlow(state, expanded, number); handled {
		return err
	}
	if _, _, ok := parseSet(expanded, state.variables, state.working); ok || allowedConditionalCommand(expanded) {
		return nil
	}
	result.findings = append(result.findings, blocking("WORKSPACE_SCRIPT_SYNTAX_UNSUPPORTED", "Conditional service or interpreter execution is unsupported.", path, number))
	return nil
}

func countBATConditional(state *batParserState) error {
	state.blocks++
	if state.blocks > maxBATBlocks {
		return ErrScriptTooLarge
	}
	return nil
}

func openBATBlock(state *batParserState) error {
	state.depth++
	if state.depth > maxBATNesting {
		return ErrScriptTooLarge
	}
	return nil
}

func parseBATControlFlow(state *batParserState, line string, number int) (bool, error) {
	if match := batLabelPattern.FindStringSubmatch(strings.TrimSpace(line)); len(match) == 2 {
		label := strings.ToLower(match[1])
		if _, exists := state.labels[label]; exists {
			return true, fmt.Errorf("%w: duplicate label", ErrScriptUnsupported)
		}
		state.labels[label] = number
		return true, nil
	}
	match := batGotoPattern.FindStringSubmatch(strings.TrimSpace(line))
	if len(match) != 2 {
		return false, nil
	}
	if len(state.gotos) >= maxBATJumps {
		return true, ErrScriptTooLarge
	}
	state.gotos = append(state.gotos, batGoto{target: strings.ToLower(match[1]), line: number})
	return true, nil
}

func validateBATControlFlow(state batParserState) error {
	for _, jump := range state.gotos {
		if jump.target == "eof" {
			continue
		}
		line, exists := state.labels[jump.target]
		if !exists {
			return fmt.Errorf("%w: unknown goto label", ErrScriptUnsupported)
		}
		if line <= jump.line {
			return fmt.Errorf("%w: backward goto", ErrReferenceCycle)
		}
	}
	return nil
}

func emptyBATError(findings []Finding) error {
	for _, finding := range findings {
		if finding.Code == "WORKSPACE_SCRIPT_DANGEROUS" {
			return fmt.Errorf("%w: no supported service command", ErrScriptDangerous)
		}
	}
	return fmt.Errorf("%w: no supported service command", ErrScriptUnsupported)
}

func parseReference(line, working string, number int) (referenceFact, bool, bool) {
	parts := splitCommandLine(line)
	if len(parts) == 2 && strings.EqualFold(parts[0], "call") && strings.EqualFold(filepath.Ext(parts[1]), ".bat") {
		if strings.ContainsAny(parts[1], "%$!") {
			return referenceFact{}, false, true
		}
		return referenceFact{kind: referenceBAT, path: parts[1], workingDir: filepath.ToSlash(working), line: number}, true, false
	}
	return parsePowerShellReference(parts, working, number)
}

func parsePowerShellReference(parts []string, working string, number int) (referenceFact, bool, bool) {
	if len(parts) == 0 {
		return referenceFact{}, false, false
	}
	name := strings.ToLower(filepath.Base(parts[0]))
	if name != "powershell" && name != "powershell.exe" && name != "pwsh" && name != "pwsh.exe" {
		return referenceFact{}, false, false
	}
	file := ""
	for index := 1; index < len(parts); index++ {
		switch strings.ToLower(parts[index]) {
		case "-noprofile", "-noninteractive", "-nologo":
		case "-executionpolicy":
			index++
			if index >= len(parts) || !strings.EqualFold(parts[index], "bypass") {
				return referenceFact{}, false, true
			}
		case "-file":
			index++
			if index >= len(parts) || file != "" {
				return referenceFact{}, false, true
			}
			file = parts[index]
		default:
			return referenceFact{}, false, true
		}
	}
	if file == "" || filepath.IsAbs(file) || !strings.EqualFold(filepath.Ext(file), ".ps1") || strings.ContainsAny(file, "%$") {
		return referenceFact{}, false, true
	}
	return referenceFact{kind: referencePS1, path: file, workingDir: filepath.ToSlash(working), line: number}, true, false
}

func ignoredBATLine(line string) bool {
	lower := strings.ToLower(strings.TrimSpace(line))
	return strings.HasPrefix(lower, "echo") || lower == "setlocal" || lower == "endlocal" ||
		strings.HasPrefix(lower, "chcp ") || strings.HasPrefix(lower, "where ") ||
		strings.HasPrefix(lower, "docker --version") || strings.HasPrefix(lower, "docker.exe --version") ||
		strings.HasPrefix(lower, "docker compose version") || strings.HasPrefix(lower, "docker.exe compose version") ||
		strings.HasPrefix(lower, "timeout ") || lower == "pause" || lower == "popd" || strings.HasPrefix(lower, "exit /b")
}

func dangerousBATLine(line string) bool {
	lower := strings.ToLower(line)
	for _, value := range []string{"for /f", "curl ", "wget ", "reg add", "reg delete", "-encodedcommand", "-command"} {
		if strings.Contains(lower, value) {
			return true
		}
	}
	if strings.Contains(lower, "cmd /c") || strings.Contains(lower, "cmd.exe /c") {
		parts := unwrapCommand(splitCommandLine(line))
		if len(parts) == 0 || !looksLikeCommand(parts[0]) {
			return true
		}
	}
	clean := strings.NewReplacer("2>nul", "", ">nul", "", "2>&1", "").Replace(lower)
	return strings.ContainsAny(clean, "|<>&")
}

func parseSet(line string, variables map[string]string, working string) (string, string, bool) {
	match := setPattern.FindStringSubmatch(line)
	if len(match) != 3 {
		return "", "", false
	}
	value := strings.TrimSuffix(match[2], "\"")
	return strings.ToUpper(match[1]), expandBAT(value, variables, working), true
}

func expandBAT(value string, variables map[string]string, working string) string {
	value = strings.ReplaceAll(value, "%~dp0", "."+string(filepath.Separator))
	value = strings.ReplaceAll(value, "%CD%", working)
	for name, replacement := range variables {
		value = strings.ReplaceAll(value, "%"+name+"%", replacement)
	}
	return value
}

func parseDirectory(line string) (string, bool) {
	lower := strings.ToLower(line)
	if !strings.HasPrefix(lower, "cd ") && !strings.HasPrefix(lower, "cd /d ") && !strings.HasPrefix(lower, "pushd ") {
		return "", false
	}
	parts := splitCommandLine(line)
	if len(parts) < 2 {
		return "", false
	}
	return filepath.Clean(parts[len(parts)-1]), true
}

func parseCommand(line, working, path string, number int) (commandFact, bool, bool) {
	parts := unwrapCommand(splitCommandLine(line))
	if len(parts) == 0 {
		return commandFact{}, false, false
	}
	name := strings.ToLower(filepath.Base(parts[0]))
	if strings.Contains(strings.ToLower(line), "--project") && strings.Contains(strings.ToLower(line), "--build") {
		return commandFact{}, true, false
	}
	runner := map[string]string{"node": "node", "node.exe": "node", "npm": "npm", "npm.cmd": "npm", "mvn": "maven", "mvn.cmd": "maven", "mvnw.cmd": "maven", "java": "java", "java.exe": "java", "go": "go", "go.exe": "go"}[name]
	if runner == "" {
		return commandFact{}, false, false
	}
	return commandFact{runner: runner, workingDir: filepath.ToSlash(working), arguments: append([]string(nil), parts[1:]...), path: path, line: number}, false, true
}

func unwrapCommand(parts []string) []string {
	if len(parts) > 1 && strings.EqualFold(parts[0], "call") {
		parts = parts[1:]
	}
	if len(parts) > 1 && strings.EqualFold(parts[0], "start") {
		parts = parts[1:]
		if len(parts) > 0 && !looksLikeCommand(parts[0]) {
			parts = parts[1:]
		}
	}
	if len(parts) > 2 && strings.EqualFold(filepath.Base(parts[0]), "cmd.exe") && strings.EqualFold(parts[1], "/c") {
		parts = splitCommandLine(strings.Join(parts[2:], " "))
	}
	return parts
}

func looksLikeCommand(value string) bool {
	name := strings.ToLower(filepath.Base(value))
	_, ok := map[string]struct{}{"node": {}, "node.exe": {}, "npm": {}, "npm.cmd": {}, "mvn": {}, "mvn.cmd": {}, "mvnw.cmd": {}, "java": {}, "java.exe": {}, "go": {}, "go.exe": {}, "cmd": {}, "cmd.exe": {}}[name]
	return ok
}

func splitCommandLine(value string) []string {
	result := make([]string, 0)
	var token strings.Builder
	quoted := false
	for _, char := range value {
		switch {
		case char == '"':
			quoted = !quoted
		case (char == ' ' || char == '\t') && !quoted:
			if token.Len() > 0 {
				result = append(result, token.String())
				token.Reset()
			}
		default:
			token.WriteRune(char)
		}
	}
	if token.Len() > 0 {
		result = append(result, token.String())
	}
	return result
}

func blocking(code, message, path string, line int) Finding {
	return Finding{Code: code, Severity: "blocking", Message: message, Confidence: Confirmed, Evidence: []Evidence{{Path: filepath.ToSlash(path), Line: line}}}
}

func sortFindings(findings []Finding) {
	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].Code != findings[j].Code {
			return findings[i].Code < findings[j].Code
		}
		left, right := Evidence{}, Evidence{}
		if len(findings[i].Evidence) > 0 {
			left = findings[i].Evidence[0]
		}
		if len(findings[j].Evidence) > 0 {
			right = findings[j].Evidence[0]
		}
		if left.Path != right.Path {
			return left.Path < right.Path
		}
		return left.Line < right.Line
	})
}
