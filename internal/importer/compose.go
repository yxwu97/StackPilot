package importer

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
	"stackpilot/internal/security"
)

const maxComposeBytes = 1 << 20

var composeDefaultPattern = regexp.MustCompile(`\$\{[A-Za-z_][A-Za-z0-9_]*:-([0-9]{1,5})\}`)

type composeProject struct {
	File       string
	Services   []composeServiceFact
	Ports      []composePortFact
	BuildFiles []string
	Build      bool
}

type composeServiceFact struct {
	Name      string
	Build     bool
	Health    bool
	DependsOn []string
}

type composePortFact struct {
	Name      string
	Service   string
	Published int
	Target    int
	Exposure  string
	Line      int
}

func parseComposeProject(root, relative, absolute string, contents []byte, build bool) (composeProject, error) {
	if len(contents) > maxComposeBytes {
		return composeProject{}, ErrScriptTooLarge
	}
	document, err := decodeComposeNode(contents)
	if err != nil {
		return composeProject{}, err
	}
	rootMap, err := composeMapping(document, "document")
	if err != nil {
		return composeProject{}, err
	}
	if err := allowComposeKeys(rootMap, "document", "name", "services", "volumes", "networks"); err != nil {
		return composeProject{}, err
	}
	servicesNode, exists := rootMap["services"]
	if !exists {
		return composeProject{}, ErrComposeBuildConfig
	}
	serviceMap, err := composeMapping(servicesNode, "services")
	if err != nil || len(serviceMap) == 0 || len(serviceMap) > 64 {
		return composeProject{}, ErrComposeBuildConfig
	}
	project := composeProject{File: filepath.ToSlash(relative), Build: build}
	for _, name := range sortedNodeKeys(serviceMap) {
		service, ports, buildFiles, err := parseComposeService(root, filepath.Dir(absolute), name, serviceMap[name])
		if err != nil {
			return composeProject{}, err
		}
		project.Services = append(project.Services, service)
		project.Ports = append(project.Ports, ports...)
		project.BuildFiles = append(project.BuildFiles, buildFiles...)
	}
	managed := make(map[string]bool, len(project.Services))
	for _, service := range project.Services {
		managed[service.Name] = true
	}
	for _, service := range project.Services {
		for _, dependency := range service.DependsOn {
			if !managed[dependency] {
				return composeProject{}, ErrComposeBuildConfig
			}
		}
	}
	return project, nil
}

func decodeComposeNode(contents []byte) (*yaml.Node, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(contents))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil || len(document.Content) != 1 {
		return nil, ErrComposeBuildConfig
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, ErrComposeBuildConfig
	}
	if err := validateComposeNode(document.Content[0]); err != nil {
		return nil, err
	}
	return document.Content[0], nil
}

func validateComposeNode(node *yaml.Node) error {
	if node == nil || node.Kind == yaml.AliasNode || node.Anchor != "" || node.Tag == "!!merge" {
		return ErrComposeBuildConfig
	}
	if node.Kind == yaml.MappingNode {
		seen := make(map[string]bool, len(node.Content)/2)
		for index := 0; index < len(node.Content); index += 2 {
			key := node.Content[index]
			if key.Kind != yaml.ScalarNode || seen[key.Value] {
				return ErrComposeBuildConfig
			}
			seen[key.Value] = true
		}
	}
	for _, child := range node.Content {
		if err := validateComposeNode(child); err != nil {
			return err
		}
	}
	return nil
}

func parseComposeService(root, directory, name string, node *yaml.Node) (composeServiceFact, []composePortFact, []string, error) {
	values, err := composeMapping(node, "services."+name)
	if err != nil || !validComposeServiceName(name) {
		return composeServiceFact{}, nil, nil, ErrComposeBuildConfig
	}
	allowed := []string{"image", "build", "command", "container_name", "depends_on", "environment", "env_file", "expose", "healthcheck", "networks", "ports", "restart", "user", "volumes", "working_dir"}
	if err := allowComposeKeys(values, "services."+name, allowed...); err != nil {
		return composeServiceFact{}, nil, nil, err
	}
	service := composeServiceFact{Name: name}
	if dependency, exists := values["depends_on"]; exists {
		service.DependsOn, err = parseComposeDependencies(dependency)
		if err != nil {
			return composeServiceFact{}, nil, nil, err
		}
	}
	if health, exists := values["healthcheck"]; exists {
		service.Health, err = composeHealthEnabled(health)
		if err != nil {
			return composeServiceFact{}, nil, nil, err
		}
	}
	ports, err := parseComposePorts(name, values["ports"])
	if err != nil {
		return composeServiceFact{}, nil, nil, err
	}
	buildFiles := []string{}
	if buildNode, exists := values["build"]; exists {
		service.Build = true
		buildFiles, err = parseComposeBuild(root, directory, buildNode)
		if err != nil {
			return composeServiceFact{}, nil, nil, err
		}
	}
	if !service.Build {
		if _, exists := values["image"]; !exists {
			return composeServiceFact{}, nil, nil, ErrComposeBuildConfig
		}
	}
	return service, ports, buildFiles, nil
}

func parseComposeBuild(root, directory string, node *yaml.Node) ([]string, error) {
	contextValue, dockerfile := "", "Dockerfile"
	switch node.Kind {
	case yaml.ScalarNode:
		contextValue = node.Value
	case yaml.MappingNode:
		values, err := composeMapping(node, "build")
		if err != nil || allowComposeKeys(values, "build", "context", "dockerfile") != nil {
			return nil, ErrComposeBuildConfig
		}
		contextValue = scalarValue(values["context"])
		if value, exists := values["dockerfile"]; exists {
			dockerfile = scalarValue(value)
		}
	default:
		return nil, ErrComposeBuildConfig
	}
	if contextValue == "" || filepath.IsAbs(contextValue) || remoteBuildContext(contextValue) || filepath.IsAbs(dockerfile) {
		return nil, ErrComposeBuildConfig
	}
	contextPath, err := canonicalComposePath(root, directory, contextValue, true)
	if err != nil {
		return nil, err
	}
	dockerfilePath, err := canonicalComposePath(root, contextPath, dockerfile, false)
	if err != nil {
		return nil, err
	}
	return []string{dockerfilePath}, nil
}

func canonicalComposePath(root, base, value string, directory bool) (string, error) {
	path, err := security.CanonicalExistingPath(filepath.Join(base, filepath.FromSlash(value)))
	if err != nil {
		return "", ErrComposeBuildConfig
	}
	inside, pathErr := security.PathWithinRoot(root, path)
	info, statErr := os.Stat(path)
	if pathErr != nil || !inside || statErr != nil || (directory && !info.IsDir()) || (!directory && !info.Mode().IsRegular()) {
		return "", ErrComposeBuildConfig
	}
	return path, nil
}

func remoteBuildContext(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(value))
	return strings.Contains(lower, "://") || strings.HasPrefix(lower, "git@") || strings.HasPrefix(lower, "github.com/")
}

func parseComposeDependencies(node *yaml.Node) ([]string, error) {
	result := make([]string, 0)
	switch node.Kind {
	case yaml.SequenceNode:
		for _, child := range node.Content {
			if child.Kind != yaml.ScalarNode || !validComposeServiceName(child.Value) {
				return nil, ErrComposeBuildConfig
			}
			result = append(result, child.Value)
		}
	case yaml.MappingNode:
		values, err := composeMapping(node, "depends_on")
		if err != nil {
			return nil, err
		}
		result = sortedNodeKeys(values)
	default:
		return nil, ErrComposeBuildConfig
	}
	sort.Strings(result)
	return result, nil
}

func composeHealthEnabled(node *yaml.Node) (bool, error) {
	if node == nil || node.Tag == "!!null" {
		return false, nil
	}
	values, err := composeMapping(node, "healthcheck")
	if err != nil {
		return false, err
	}
	if allowComposeKeys(values, "healthcheck", "test", "interval", "timeout", "retries", "start_period", "start_interval", "disable") != nil {
		return false, ErrComposeBuildConfig
	}
	if disabled, exists := values["disable"]; exists && strings.EqualFold(scalarValue(disabled), "true") {
		return false, nil
	}
	_, exists := values["test"]
	return exists, nil
}

func parseComposePorts(service string, node *yaml.Node) ([]composePortFact, error) {
	if node == nil {
		return nil, nil
	}
	if node.Kind != yaml.SequenceNode || len(node.Content) > 64 {
		return nil, ErrComposeBuildConfig
	}
	result := make([]composePortFact, 0, len(node.Content))
	for _, child := range node.Content {
		port, mapped, err := parseComposePort(service, child)
		if err != nil {
			return nil, err
		}
		if mapped {
			port.Name = composePortName(service, port.Target, len(result))
			result = append(result, port)
		}
	}
	return result, nil
}

func parseComposePort(service string, node *yaml.Node) (composePortFact, bool, error) {
	if node.Kind == yaml.ScalarNode {
		return parseShortComposePort(service, node)
	}
	values, err := composeMapping(node, "ports")
	if err != nil || allowComposeKeys(values, "ports", "target", "published", "host_ip", "protocol", "mode", "app_protocol", "name") != nil {
		return composePortFact{}, false, ErrComposeBuildConfig
	}
	if protocol := scalarValue(values["protocol"]); protocol != "" && !strings.EqualFold(protocol, "tcp") {
		return composePortFact{}, false, ErrComposeBuildConfig
	}
	published, ok := composePortNumber(scalarValue(values["published"]))
	if !ok {
		return composePortFact{}, false, nil
	}
	target, ok := composePortNumber(scalarValue(values["target"]))
	if !ok {
		return composePortFact{}, false, ErrComposeBuildConfig
	}
	exposure := composeExposure(scalarValue(values["host_ip"]))
	return composePortFact{Service: service, Published: published, Target: target, Exposure: exposure, Line: node.Line}, true, nil
}

func parseShortComposePort(service string, node *yaml.Node) (composePortFact, bool, error) {
	value := composeDefaultPattern.ReplaceAllString(node.Value, "$1")
	value, protocol, _ := strings.Cut(value, "/")
	if protocol != "" && !strings.EqualFold(protocol, "tcp") {
		return composePortFact{}, false, ErrComposeBuildConfig
	}
	parts := strings.Split(value, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return composePortFact{}, false, nil
	}
	published, ok := composePortNumber(parts[len(parts)-2])
	if !ok {
		return composePortFact{}, false, ErrComposeBuildConfig
	}
	target, ok := composePortNumber(parts[len(parts)-1])
	if !ok {
		return composePortFact{}, false, ErrComposeBuildConfig
	}
	host := ""
	if len(parts) == 3 {
		host = parts[0]
	}
	return composePortFact{Service: service, Published: published, Target: target, Exposure: composeExposure(host), Line: node.Line}, true, nil
}

func composePortNumber(value string) (int, bool) {
	value = composeDefaultPattern.ReplaceAllString(strings.TrimSpace(value), "$1")
	parsed, err := strconv.Atoi(value)
	return parsed, err == nil && parsed >= 1 && parsed <= 65535
}

func composeExposure(host string) string {
	if host == "127.0.0.1" || strings.EqualFold(host, "localhost") || host == "::1" {
		return "loopback"
	}
	return "all_interfaces"
}

func composePortName(service string, target, index int) string {
	base := sanitizeID(service)
	if index == 0 {
		return base
	}
	return fmt.Sprintf("%s-%d", base, target)
}

func composeMapping(node *yaml.Node, path string) (map[string]*yaml.Node, error) {
	if node == nil || node.Kind != yaml.MappingNode || len(node.Content)%2 != 0 {
		return nil, fmt.Errorf("%w: %s", ErrComposeBuildConfig, path)
	}
	result := make(map[string]*yaml.Node, len(node.Content)/2)
	for index := 0; index < len(node.Content); index += 2 {
		key := node.Content[index]
		if key.Kind != yaml.ScalarNode || key.Value == "" {
			return nil, ErrComposeBuildConfig
		}
		result[key.Value] = node.Content[index+1]
	}
	return result, nil
}

func allowComposeKeys(values map[string]*yaml.Node, path string, allowed ...string) error {
	set := make(map[string]bool, len(allowed))
	for _, key := range allowed {
		set[key] = true
	}
	for key := range values {
		if !set[key] {
			return fmt.Errorf("%w: %s.%s", ErrComposeBuildConfig, path, key)
		}
	}
	return nil
}

func sortedNodeKeys(values map[string]*yaml.Node) []string {
	result := make([]string, 0, len(values))
	for key := range values {
		result = append(result, key)
	}
	sort.Strings(result)
	return result
}

func scalarValue(node *yaml.Node) string {
	if node == nil || node.Kind != yaml.ScalarNode {
		return ""
	}
	return node.Value
}

func validComposeServiceName(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for index, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || (index > 0 && strings.ContainsRune("._-", char)) {
			continue
		}
		return false
	}
	return true
}
