package importer

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
	"stackpilot/internal/manifest"
	"stackpilot/internal/security"
)

var (
	packageIDPattern      = regexp.MustCompile(`[^a-z0-9]+`)
	portDefaultPattern    = regexp.MustCompile(`(?m)\b(?:BASE_)?PORT\s*=.*?\|\|\s*([0-9]{4,5})\s*;`)
	loopbackListenPattern = regexp.MustCompile(`(?m)\.listen\s*\([^,]+,\s*['"](?:127\.0\.0\.1|localhost|::1)['"]`)
)

type projectIdentity struct{ id, name, description string }

func discoverIdentity(root string) projectIdentity {
	result := projectIdentity{id: sanitizeID(filepath.Base(root)), name: filepath.Base(root)}
	contents, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		return result
	}
	var value struct{ Name, Description string }
	if json.Unmarshal(contents, &value) == nil {
		if value.Name != "" {
			result.id, result.name = sanitizeID(value.Name), value.Name
		}
		result.description = value.Description
	}
	return result
}

func sanitizeID(value string) string {
	value = strings.Trim(packageIDPattern.ReplaceAllString(strings.ToLower(value), "-"), "-")
	if value == "" || value[0] < 'a' || value[0] > 'z' {
		value = "workspace-" + value
	}
	if len(value) > 63 {
		value = strings.TrimRight(value[:63], "-")
	}
	return value
}

func (analyzer *Analyzer) buildCandidates(ctx context.Context, root string, identity projectIdentity, analysis batAnalysis) ([]CandidateDraft, error) {
	result := make([]CandidateDraft, 0, len(analysis.composeProjects)+2)
	for _, project := range analysis.composeProjects {
		candidate, err := analyzer.composeCandidate(ctx, root, identity, project, analysis.findings)
		if err != nil {
			return nil, err
		}
		result = append(result, candidate)
	}
	serve, found, err := analyzer.serveCandidate(ctx, root, identity, analysis)
	if err != nil {
		return nil, err
	}
	if found {
		result = append(result, serve)
	}
	processes, hasProcesses, err := analyzer.processCandidate(ctx, root, identity, analysis, serve, found)
	if err != nil {
		return nil, err
	}
	if hasProcesses {
		result = append(result, processes)
	}
	if analysis.hasCocos && found {
		build := serve
		build.ID, build.Name = "build-and-serve", "Build with Cocos Creator, then serve"
		build.RequiredCapabilities = append(build.RequiredCapabilities, "workspace.runner.cocos")
		build.Findings = append(build.Findings, Finding{Code: "FEATURE_NOT_ENABLED", Severity: "blocking", Message: "The controlled Cocos Creator runner is not enabled.", Confidence: Confirmed})
		build.Applyable = false
		result = append(result, build)
	}
	if len(result) == 0 {
		return nil, ErrImportIncomplete
	}
	return result, nil
}

func (analyzer *Analyzer) composeCandidate(ctx context.Context, root string, identity projectIdentity, project composeProject, inherited []Finding) (CandidateDraft, error) {
	serviceNames := make([]string, 0, len(project.Services))
	buildServices := make([]string, 0)
	readiness := make(map[string]string, len(project.Services))
	findings := append([]Finding(nil), inherited...)
	for _, service := range project.Services {
		serviceNames = append(serviceNames, service.Name)
		if service.Build {
			buildServices = append(buildServices, service.Name)
		}
		if service.Health {
			readiness[service.Name] = "healthy"
		} else {
			readiness[service.Name] = "running"
			finding := blocking("WORKSPACE_IMPORT_READINESS_UNCONFIRMED", "Running readiness requires explicit confirmation because this service has no healthcheck.", project.File, 0)
			finding.Field = service.Name
			findings = append(findings, finding)
		}
	}
	ports, mappings := composePortDrafts(project, &findings)
	sort.Strings(serviceNames)
	sort.Strings(buildServices)
	policy := "never"
	required := []string{"phase2.compose"}
	if project.Build && len(buildServices) > 0 {
		policy = "always"
		required = append(required, "phase2.compose-build")
		findings = append(findings, Finding{Code: "COMPOSE_BUILD_CONFIRMED", Severity: "info", Message: "The source script explicitly requests a local Compose build.", Confidence: Confirmed, Evidence: []Evidence{{Path: project.File}}})
		findings = append(findings, blocking("WORKSPACE_IMPORT_BUILD_UNCONFIRMED", "Executing the discovered local Dockerfiles requires explicit confirmation.", project.File, 0))
	}
	compose := &ComposeDraft{File: project.File, Services: serviceNames, BuildPolicy: policy, BuildServices: buildServices, Readiness: readiness, Ports: mappings}
	service := ServiceDraft{ID: "compose", DisplayName: "Compose services", Driver: "compose", Mode: "daemon", ReadinessType: "compose", Confidence: Confirmed, Compose: compose}
	candidate := CandidateDraft{ID: "run-compose-project", Name: "Run Compose project", Description: "Build and run the discovered Compose services through the controlled Compose driver.", Applyable: noBlockers(findings), RequiredCapabilities: required, Services: []ServiceDraft{service}, Ports: ports, Findings: findings}
	if err := analyzer.renderCandidate(ctx, root, identity, &candidate); err != nil {
		return CandidateDraft{}, err
	}
	return candidate, nil
}

func composePortDrafts(project composeProject, findings *[]Finding) ([]PortDraft, map[string]ComposePortDraft) {
	ports := make([]PortDraft, 0, len(project.Ports))
	mappings := make(map[string]ComposePortDraft, len(project.Ports))
	for _, port := range project.Ports {
		ports = append(ports, PortDraft{Name: port.Name, Preferred: port.Published, Exposure: port.Exposure, Confidence: Confirmed})
		mappings[port.Name] = ComposePortDraft{Service: port.Service, Target: port.Target}
		if port.Exposure != "loopback" {
			*findings = append(*findings, Finding{Code: "COMPOSE_PORT_WILL_BE_LOOPBACK", Severity: "warning", Message: "The base Compose port is not loopback-bound; the runtime override will replace it with loopback exposure.", Confidence: Confirmed, Evidence: []Evidence{{Path: project.File, Line: port.Line, Field: port.Name}}})
		}
	}
	return ports, mappings
}

func (analyzer *Analyzer) processCandidate(ctx context.Context, root string, identity projectIdentity, analysis batAnalysis, node CandidateDraft, hasNode bool) (CandidateDraft, bool, error) {
	services := make([]ServiceDraft, 0, len(analysis.commands))
	counts := map[string]int{}
	for _, fact := range analysis.commands {
		if fact.runner == "node" {
			continue
		}
		working, err := resolveWorkingDirectory(root, fact.workingDir)
		if err != nil {
			return CandidateDraft{}, false, err
		}
		if info, err := os.Stat(working); err != nil || !info.IsDir() {
			return CandidateDraft{}, false, ErrScriptNotFound
		}
		counts[fact.runner]++
		id := fact.runner
		if counts[fact.runner] > 1 {
			id += "-" + strconv.Itoa(counts[fact.runner])
		}
		services = append(services, ServiceDraft{ID: id, DisplayName: runnerDisplayName(fact.runner), Driver: "process", Runner: fact.runner,
			Mode: "daemon", WorkingDirectory: fact.workingDir, Arguments: append([]string(nil), fact.arguments...),
			Environment: map[string]string{}, ReadinessType: "process", Confidence: Confirmed})
	}
	if len(services) == 0 {
		return CandidateDraft{}, false, nil
	}
	candidate := CandidateDraft{ID: "run-discovered-services", Name: "Run discovered services",
		Description: "Run the discovered services directly through trusted StackPilot runners.", Applyable: noBlockers(analysis.findings),
		Services: services, Findings: append([]Finding(nil), analysis.findings...)}
	if counts["go"] > 0 {
		candidate.RequiredCapabilities = append(candidate.RequiredCapabilities, "workspace.runner.go")
	}
	if hasNode {
		candidate.Services = append(candidate.Services, node.Services...)
		candidate.Ports = append(candidate.Ports, node.Ports...)
		candidate.RequiredCapabilities = append(candidate.RequiredCapabilities, node.RequiredCapabilities...)
		candidate.Findings = append(candidate.Findings, node.Findings...)
		candidate.Applyable = noBlockers(candidate.Findings)
	}
	if err := analyzer.renderCandidate(ctx, root, identity, &candidate); err != nil {
		return CandidateDraft{}, false, err
	}
	return candidate, true, nil
}

func runnerDisplayName(runner string) string {
	return map[string]string{"maven": "Maven service", "npm": "npm service", "java": "Java service", "go": "Go service"}[runner]
}

func (analyzer *Analyzer) serveCandidate(ctx context.Context, root string, identity projectIdentity, analysis batAnalysis) (CandidateDraft, bool, error) {
	for index := len(analysis.commands) - 1; index >= 0; index-- {
		fact := analysis.commands[index]
		if fact.runner != "node" || len(fact.arguments) == 0 {
			continue
		}
		script := fact.arguments[0]
		if strings.Contains(strings.ToLower(script), "check-build-freshness") {
			continue
		}
		return analyzer.nodeCandidate(ctx, root, identity, fact, analysis.findings)
	}
	return CandidateDraft{}, false, nil
}

func (analyzer *Analyzer) nodeCandidate(ctx context.Context, root string, identity projectIdentity, fact commandFact, inherited []Finding) (CandidateDraft, bool, error) {
	working, err := resolveWorkingDirectory(root, fact.workingDir)
	if err != nil {
		return CandidateDraft{}, false, err
	}
	scriptPath, err := resolveReference(root, working, fact.arguments[0])
	if err != nil {
		return CandidateDraft{}, false, err
	}
	contents, err := os.ReadFile(scriptPath)
	if err != nil || len(contents) > maxScriptBytes {
		return CandidateDraft{}, false, ErrScriptTooLarge
	}
	port, confirmed := discoverPort(contents)
	exposure := "all_interfaces"
	if loopbackListenPattern.Match(contents) {
		exposure = "loopback"
	}
	findings := append([]Finding(nil), inherited...)
	relScript, _ := filepath.Rel(root, scriptPath)
	if !confirmed {
		findings = append(findings, blocking("WORKSPACE_IMPORT_PORT_UNCONFIRMED", "The service port could not be confirmed.", filepath.ToSlash(relScript), 0))
	}
	if exposure != "loopback" {
		findings = append(findings, blocking("WORKSPACE_IMPORT_EXPOSURE_UNSAFE", "The Node service listens beyond the loopback interface.", filepath.ToSlash(relScript), 0))
	}
	arguments := append([]string(nil), fact.arguments...)
	if confirmed {
		arguments = append(arguments, "--port=${ports.web}")
	}
	arguments = append(arguments, "--no-open")
	candidate := makeNodeCandidate(identity, fact, arguments, port, exposure, findings)
	if err := analyzer.renderCandidate(ctx, root, identity, &candidate); err != nil {
		return CandidateDraft{}, false, err
	}
	return candidate, true, nil
}

func resolveWorkingDirectory(root, value string) (string, error) {
	if value == "" || value == "." {
		return root, nil
	}
	if filepath.IsAbs(value) {
		return "", ErrScriptOutside
	}
	return resolveReference(root, root, value)
}

func resolveReference(root, working, value string) (string, error) {
	if filepath.IsAbs(value) {
		return "", ErrScriptOutside
	}
	candidate, err := security.CanonicalExistingPath(filepath.Join(working, filepath.FromSlash(value)))
	if err != nil {
		return "", ErrScriptNotFound
	}
	inside, err := security.PathWithinRoot(root, candidate)
	if err != nil || !inside {
		return "", ErrScriptOutside
	}
	info, err := os.Stat(candidate)
	if err != nil || (!info.Mode().IsRegular() && !info.IsDir()) {
		return "", ErrScriptNotFound
	}
	return candidate, nil
}

func discoverPort(contents []byte) (int, bool) {
	match := portDefaultPattern.FindSubmatch(contents)
	if len(match) != 2 {
		return 0, false
	}
	value, err := strconv.Atoi(string(match[1]))
	return value, err == nil && value >= 1024 && value <= 65535
}

func makeNodeCandidate(identity projectIdentity, fact commandFact, arguments []string, port int, exposure string, findings []Finding) CandidateDraft {
	service := ServiceDraft{ID: "web", DisplayName: "Web", Driver: "process", Runner: "node", Mode: "daemon", WorkingDirectory: fact.workingDir,
		Arguments: arguments, Environment: map[string]string{}, ReadinessType: "http", Confidence: Confirmed}
	ports := []PortDraft{}
	if port > 0 {
		service.ReadinessTarget = "http://127.0.0.1:${ports.web}/"
		ports = append(ports, PortDraft{Name: "web", Preferred: port, Exposure: exposure, Confidence: Confirmed})
	}
	return CandidateDraft{ID: "serve-existing", Name: "Serve existing build", Description: "Run the discovered Node static service without executing the BAT file.",
		Applyable: noBlockers(findings), RequiredCapabilities: []string{"workspace.runner.node"}, Services: []ServiceDraft{service}, Ports: ports, Findings: findings}
}

func noBlockers(findings []Finding) bool {
	for _, finding := range findings {
		if finding.Severity == "blocking" {
			return false
		}
	}
	return true
}

func (analyzer *Analyzer) renderCandidate(ctx context.Context, root string, identity projectIdentity, candidate *CandidateDraft) error {
	candidate.Manifest = candidateManifest(identity, *candidate)
	encoded, err := yaml.Marshal(candidate.Manifest)
	if err != nil {
		return err
	}
	document, err := analyzer.loader.Parse(encoded)
	if err != nil {
		return err
	}
	validated, err := analyzer.validator.Validate(ctx, document, root)
	if err != nil {
		return err
	}
	normalized, err := yaml.Marshal(validated.Manifest)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(validated.JSON)
	candidate.Manifest, candidate.ManifestYAML = validated.Manifest, string(normalized)
	candidate.ManifestDigest = fmt.Sprintf("%x", digest[:])
	return nil
}

func (analyzer *Analyzer) RenderManifest(ctx context.Context, root string, value manifest.Manifest) (manifest.Manifest, string, string, error) {
	encoded, err := yaml.Marshal(value)
	if err != nil {
		return manifest.Manifest{}, "", "", err
	}
	document, err := analyzer.loader.Parse(encoded)
	if err != nil {
		return manifest.Manifest{}, "", "", err
	}
	validated, err := analyzer.validator.Validate(ctx, document, root)
	if err != nil {
		return manifest.Manifest{}, "", "", err
	}
	normalized, err := yaml.Marshal(validated.Manifest)
	if err != nil {
		return manifest.Manifest{}, "", "", err
	}
	digest := sha256.Sum256(validated.JSON)
	return validated.Manifest, string(normalized), fmt.Sprintf("%x", digest[:]), nil
}

func candidateManifest(identity projectIdentity, candidate CandidateDraft) manifest.Manifest {
	ports := make(map[string]manifest.Port, len(candidate.Ports))
	for _, port := range candidate.Ports {
		preferred := port.Preferred
		ports[port.Name] = manifest.Port{Protocol: "tcp", Preferred: &preferred, ConflictPolicy: "auto", Exposure: "loopback"}
	}
	services := make(map[string]manifest.Service, len(candidate.Services))
	for _, service := range candidate.Services {
		required := true
		driver := service.Driver
		if driver == "" {
			driver = "process"
		}
		definition := manifest.Service{DisplayName: service.DisplayName, Required: &required, Driver: driver, Mode: service.Mode,
			Runner: service.Runner, WorkingDirectory: service.WorkingDirectory, Arguments: service.Arguments, Environment: service.Environment}
		if driver == "compose" && service.Compose != nil {
			mappings := make(map[string]manifest.ComposePort, len(service.Compose.Ports))
			for name, port := range service.Compose.Ports {
				mappings[name] = manifest.ComposePort{Service: port.Service, Target: port.Target}
			}
			definition.Compose = &manifest.ComposeService{File: service.Compose.File, Services: append([]string(nil), service.Compose.Services...), BuildPolicy: service.Compose.BuildPolicy, Readiness: cloneStringMap(service.Compose.Readiness), Ports: mappings}
			definition.Runner, definition.WorkingDirectory, definition.Arguments = "", "", nil
		}
		if service.ReadinessType != "" {
			definition.Readiness = &manifest.HealthCheck{Type: service.ReadinessType, URL: service.ReadinessTarget, Timeout: "30s", Interval: "1s"}
		}
		services[service.ID] = definition
	}
	return manifest.Manifest{APIVersion: "stackpilot.io/v1alpha1", Kind: "System", Metadata: manifest.Metadata{ID: identity.id, Name: identity.name, Description: identity.description}, Spec: manifest.Spec{Ports: ports, Services: services}}
}

func cloneStringMap(source map[string]string) map[string]string {
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
