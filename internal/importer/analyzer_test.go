package importer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestAnalyzeControlledComposeSourceGraph(t *testing.T) {
	root := composeImportFixture(t)
	analyzer, err := NewAnalyzer()
	if err != nil {
		t.Fatal(err)
	}
	draft, err := analyzer.Analyze(context.Background(), root, "start.bat")
	if err != nil {
		t.Fatal(err)
	}
	if len(draft.Candidates) != 1 {
		t.Fatalf("candidate count = %d", len(draft.Candidates))
	}
	candidate := draft.Candidates[0]
	if candidate.Applyable || len(candidate.Services) != 1 || candidate.Services[0].Driver != "compose" {
		t.Fatalf("Compose candidate = %#v", candidate)
	}
	compose := candidate.Services[0].Compose
	if compose == nil || compose.BuildPolicy != "always" || len(compose.Services) != 5 || len(compose.BuildServices) != 3 {
		t.Fatalf("Compose summary = %#v", compose)
	}
	if compose.Readiness["job"] != "running" || compose.Readiness["gateway"] != "running" || compose.Readiness["mysql"] != "healthy" {
		t.Fatalf("readiness = %#v", compose.Readiness)
	}
	if len(candidate.Ports) != 1 || candidate.Ports[0].Preferred != 8443 || candidate.Manifest.Spec.Ports["gateway"].Exposure != "loopback" {
		t.Fatalf("ports = %#v manifest=%#v", candidate.Ports, candidate.Manifest.Spec.Ports)
	}
	writeFixture(t, filepath.Join(root, "web", "Dockerfile"), "FROM scratch\n# changed\n")
	if err := analyzer.VerifySource(context.Background(), root, "start.bat", draft.SourceDigest); !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("VerifySource(Dockerfile changed) = %v", err)
	}
}

func TestBATAndPS1StructuralLimitsAndDangerousPrecedence(t *testing.T) {
	if _, err := parseBAT("bad.bat", []byte(")\n"), "."); !errors.Is(err, ErrScriptUnsupported) {
		t.Fatalf("unmatched BAT block error = %v", err)
	}
	if _, err := parseBAT("bad.bat", []byte("powershell.exe -EncodedCommand ZQBjAGgAbwA=\n"), "."); !errors.Is(err, ErrScriptDangerous) {
		t.Fatalf("dangerous BAT error = %v", err)
	}
	if _, err := parsePS1("bad.ps1", []byte("Start-Process docker\n"), "."); !errors.Is(err, ErrScriptDangerous) {
		t.Fatalf("dangerous PS1 error = %v", err)
	}
	for name, script := range map[string]string{
		"different variable": "docker compose -f compose.yaml ps\nif ($OTHER -ne 0) {\n  throw \"failed\"\n}\n",
		"extra command":      "docker compose -f compose.yaml ps\nif ($LASTEXITCODE -ne 0) {\n  Start-Process docker\n}\n",
		"command expansion":  "docker compose -f compose.yaml ps\nif ($LASTEXITCODE -ne 0) {\n  throw \"$(Invoke-WebRequest https://example.invalid)\"\n}\n",
		"isolated guard":     "if ($LASTEXITCODE -ne 0) {\n  throw \"failed: $LASTEXITCODE\"\n}\ndocker compose -f compose.yaml ps\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parsePS1("bad.ps1", []byte(script), "."); !errors.Is(err, ErrScriptDangerous) {
				t.Fatalf("parsePS1() error = %v, want dangerous", err)
			}
		})
	}
}

func TestBATCommentsUseExactREMToken(t *testing.T) {
	for _, comment := range []string{"REM", "rem comment", "ReM\tcomment"} {
		analysis, err := parseBAT("run.bat", []byte(comment+"\nnode tools/serve.js\n"), ".")
		if err != nil || len(analysis.findings) != 0 {
			t.Fatalf("comment %q = (%#v, %v)", comment, analysis.findings, err)
		}
	}

	analysis, err := parseBAT("run.bat", []byte("REMARK\nnode tools/serve.js\n"), ".")
	if err != nil || !hasFinding(analysis.findings, "WORKSPACE_SCRIPT_SYNTAX_UNSUPPORTED") {
		t.Fatalf("REMARK = (%#v, %v)", analysis.findings, err)
	}
}

func TestBATAcceptsNegatedSavedErrorLevelGuard(t *testing.T) {
	contents := "powershell.exe -NoProfile -File scripts\\dev-up.ps1\r\n" +
		"set \"START_EXIT_CODE=%ERRORLEVEL%\"\r\n" +
		"if not \"%START_EXIT_CODE%\"==\"0\" (\r\n  echo failed\r\n  pause\r\n  exit /b %START_EXIT_CODE%\r\n)\r\n"
	analysis, err := parseBAT("start.bat", []byte(contents), ".")
	if err != nil {
		t.Fatal(err)
	}
	if hasFinding(analysis.findings, "WORKSPACE_SCRIPT_SYNTAX_UNSUPPORTED") {
		t.Fatalf("saved errorlevel guard produced syntax blocker: %#v", analysis.findings)
	}
}

func TestAnalyzeWFGameControlFlowFixture(t *testing.T) {
	root := nodeFixture(t, `server.listen(port, () => {});`)
	writeFixture(t, filepath.Join(root, "tools", "check-build-freshness.js"), "process.exit(0);\n")
	writeFixture(t, filepath.Join(root, "run.bat"), wfGameControlFlowFixture())
	analyzer, _ := NewAnalyzer()
	draft, err := analyzer.Analyze(context.Background(), root, "run.bat")
	if err != nil {
		t.Fatal(err)
	}
	if len(draft.Candidates) != 2 || draft.Candidates[0].ID != "serve-existing" || draft.Candidates[1].ID != "build-and-serve" {
		t.Fatalf("WFGame fixture candidates = %#v", draft.Candidates)
	}
	for _, candidate := range draft.Candidates {
		for _, finding := range candidate.Findings {
			if finding.Code == "WORKSPACE_SCRIPT_SYNTAX_UNSUPPORTED" {
				t.Fatalf("safe control flow produced syntax blocker: %#v", finding)
			}
		}
	}
}

func TestBATControlFlowRejectsUnsafeBranchesAndJumps(t *testing.T) {
	t.Run("conditional service", func(t *testing.T) {
		analysis, err := parseBAT("run.bat", []byte("if exist ready.txt node tools/serve.js\nnode tools/serve.js\n"), ".")
		if err != nil || !hasFinding(analysis.findings, "WORKSPACE_SCRIPT_SYNTAX_UNSUPPORTED") {
			t.Fatalf("conditional service = (%#v, %v)", analysis.findings, err)
		}
	})
	t.Run("dangerous block precedence", func(t *testing.T) {
		analysis, err := parseBAT("run.bat", []byte("if exist ready.txt (\ncurl https://example.invalid/payload\n)\nnode tools/serve.js\n"), ".")
		if err != nil || !hasFinding(analysis.findings, "WORKSPACE_SCRIPT_DANGEROUS") {
			t.Fatalf("dangerous block = (%#v, %v)", analysis.findings, err)
		}
	})
	t.Run("backward jump", func(t *testing.T) {
		_, err := parseBAT("run.bat", []byte(":loop\ngoto loop\nnode tools/serve.js\n"), ".")
		if !errors.Is(err, ErrReferenceCycle) {
			t.Fatalf("backward goto error = %v", err)
		}
	})
	t.Run("unknown jump", func(t *testing.T) {
		_, err := parseBAT("run.bat", []byte("goto missing\nnode tools/serve.js\n"), ".")
		if !errors.Is(err, ErrScriptUnsupported) {
			t.Fatalf("unknown goto error = %v", err)
		}
	})
	t.Run("dynamic jump", func(t *testing.T) {
		analysis, err := parseBAT("run.bat", []byte("goto %TARGET%\nnode tools/serve.js\n"), ".")
		if err != nil || !hasFinding(analysis.findings, "WORKSPACE_SCRIPT_SYNTAX_UNSUPPORTED") {
			t.Fatalf("dynamic goto = (%#v, %v)", analysis.findings, err)
		}
	})
}

func TestProbeAndAnalyzeSafeNodeWorkspace(t *testing.T) {
	root := nodeFixture(t, `server.listen(port, '127.0.0.1', () => {});`)
	analyzer, err := NewAnalyzer()
	if err != nil {
		t.Fatal(err)
	}
	probe, err := analyzer.Probe(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if probe.State != StateInitializationNeeded || len(probe.Candidates) != 1 {
		t.Fatalf("probe = %#v", probe)
	}
	draft, err := analyzer.Analyze(context.Background(), root, "run.bat")
	if err != nil {
		t.Fatal(err)
	}
	if len(draft.Candidates) != 1 || !draft.Candidates[0].Applyable {
		t.Fatalf("draft = %#v", draft)
	}
	if got := draft.Candidates[0].Ports[0].Preferred; got != 7460 {
		t.Fatalf("port = %d", got)
	}
	if draft.Candidates[0].Manifest.Metadata.ID != "safe-game" {
		t.Fatalf("system ID = %q", draft.Candidates[0].Manifest.Metadata.ID)
	}
}

func TestProbeScansShallowBATBeforeDeepEntryLimit(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, filepath.Join(root, "start.bat"), "@echo off\r\n")
	for index := 0; index < 4; index++ {
		writeFixture(t, filepath.Join(root, "a-cache", "deep", fmt.Sprintf("entry-%d.txt", index)), "cache")
	}

	candidates, err := scanScriptsWithLimits(context.Background(), root, 3, maxScanDepth)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].Path != "start.bat" {
		t.Fatalf("candidates = %#v, want shallow start.bat", candidates)
	}
}

func TestProbePreservesScanDepthLimit(t *testing.T) {
	root := t.TempDir()
	boundary := filepath.Join(root, "one", "two", "three", "four", "five")
	writeFixture(t, filepath.Join(boundary, "boundary.bat"), "@echo off\r\n")
	writeFixture(t, filepath.Join(boundary, "six", "too-deep.bat"), "@echo off\r\n")

	candidates, err := scanScriptsWithLimits(context.Background(), root, 100, maxScanDepth)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].Path != "one/two/three/four/five/boundary.bat" {
		t.Fatalf("candidates = %#v, want only depth-boundary BAT", candidates)
	}
}

func TestAnalyzeBlocksNonLoopbackNodeServer(t *testing.T) {
	root := nodeFixture(t, `server.listen(port, () => {});`)
	analyzer, _ := NewAnalyzer()
	draft, err := analyzer.Analyze(context.Background(), root, "run.bat")
	if err != nil {
		t.Fatal(err)
	}
	candidate := draft.Candidates[0]
	if candidate.Applyable || len(candidate.Blockers()) == 0 {
		t.Fatalf("unsafe candidate = %#v", candidate)
	}
}

func TestVerifySourceDetectsChange(t *testing.T) {
	root := nodeFixture(t, `server.listen(port, '127.0.0.1');`)
	analyzer, _ := NewAnalyzer()
	draft, err := analyzer.Analyze(context.Background(), root, "run.bat")
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(root, "run.bat"), "@echo off\nnode tools/serve.js build\nrem changed\n")
	if err := analyzer.VerifySource(context.Background(), root, "run.bat", draft.SourceDigest); err != ErrSourceChanged {
		t.Fatalf("VerifySource() = %v", err)
	}
}

func TestAnalyzeNestedBATAndTrustedRunnerCandidates(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, filepath.Join(root, "package.json"), `{"name":"multi-service"}`)
	writeFixture(t, filepath.Join(root, "run.bat"), "@echo off\r\ncall scripts\\services.bat\r\n")
	writeFixture(t, filepath.Join(root, "scripts", "services.bat"), "@echo off\r\nnpm.cmd run dev\r\nmvn.cmd spring-boot:run\r\njava.exe -jar app.jar\r\n")
	analyzer, _ := NewAnalyzer()
	draft, err := analyzer.Analyze(context.Background(), root, "run.bat")
	if err != nil {
		t.Fatal(err)
	}
	if len(draft.Candidates) != 1 || len(draft.Candidates[0].Services) != 3 || !draft.Candidates[0].Applyable {
		t.Fatalf("trusted runner draft = %#v", draft)
	}
	writeFixture(t, filepath.Join(root, "scripts", "services.bat"), "npm.cmd run changed\r\n")
	if err := analyzer.VerifySource(context.Background(), root, "run.bat", draft.SourceDigest); err != ErrSourceChanged {
		t.Fatalf("nested VerifySource() = %v", err)
	}
}

func TestAnalyzeGoRunnerCandidate(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, filepath.Join(root, "go.mod"), "module example.test/service\n\ngo 1.26.0\n")
	writeFixture(t, filepath.Join(root, "run.bat"), "@echo off\r\ngo run ./cmd/service\r\n")
	if err := os.MkdirAll(filepath.Join(root, "cmd", "service"), 0o700); err != nil {
		t.Fatal(err)
	}
	analyzer, _ := NewAnalyzer()
	draft, err := analyzer.Analyze(context.Background(), root, "run.bat")
	if err != nil {
		t.Fatal(err)
	}
	if len(draft.Candidates) != 1 || len(draft.Candidates[0].Services) != 1 ||
		draft.Candidates[0].Services[0].Runner != "go" ||
		!slices.Contains(draft.Candidates[0].RequiredCapabilities, "workspace.runner.go") {
		t.Fatalf("Go runner draft = %#v", draft)
	}
}

func TestAnalyzeRejectsBATReferenceCycle(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, filepath.Join(root, "a.bat"), "call b.bat\r\n")
	writeFixture(t, filepath.Join(root, "b.bat"), "call a.bat\r\n")
	analyzer, _ := NewAnalyzer()
	if _, err := analyzer.Analyze(context.Background(), root, "a.bat"); err != ErrReferenceCycle {
		t.Fatalf("Analyze() = %v", err)
	}
}

func TestGNMarketProbeReadOnlyGate(t *testing.T) {
	root := os.Getenv("STACKPILOT_GNMARKET_PATH")
	if root == "" {
		t.Skip("set STACKPILOT_GNMARKET_PATH to run the authorized read-only gate")
	}
	analyzer, err := NewAnalyzer()
	if err != nil {
		t.Fatal(err)
	}
	probe, err := analyzer.Probe(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if probe.State != StateReadyToRegister || len(probe.Candidates) != 0 {
		t.Fatalf("GNMarket probe = %#v, want existing manifest registration", probe)
	}
}

func TestWFGameReadOnlyGate(t *testing.T) {
	root := os.Getenv("STACKPILOT_WFGAME_PATH")
	if root == "" {
		t.Skip("set STACKPILOT_WFGAME_PATH to run the authorized read-only gate")
	}
	analyzer, _ := NewAnalyzer()
	draft, err := analyzer.Analyze(context.Background(), root, "run.bat")
	if err != nil {
		t.Fatal(err)
	}
	if len(draft.Candidates) != 2 {
		t.Fatalf("WFGame candidates = %d, want serve and build-and-serve", len(draft.Candidates))
	}
	for _, candidate := range draft.Candidates {
		if candidate.Applyable || len(candidate.Blockers()) == 0 {
			t.Fatalf("WFGame candidate %q must remain blocked: %#v", candidate.ID, candidate.Findings)
		}
		if hasFinding(candidate.Findings, "WORKSPACE_SCRIPT_SYNTAX_UNSUPPORTED") {
			t.Fatalf("WFGame candidate %q has a false syntax blocker: %#v", candidate.ID, candidate.Findings)
		}
		if !hasFinding(candidate.Findings, "WORKSPACE_IMPORT_EXPOSURE_UNSAFE") {
			t.Fatalf("WFGame candidate %q lost the non-loopback exposure blocker: %#v", candidate.ID, candidate.Findings)
		}
	}
}

func nodeFixture(t *testing.T, listen string) string {
	t.Helper()
	root := t.TempDir()
	writeFixture(t, filepath.Join(root, "package.json"), `{"name":"safe-game","description":"fixture"}`)
	writeFixture(t, filepath.Join(root, "run.bat"), "@echo off\r\nsetlocal\r\ncd /d \"%~dp0\"\r\nset \"BUILD_DIR=build\\web\"\r\nnode \"tools\\serve.js\" \"%BUILD_DIR%\"\r\n")
	writeFixture(t, filepath.Join(root, "tools", "serve.js"), "const PORT = Number('') || 7460;\nconst port = PORT;\n"+listen+"\n")
	if err := os.MkdirAll(filepath.Join(root, "build", "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func writeFixture(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func hasFinding(findings []Finding, code string) bool {
	for _, finding := range findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}

func wfGameControlFlowFixture() string {
	return "@echo off\r\nsetlocal\r\ncd /d \"%~dp0\"\r\nREM\r\n" +
		"set \"CREATOR=tools\\creator.exe\"\r\nset \"BUILD_DIR=build\\web\"\r\nset \"MODE=%~1\"\r\n" +
		"if \"%MODE%\"==\"\" set \"MODE=auto\"\r\nif /i \"%MODE%\"==\"serve\" goto serve\r\n" +
		"if /i \"%MODE%\"==\"build\" goto build\r\nif not exist \"%BUILD_DIR%\\index.html\" (\r\n  echo build missing\r\n  goto build\r\n)\r\n" +
		"node \"tools\\check-build-freshness.js\" \"%BUILD_DIR%\"\r\nif errorlevel 2 goto build\r\nif errorlevel 1 exit /b 1\r\ngoto serve\r\n" +
		":build\r\nif not exist \"%CREATOR%\" (\r\n  echo creator missing\r\n  exit /b 1\r\n)\r\n" +
		"\"%CREATOR%\" --project \"%CD%\" --build \"platform=web;debug=true\"\r\n" +
		"if not exist \"%BUILD_DIR%\\index.html\" (\r\n  echo build failed\r\n  exit /b 1\r\n)\r\n" +
		":serve\r\nif not exist \"%BUILD_DIR%\\index.html\" (\r\n  echo build missing\r\n  exit /b 1\r\n)\r\n" +
		"node \"tools\\serve.js\" \"%BUILD_DIR%\"\r\n"
}

func composeImportFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	bat := "@echo off\r\nwhere docker >nul 2>&1\r\nif errorlevel 1 (\r\n  echo Docker missing\r\n  pause\r\n  exit /b 1\r\n)\r\ndocker --version >nul 2>&1\r\npowershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts\\dev-up.ps1\r\nset \"START_EXIT_CODE=%ERRORLEVEL%\"\r\nif not \"%START_EXIT_CODE%\"==\"0\" (\r\n  echo failed\r\n  pause\r\n  exit /b %START_EXIT_CODE%\r\n)\r\n"
	ps1 := "$ErrorActionPreference = 'Stop'\ndocker compose -f compose.yaml up --build -d\nif ($LASTEXITCODE -ne 0) {\n  throw \"docker compose up failed with exit code $LASTEXITCODE\"\n}\ndocker compose -f compose.yaml ps\nif ($LASTEXITCODE -ne 0) {\n  throw \"docker compose ps failed with exit code $LASTEXITCODE\"\n}\n"
	compose := `services:
  mysql:
    image: mysql:8.4
    healthcheck: {test: [CMD, mysqladmin, ping]}
  web:
    build: ./web
    depends_on: {mysql: {condition: service_healthy}}
    healthcheck: {test: [CMD, /health]}
  job:
    build: ./job
    depends_on: [web]
  frontend:
    build: {context: ./frontend, dockerfile: Dockerfile}
    depends_on: [web]
    healthcheck: {test: [CMD, /health]}
  gateway:
    image: nginx:1.27
    depends_on: [frontend]
    ports: ["${HTTPS_PORT:-8443}:8443"]
`
	writeFixture(t, filepath.Join(root, "start.bat"), bat)
	writeFixture(t, filepath.Join(root, "scripts", "dev-up.ps1"), ps1)
	writeFixture(t, filepath.Join(root, "compose.yaml"), compose)
	for _, directory := range []string{"web", "job", "frontend"} {
		writeFixture(t, filepath.Join(root, directory, "Dockerfile"), "FROM scratch\n")
	}
	return root
}
