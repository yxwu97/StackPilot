package compose

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"stackpilot/internal/domain"
	"stackpilot/internal/security"
)

const overrideFileName = "compose.override.yml"

// PortOverride is one planned loopback host-to-container TCP mapping.
type PortOverride struct {
	Service   string
	Target    int
	Published int
}

// OverrideRequest contains only resolved non-sensitive runtime values.
type OverrideRequest struct {
	OperationID domain.OperationID
	SystemID    domain.SystemID
	WorkspaceID domain.WorkspaceID
	InstanceID  domain.SystemInstanceID
	Services    []string
	Ports       map[string]PortOverride
	Environment map[string]map[string]string
}

// OverrideResult identifies the immutable generated override.
type OverrideResult struct {
	Path   string
	Digest string
}

// OverrideGenerator writes validated runtime overrides under the control data root.
type OverrideGenerator struct{ dataDir string }

// NewOverrideGenerator constructs a generator rooted in an existing control data directory.
func NewOverrideGenerator(dataDir string) (*OverrideGenerator, error) {
	if !filepath.IsAbs(dataDir) {
		return nil, ErrOverrideInvalid
	}
	canonical, err := security.CanonicalExistingPath(dataDir)
	if err != nil {
		return nil, ErrOverrideInvalid
	}
	return &OverrideGenerator{dataDir: canonical}, nil
}

// Generate validates, strictly reparses, and atomically publishes one override.
func (generator *OverrideGenerator) Generate(request OverrideRequest) (*OverrideResult, error) {
	if err := validateOverrideRequest(request); err != nil {
		return nil, err
	}
	document := buildOverride(request)
	contents, err := yaml.Marshal(document)
	if err != nil {
		return nil, ErrOverrideInvalid
	}
	if err := validateOverrideBytes(contents, document); err != nil {
		return nil, err
	}
	directory, err := generator.operationDirectory(request.OperationID)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(directory, overrideFileName)
	if err := publishOverride(path, contents); err != nil {
		return nil, err
	}
	if err := validatePublishedOverride(generator.dataDir, path, contents, document); err != nil {
		return nil, err
	}
	digest := sha256.Sum256(contents)
	return &OverrideResult{Path: path, Digest: hex.EncodeToString(digest[:])}, nil
}

func validateOverrideRequest(request OverrideRequest) error {
	if _, err := domain.ParseOperationID(request.OperationID.String()); err != nil {
		return ErrOverrideInvalid
	}
	if _, err := domain.ParseSystemID(request.SystemID.String()); err != nil {
		return ErrOverrideInvalid
	}
	if _, err := domain.ParseWorkspaceID(request.WorkspaceID.String()); err != nil {
		return ErrOverrideInvalid
	}
	if _, err := domain.ParseSystemInstanceID(request.InstanceID.String()); err != nil {
		return ErrOverrideInvalid
	}
	services := serviceSet(request.Services)
	if len(services) != len(request.Services) || len(services) == 0 {
		return ErrOverrideInvalid
	}
	return validateOverrideValues(request, services)
}

func validateOverrideValues(request OverrideRequest, services map[string]struct{}) error {
	for logicalName, mapping := range request.Ports {
		if _, err := domain.ParseServiceID(logicalName); err != nil || !validComposeService(mapping.Service) {
			return ErrOverrideInvalid
		}
		if _, exists := services[mapping.Service]; !exists || mapping.Target < 1 || mapping.Target > 65535 || mapping.Published < 1024 || mapping.Published > 65535 {
			return ErrOverrideInvalid
		}
	}
	for service, environment := range request.Environment {
		if _, exists := services[service]; !exists {
			return ErrOverrideInvalid
		}
		for name, value := range environment {
			if !validEnvironmentName(name) || !validOverrideValue(value) {
				return ErrOverrideInvalid
			}
		}
	}
	return nil
}

func buildOverride(request OverrideRequest) overrideDocument {
	services := make(map[string]overrideService, len(request.Services))
	for _, name := range request.Services {
		services[name] = overrideService{Labels: runtimeLabels(request, name)}
	}
	for _, logicalName := range sortedPortNames(request.Ports) {
		mapping := request.Ports[logicalName]
		service := services[mapping.Service]
		service.Ports = append(service.Ports, overridePort{
			Target: mapping.Target, Published: strconv.Itoa(mapping.Published), HostIP: "127.0.0.1", Protocol: "tcp", Mode: "host",
		})
		services[mapping.Service] = service
	}
	for serviceName, environment := range request.Environment {
		service := services[serviceName]
		service.Environment = cloneStringMap(environment)
		services[serviceName] = service
	}
	return overrideDocument{Services: services}
}

func runtimeLabels(request OverrideRequest, service string) map[string]string {
	return map[string]string{
		"stackpilot.system": request.SystemID.String(), "stackpilot.workspace": request.WorkspaceID.String(),
		"stackpilot.instance": request.InstanceID.String(), "stackpilot.service": service,
	}
}

func validateOverrideBytes(contents []byte, expected overrideDocument) error {
	var actual overrideDocument
	if err := decodeStrictOverride(contents, &actual); err != nil {
		return err
	}
	encodedActual, _ := yaml.Marshal(actual)
	encodedExpected, _ := yaml.Marshal(expected)
	if !bytes.Equal(encodedActual, encodedExpected) {
		return ErrOverrideInvalid
	}
	return nil
}

func decodeStrictOverride(contents []byte, actual *overrideDocument) error {
	decoder := yaml.NewDecoder(bytes.NewReader(contents))
	decoder.KnownFields(true)
	if err := decoder.Decode(actual); err != nil {
		return ErrOverrideInvalid
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return ErrOverrideInvalid
	}
	return validateOverrideShape(*actual)
}

func validateOverrideShape(document overrideDocument) error {
	if len(document.Services) == 0 {
		return ErrOverrideInvalid
	}
	for name, service := range document.Services {
		if !validComposeService(name) || len(service.Labels) != 4 {
			return ErrOverrideInvalid
		}
		for _, port := range service.Ports {
			published, err := strconv.Atoi(port.Published)
			if err != nil || published < 1024 || port.Target < 1 || port.HostIP != "127.0.0.1" || port.Protocol != "tcp" || port.Mode != "host" {
				return ErrOverrideInvalid
			}
		}
	}
	return nil
}

func (generator *OverrideGenerator) operationDirectory(operationID domain.OperationID) (string, error) {
	runtimeDirectory, err := ensureContainedDirectory(generator.dataDir, "runtime")
	if err != nil {
		return "", err
	}
	operationsDirectory, err := ensureContainedDirectory(runtimeDirectory, "operations")
	if err != nil {
		return "", err
	}
	return ensureContainedDirectory(operationsDirectory, operationID.String())
}

func ensureContainedDirectory(parent, name string) (string, error) {
	directory := filepath.Join(parent, name)
	if err := os.Mkdir(directory, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", fmt.Errorf("create Compose runtime directory: %w", err)
	}
	canonical, err := security.CanonicalExistingPath(directory)
	if err != nil {
		return "", ErrOverrideInvalid
	}
	inside, err := security.PathWithinRoot(parent, canonical)
	if err != nil || !inside || canonical == parent {
		return "", ErrOverrideInvalid
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.IsDir() {
		return "", ErrOverrideInvalid
	}
	return canonical, nil
}

func publishOverride(path string, contents []byte) error {
	if existing, err := os.ReadFile(path); err == nil {
		if bytes.Equal(existing, contents) {
			return nil
		}
		return ErrOverrideConflict
	} else if !errors.Is(err, os.ErrNotExist) {
		return ErrOverrideInvalid
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".compose-override-*.tmp")
	if err != nil {
		return ErrOverrideInvalid
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := writeTemporaryOverride(temporary, contents); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return resolvePublishRace(path, contents)
	}
	return nil
}

func resolvePublishRace(path string, contents []byte) error {
	existing, err := os.ReadFile(path)
	if err != nil {
		return ErrOverrideInvalid
	}
	if !bytes.Equal(existing, contents) {
		return ErrOverrideConflict
	}
	return nil
}

func writeTemporaryOverride(file *os.File, contents []byte) error {
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return ErrOverrideInvalid
	}
	if _, err := file.Write(contents); err != nil {
		_ = file.Close()
		return ErrOverrideInvalid
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return ErrOverrideInvalid
	}
	if err := file.Close(); err != nil {
		return ErrOverrideInvalid
	}
	return nil
}

func validatePublishedOverride(dataDir, path string, expected []byte, document overrideDocument) error {
	canonical, err := security.CanonicalExistingPath(path)
	if err != nil {
		return ErrOverrideInvalid
	}
	inside, err := security.PathWithinRoot(dataDir, canonical)
	if err != nil || !inside {
		return ErrOverrideInvalid
	}
	contents, err := os.ReadFile(canonical)
	if err != nil || !bytes.Equal(contents, expected) {
		return ErrOverrideInvalid
	}
	return validateOverrideBytes(contents, document)
}

func serviceSet(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		if validComposeService(value) {
			result[value] = struct{}{}
		}
	}
	return result
}

func validComposeService(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for index, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || (index > 0 && (r == '.' || r == '_' || r == '-')) {
			continue
		}
		return false
	}
	return true
}

func validEnvironmentName(value string) bool {
	if value == "" || !((value[0] >= 'A' && value[0] <= 'Z') || (value[0] >= 'a' && value[0] <= 'z') || value[0] == '_') {
		return false
	}
	for _, char := range value[1:] {
		if !((char >= 'A' && char <= 'Z') || (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '_') {
			return false
		}
	}
	return true
}

func validOverrideValue(value string) bool {
	return len(value) <= 16384 && !strings.ContainsAny(value, "\x00\r\n") && !strings.Contains(value, "${")
}

func sortedPortNames(values map[string]PortOverride) []string {
	result := make([]string, 0, len(values))
	for name := range values {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}

func cloneStringMap(source map[string]string) map[string]string {
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

type overrideDocument struct {
	Services map[string]overrideService `yaml:"services"`
}

type overrideService struct {
	Ports       []overridePort    `yaml:"ports,omitempty"`
	Environment map[string]string `yaml:"environment,omitempty"`
	Labels      map[string]string `yaml:"labels"`
}

type overridePort struct {
	Target    int    `yaml:"target"`
	Published string `yaml:"published"`
	HostIP    string `yaml:"host_ip"`
	Protocol  string `yaml:"protocol"`
	Mode      string `yaml:"mode"`
}
