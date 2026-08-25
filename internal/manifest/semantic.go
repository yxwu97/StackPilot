package manifest

import (
	"context"
	"encoding/json"
	"fmt"

	"stackpilot/internal/domain"
)

// PortRange is a validated inclusive fallback range.
type PortRange struct {
	Start int
	End   int
}

// ValidatedDocument contains defaulted manifest data and trusted path resolutions.
type ValidatedDocument struct {
	Manifest           Manifest
	JSON               []byte
	WorkspaceRoot      string
	WorkingDirectories map[string]string
	PortRanges         map[string]PortRange
}

// Validator applies enabled semantic, safety, and capability rules.
type Validator struct{ enabled map[string]bool }

// NewValidator constructs the manifest validator.
func NewValidator() *Validator { return NewValidatorWithCapabilities() }

// NewValidatorWithCapabilities constructs a validator with explicit executable capabilities.
func NewValidatorWithCapabilities(capabilities ...string) *Validator {
	enabled := make(map[string]bool, len(capabilities))
	for _, capability := range capabilities {
		enabled[capability] = true
	}
	return &Validator{enabled: enabled}
}

// Validate returns a defaulted immutable snapshot without mutating the Loader result.
func (v *Validator) Validate(ctx context.Context, document *Document, workspaceRoot string) (*ValidatedDocument, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if document == nil {
		return nil, fmt.Errorf("manifest document is required")
	}
	root, err := CanonicalWorkspaceRoot(workspaceRoot)
	if err != nil {
		return nil, err
	}
	manifest := cloneManifest(document.Manifest)
	applyDefaults(&manifest)
	if err := validatePolicyDurations(manifest.Spec.Policies); err != nil {
		return nil, err
	}
	if err := validateManifestIDs(manifest); err != nil {
		return nil, err
	}
	ranges, err := validatePorts(manifest.Spec.Ports)
	if err != nil {
		return nil, err
	}
	workingDirectories, err := v.validateServices(ctx, manifest, root)
	if err != nil {
		return nil, err
	}
	if err := validateDependencyGraph(manifest.Spec.Services); err != nil {
		return nil, err
	}
	if err := validatePortOwners(manifest.Spec.Ports, manifest.Spec.Services); err != nil {
		return nil, err
	}
	normalized, err := json.Marshal(manifest)
	if err != nil {
		return nil, fmt.Errorf("encode validated manifest: %w", err)
	}
	return &ValidatedDocument{
		Manifest: manifest, JSON: normalized, WorkspaceRoot: root,
		WorkingDirectories: workingDirectories, PortRanges: ranges,
	}, nil
}

func validateManifestIDs(manifest Manifest) error {
	if _, err := domain.ParseSystemID(manifest.Metadata.ID); err != nil {
		return newValidationError("$.metadata", "id", ErrSemanticInvalid)
	}
	for serviceID := range manifest.Spec.Services {
		if _, err := domain.ParseServiceID(serviceID); err != nil {
			return newValidationError("$.spec.services", serviceID, ErrSemanticInvalid)
		}
	}
	return nil
}

func (v *Validator) validateServices(ctx context.Context, manifest Manifest, root string) (map[string]string, error) {
	resolved := make(map[string]string, len(manifest.Spec.Services))
	for serviceID, service := range manifest.Spec.Services {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		path := "$.spec.services." + serviceID
		if service.Driver == "compose" {
			if err := validateComposeService(service, manifest.Spec.Ports, root, path); err != nil {
				return nil, err
			}
		}
		if err := v.validateServiceCapabilities(service, path); err != nil {
			return nil, err
		}
		if err := validatePythonVenv(service, root, path); err != nil {
			return nil, err
		}
		if err := validateServiceDurations(service, manifest.Spec.Policies, path); err != nil {
			return nil, err
		}
		if err := validateRestart(service, path); err != nil {
			return nil, err
		}
		if err := validateServiceTemplates(service, manifest.Spec.Ports, path); err != nil {
			return nil, err
		}
		workingDirectory := root
		if service.Driver != "compose" {
			var err error
			workingDirectory, err = resolveWorkspaceDirectory(root, service.WorkingDirectory, path+".workingDirectory")
			if err != nil {
				return nil, err
			}
		}
		if service.Mode == "oneshot" {
			if service.Readiness != nil || service.Liveness != nil || service.Restart.Policy != "never" {
				return nil, newValidationError(path, "readiness", ErrSemanticInvalid)
			}
		} else if err := validateHealthCheck(service.Readiness, manifest.Spec.Ports, manifest.Spec.Policies, path+".readiness"); err != nil {
			return nil, err
		}
		if service.Liveness != nil {
			if err := validateHealthCheck(service.Liveness, manifest.Spec.Ports, manifest.Spec.Policies, path+".liveness"); err != nil {
				return nil, err
			}
		}
		for dependency, condition := range service.DependsOn {
			upstreamMode := manifest.Spec.Services[dependency].Mode
			if (condition == "completed" && upstreamMode != "oneshot") || (condition == "ready" && upstreamMode == "oneshot") {
				return nil, newValidationError(path+".dependsOn", dependency, ErrSemanticInvalid)
			}
		}
		resolved[serviceID] = workingDirectory
	}
	return resolved, nil
}

func validateComposeService(service Service, ports map[string]Port, root, path string) error {
	if service.Compose == nil || service.Compose.File == "" || len(service.Compose.Services) == 0 {
		return newValidationError(path, "compose", ErrSemanticInvalid)
	}
	if service.Mode != "daemon" || service.Runner != "" || service.VirtualEnvironment != "" ||
		service.WorkingDirectory != "" || service.Arguments != nil || service.Readiness == nil || service.Readiness.Type != "compose" {
		return newValidationError(path, "driver", ErrSemanticInvalid)
	}
	if _, err := ResolveWorkspaceFile(root, service.Compose.File, path+".compose.file"); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(service.Compose.Services))
	for _, name := range service.Compose.Services {
		if name == "" {
			return newValidationError(path+".compose.services", "", ErrSemanticInvalid)
		}
		if _, exists := seen[name]; exists {
			return newValidationError(path+".compose.services", name, ErrSemanticInvalid)
		}
		seen[name] = struct{}{}
	}
	if err := validateComposePolicy(*service.Compose, seen, path); err != nil {
		return err
	}
	return validateComposeMappings(*service.Compose, ports, seen, path)
}

// EffectiveComposeBuildPolicy returns the closed build behavior for a Compose service.
func EffectiveComposeBuildPolicy(compose ComposeService) string {
	if compose.BuildPolicy == "" {
		return "never"
	}
	return compose.BuildPolicy
}

// EffectiveComposeReadiness returns a closed requirement for every managed service.
func EffectiveComposeReadiness(compose ComposeService) map[string]string {
	result := make(map[string]string, len(compose.Services))
	for _, name := range compose.Services {
		result[name] = "healthy"
	}
	for name, requirement := range compose.Readiness {
		result[name] = requirement
	}
	return result
}

func validateComposePolicy(compose ComposeService, services map[string]struct{}, path string) error {
	policy := EffectiveComposeBuildPolicy(compose)
	if policy != "never" && policy != "always" {
		return newValidationError(path+".compose", "buildPolicy", ErrSemanticInvalid)
	}
	for service, requirement := range compose.Readiness {
		if _, exists := services[service]; !exists {
			return newValidationError(path+".compose.readiness", service, ErrReferenceNotFound)
		}
		if requirement != "healthy" && requirement != "running" {
			return newValidationError(path+".compose.readiness", service, ErrSemanticInvalid)
		}
	}
	if len(compose.Readiness) != 0 && len(compose.Readiness) != len(services) {
		return newValidationError(path+".compose", "readiness", ErrSemanticInvalid)
	}
	return nil
}

func validateComposeMappings(compose ComposeService, ports map[string]Port, services map[string]struct{}, path string) error {
	for logicalName, mapping := range compose.Ports {
		if _, exists := ports[logicalName]; !exists {
			return newValidationError(path+".compose.ports", logicalName, ErrReferenceNotFound)
		}
		if _, exists := services[mapping.Service]; !exists || mapping.Target < 1 || mapping.Target > 65535 {
			return newValidationError(path+".compose.ports", logicalName, ErrSemanticInvalid)
		}
	}
	for service, environment := range compose.Environment {
		if _, exists := services[service]; !exists {
			return newValidationError(path+".compose.environment", service, ErrReferenceNotFound)
		}
		for name, value := range environment {
			if err := validateTemplateValue(value, ports, false, path+".compose.environment."+service+"."+name); err != nil {
				return err
			}
		}
	}
	return nil
}

func validatePythonVenv(service Service, root, path string) error {
	if service.Runner != "python-venv" {
		if service.VirtualEnvironment != "" {
			return newValidationError(path, "virtualEnvironment", ErrSemanticInvalid)
		}
		return nil
	}
	if service.VirtualEnvironment == "" {
		return newValidationError(path, "virtualEnvironment", ErrSemanticInvalid)
	}
	_, err := resolveWorkspaceDirectory(root, service.VirtualEnvironment, path+".virtualEnvironment")
	return err
}
