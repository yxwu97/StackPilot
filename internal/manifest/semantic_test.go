package manifest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestValidatorAppliesDefaultsWithoutMutatingLoaderDocument(t *testing.T) {
	root, document := semanticFixture(t)
	validated, err := NewValidator().Validate(context.Background(), document, root)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	backend := validated.Manifest.Spec.Services["backend"]
	if backend.Mode != "daemon" || backend.Required == nil || !*backend.Required || backend.Stop.GracefulTimeout != "15s" ||
		backend.Restart.Policy != "never" || backend.Restart.InitialBackoff != "1s" || backend.Restart.MaxBackoff != "1m" ||
		backend.Restart.MaxAttempts == nil || *backend.Restart.MaxAttempts != 3 || backend.Restart.StableWindow != "5m" {
		t.Fatalf("defaulted backend = %#v", backend)
	}
	if document.Manifest.Spec.Services["backend"].Mode != "" || document.Manifest.Spec.Policies.StartTimeout != "" {
		t.Fatal("Validate() mutated the Loader document")
	}
	if validated.Manifest.Spec.Policies.StartTimeout != "10m" || validated.Manifest.Spec.Policies.StopTimeout != "2m" {
		t.Fatalf("defaulted policies = %#v", validated.Manifest.Spec.Policies)
	}
	if validated.PortRanges["backend"] != (PortRange{Start: 8200, End: 8399}) || len(validated.JSON) == 0 {
		t.Fatalf("validated range/JSON = (%#v, %d bytes)", validated.PortRanges, len(validated.JSON))
	}
	if validated.WorkingDirectories["backend"] != filepath.Join(root, "backend") {
		t.Fatalf("backend working directory = %q", validated.WorkingDirectories["backend"])
	}
}

func TestValidatorRejectsWorkspacePathEscape(t *testing.T) {
	root, document := semanticFixture(t)
	outside := filepath.Join(filepath.Dir(root), "outside")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatalf("create outside directory: %v", err)
	}
	backend := document.Manifest.Spec.Services["backend"]
	backend.WorkingDirectory = "../outside"
	document.Manifest.Spec.Services["backend"] = backend
	_, err := NewValidator().Validate(context.Background(), document, root)
	if !errors.Is(err, ErrPathOutsideWorkspace) {
		t.Fatalf("Validate(path escape) error = %v, want ErrPathOutsideWorkspace", err)
	}
}

func TestValidatorRejectsLinkedDirectoryEscape(t *testing.T) {
	root, document := semanticFixture(t)
	outside := t.TempDir()
	link := filepath.Join(root, "linked-outside")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symbolic links are unavailable in this environment: %v", err)
	}
	backend := document.Manifest.Spec.Services["backend"]
	backend.WorkingDirectory = "./linked-outside"
	document.Manifest.Spec.Services["backend"] = backend
	_, err := NewValidator().Validate(context.Background(), document, root)
	if !errors.Is(err, ErrPathOutsideWorkspace) {
		t.Fatalf("Validate(link escape) error = %v, want ErrPathOutsideWorkspace", err)
	}
}

func TestValidatorRejectsInvalidPortRanges(t *testing.T) {
	tests := []string{"8399-8200", "1000-1200", "2000-4000"}
	for _, value := range tests {
		t.Run(value, func(t *testing.T) {
			root, document := semanticFixture(t)
			port := document.Manifest.Spec.Ports["backend"]
			port.FallbackRange = value
			document.Manifest.Spec.Ports["backend"] = port
			_, err := NewValidator().Validate(context.Background(), document, root)
			if !errors.Is(err, ErrPortRangeInvalid) {
				t.Fatalf("Validate() error = %v, want ErrPortRangeInvalid", err)
			}
		})
	}
}

func TestValidatorRequiresExactlyOneReadinessOwnerPerPort(t *testing.T) {
	root, document := semanticFixture(t)
	backend := document.Manifest.Spec.Services["backend"]
	duplicate := backend
	duplicate.WorkingDirectory = "."
	document.Manifest.Spec.Services["duplicate"] = duplicate
	_, err := NewValidator().Validate(context.Background(), document, root)
	if !errors.Is(err, ErrSemanticInvalid) {
		t.Fatalf("duplicate port owner error = %v", err)
	}
	delete(document.Manifest.Spec.Services, "duplicate")
	threshold := 1
	backend.Readiness = &HealthCheck{Type: "process", Timeout: "5s", Interval: "100ms", SuccessThreshold: &threshold, FailureThreshold: &threshold}
	document.Manifest.Spec.Services["backend"] = backend
	_, err = NewValidator().Validate(context.Background(), document, root)
	if !errors.Is(err, ErrSemanticInvalid) {
		t.Fatalf("missing port owner error = %v", err)
	}
}

func TestValidatorRejectsDependencyErrors(t *testing.T) {
	tests := []struct {
		name       string
		dependency string
		want       error
	}{
		{name: "missing", dependency: "missing", want: ErrReferenceNotFound},
		{name: "self", dependency: "web", want: ErrDependencyCycle},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root, document := semanticFixture(t)
			web := document.Manifest.Spec.Services["web"]
			web.DependsOn = map[string]string{test.dependency: "ready"}
			document.Manifest.Spec.Services["web"] = web
			_, err := NewValidator().Validate(context.Background(), document, root)
			if !errors.Is(err, test.want) {
				t.Fatalf("Validate() error = %v, want %v", err, test.want)
			}
		})
	}
	root, document := semanticFixture(t)
	backend := document.Manifest.Spec.Services["backend"]
	backend.DependsOn = map[string]string{"web": "ready"}
	document.Manifest.Spec.Services["backend"] = backend
	_, err := NewValidator().Validate(context.Background(), document, root)
	if !errors.Is(err, ErrDependencyCycle) {
		t.Fatalf("Validate(cycle) error = %v, want ErrDependencyCycle", err)
	}
}

func TestValidatorRejectsInvalidTemplatesAndReferences(t *testing.T) {
	tests := []struct {
		name     string
		argument string
		want     error
	}{
		{name: "unknown port", argument: "${ports.missing}", want: ErrReferenceNotFound},
		{name: "environment lookup", argument: "${env.PATH}", want: ErrTemplateInvalid},
		{name: "command substitution", argument: "$(whoami)", want: ErrTemplateInvalid},
		{name: "malformed", argument: "${ports.backend", want: ErrTemplateInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root, document := semanticFixture(t)
			backend := document.Manifest.Spec.Services["backend"]
			backend.Arguments = []string{test.argument}
			document.Manifest.Spec.Services["backend"] = backend
			_, err := NewValidator().Validate(context.Background(), document, root)
			if !errors.Is(err, test.want) {
				t.Fatalf("Validate() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestValidatorRejectsDisabledFeaturesAtRegistration(t *testing.T) {
	tests := []struct {
		name    string
		feature string
		mutate  func(Service) Service
	}{
		{name: "liveness", feature: "liveness", mutate: func(service Service) Service { service.Liveness = cloneHealth(service.Readiness); return service }},
		{name: "restart", feature: "auto-restart", mutate: func(service Service) Service { service.Restart.Policy = "always"; return service }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root, document := semanticFixture(t)
			backend := test.mutate(document.Manifest.Spec.Services["backend"])
			document.Manifest.Spec.Services["backend"] = backend
			_, err := NewValidator().Validate(context.Background(), document, root)
			var featureError *FeatureError
			if !errors.As(err, &featureError) || featureError.Feature != test.feature {
				t.Fatalf("Validate() error = %v, want disabled feature %q", err, test.feature)
			}
		})
	}

}

func TestValidatorAcceptsWorkspacePythonVenvAndRejectsInvalidBindings(t *testing.T) {
	root, document := semanticFixture(t)
	if err := os.Mkdir(filepath.Join(root, ".venv"), 0o700); err != nil {
		t.Fatalf("create virtual environment: %v", err)
	}
	backend := document.Manifest.Spec.Services["backend"]
	backend.Runner = "python-venv"
	backend.VirtualEnvironment = ".venv"
	document.Manifest.Spec.Services["backend"] = backend
	if _, err := NewValidator().Validate(context.Background(), document, root); err != nil {
		t.Fatalf("Validate(python-venv) error = %v", err)
	}

	backend.VirtualEnvironment = ""
	document.Manifest.Spec.Services["backend"] = backend
	if _, err := NewValidator().Validate(context.Background(), document, root); !errors.Is(err, ErrSemanticInvalid) {
		t.Fatalf("Validate(missing venv) error = %v, want ErrSemanticInvalid", err)
	}
	backend.Runner = "java"
	backend.VirtualEnvironment = ".venv"
	document.Manifest.Spec.Services["backend"] = backend
	if _, err := NewValidator().Validate(context.Background(), document, root); !errors.Is(err, ErrSemanticInvalid) {
		t.Fatalf("Validate(non-python venv) error = %v, want ErrSemanticInvalid", err)
	}
}

func TestValidatorGatesGoRunner(t *testing.T) {
	root, document := semanticFixture(t)
	service := document.Manifest.Spec.Services["backend"]
	service.Runner = "go"
	document.Manifest.Spec.Services["backend"] = service

	_, err := NewValidator().Validate(context.Background(), document, root)
	var feature *FeatureError
	if !errors.As(err, &feature) || feature.Feature != "go" {
		t.Fatalf("Validate(go disabled) error = %v", err)
	}
	if _, err := NewValidatorWithCapabilities("go").Validate(context.Background(), document, root); err != nil {
		t.Fatalf("Validate(go enabled) error = %v", err)
	}
}

func TestValidatorRejectsPythonVenvEscape(t *testing.T) {
	root, document := semanticFixture(t)
	outside := t.TempDir()
	link := filepath.Join(root, ".venv")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symbolic links are unavailable in this environment: %v", err)
	}
	backend := document.Manifest.Spec.Services["backend"]
	backend.Runner = "python-venv"
	backend.VirtualEnvironment = ".venv"
	document.Manifest.Spec.Services["backend"] = backend
	if _, err := NewValidator().Validate(context.Background(), document, root); !errors.Is(err, ErrPathOutsideWorkspace) {
		t.Fatalf("Validate(venv escape) error = %v, want ErrPathOutsideWorkspace", err)
	}
}

func TestValidatorChecksComposeFileBeforeCapabilityGate(t *testing.T) {
	root := t.TempDir()
	infra := filepath.Join(root, "infra")
	if err := os.Mkdir(infra, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(infra, "compose.yaml"), []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	document, err := newTestLoader(t).Parse([]byte(composeManifest("./infra/compose.yaml")))
	if err != nil {
		t.Fatalf("parse Compose manifest: %v", err)
	}
	_, err = NewValidator().Validate(context.Background(), document, root)
	var featureError *FeatureError
	if !errors.As(err, &featureError) || featureError.Feature != "compose" {
		t.Fatalf("Validate(Compose) error = %v, want disabled compose feature", err)
	}

	outside := filepath.Join(t.TempDir(), "outside.yaml")
	if err := os.WriteFile(outside, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	relativeOutside, err := filepath.Rel(root, outside)
	if err != nil {
		t.Fatal(err)
	}
	document, err = newTestLoader(t).Parse([]byte(composeManifest(filepath.ToSlash(relativeOutside))))
	if err != nil {
		t.Fatalf("parse escaped Compose manifest: %v", err)
	}
	if _, err = NewValidator().Validate(context.Background(), document, root); !errors.Is(err, ErrPathOutsideWorkspace) {
		t.Fatalf("Validate(escaped Compose file) error = %v, want ErrPathOutsideWorkspace", err)
	}
}

func TestValidatorAcceptsComposeOnlyWhenExplicitlyEnabled(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "compose.yaml"), []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	document, err := newTestLoader(t).Parse([]byte(composeManifest("./compose.yaml")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewValidatorWithCapabilities("compose").Validate(context.Background(), document, root); err != nil {
		t.Fatalf("Validate(enabled Compose) error = %v", err)
	}
}

func TestValidatorGatesComposeBuildAndClosesReadinessRequirements(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "compose.yaml"), []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	document, err := newTestLoader(t).Parse([]byte(composeManifest("./compose.yaml")))
	if err != nil {
		t.Fatal(err)
	}
	service := document.Manifest.Spec.Services["infrastructure"]
	service.Compose.BuildPolicy = "always"
	service.Compose.Readiness = map[string]string{"database": "running"}
	document.Manifest.Spec.Services["infrastructure"] = service
	_, err = NewValidatorWithCapabilities("compose").Validate(context.Background(), document, root)
	var feature *FeatureError
	if !errors.As(err, &feature) || feature.Feature != "compose-build" {
		t.Fatalf("Validate(build disabled) error = %v", err)
	}
	validated, err := NewValidatorWithCapabilities("compose", "compose-build").Validate(context.Background(), document, root)
	if err != nil || EffectiveComposeBuildPolicy(*validated.Manifest.Spec.Services["infrastructure"].Compose) != "always" {
		t.Fatalf("Validate(build enabled) = %#v, %v", validated, err)
	}

	service.Compose.Services = append(service.Compose.Services, "cache")
	document.Manifest.Spec.Services["infrastructure"] = service
	if _, err := NewValidatorWithCapabilities("compose", "compose-build").Validate(context.Background(), document, root); !errors.Is(err, ErrSemanticInvalid) {
		t.Fatalf("Validate(incomplete readiness) error = %v", err)
	}
}

func TestComposePolicyDefaultsPreserveOmittedManifestFields(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "compose.yaml"), []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	document, err := newTestLoader(t).Parse([]byte(composeManifest("./compose.yaml")))
	if err != nil {
		t.Fatal(err)
	}
	validated, err := NewValidatorWithCapabilities("compose").Validate(context.Background(), document, root)
	if err != nil {
		t.Fatal(err)
	}
	compose := validated.Manifest.Spec.Services["infrastructure"].Compose
	if compose.BuildPolicy != "" || compose.Readiness != nil || EffectiveComposeBuildPolicy(*compose) != "never" || EffectiveComposeReadiness(*compose)["database"] != "healthy" {
		t.Fatalf("unexpected Compose defaults: %#v", compose)
	}
	health := validated.Manifest.Spec.Services["infrastructure"].Readiness
	if health.Timeout != "" || health.Interval != "" || EffectiveHealthTimeout(*health, validated.Manifest.Spec.Policies) != "10m" || EffectiveHealthInterval(*health) != "2s" {
		t.Fatalf("unexpected health defaults: %#v", health)
	}
}

func TestValidatorChecksComposePortAndEnvironmentMappings(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "compose.yaml"), []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	document, err := newTestLoader(t).Parse([]byte(composeMappedManifest("database", "${ports.database}")))
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewValidator().Validate(context.Background(), document, root)
	var featureError *FeatureError
	if !errors.As(err, &featureError) || featureError.Feature != "compose" {
		t.Fatalf("Validate(mapped Compose) error = %v", err)
	}

	document, _ = newTestLoader(t).Parse([]byte(composeMappedManifest("missing", "${ports.database}")))
	if _, err = NewValidator().Validate(context.Background(), document, root); !errors.Is(err, ErrSemanticInvalid) {
		t.Fatalf("Validate(missing Compose service) error = %v", err)
	}
	document, _ = newTestLoader(t).Parse([]byte(composeMappedManifest("database", "${secret.database-password}")))
	if _, err = NewValidator().Validate(context.Background(), document, root); !errors.Is(err, ErrTemplateInvalid) {
		t.Fatalf("Validate(Compose Secret override) error = %v", err)
	}
}

func TestValidatorAllowsOneshotWithoutReadiness(t *testing.T) {
	root, document := semanticFixture(t)
	delete(document.Manifest.Spec.Services, "web")
	document.Manifest.Spec.Ports = nil
	backend := document.Manifest.Spec.Services["backend"]
	backend.Mode = "oneshot"
	backend.Readiness = nil
	backend.Arguments = []string{"--mode=immediate-exit", "--exit-code=0"}
	document.Manifest.Spec.Services["backend"] = backend
	if _, err := NewValidator().Validate(context.Background(), document, root); err != nil {
		t.Fatalf("Validate(oneshot) error = %v", err)
	}

	one := 1
	backend.Readiness = &HealthCheck{Type: "process", Timeout: "5s", Interval: "100ms", SuccessThreshold: &one, FailureThreshold: &one}
	document.Manifest.Spec.Services["backend"] = backend
	if _, err := NewValidator().Validate(context.Background(), document, root); !errors.Is(err, ErrSemanticInvalid) {
		t.Fatalf("Validate(oneshot readiness) error = %v, want ErrSemanticInvalid", err)
	}
}

func TestValidatorRejectsReadyDependencyOnOneshot(t *testing.T) {
	root, document := semanticFixture(t)
	delete(document.Manifest.Spec.Ports, "backend")
	backend := document.Manifest.Spec.Services["backend"]
	backend.Mode = "oneshot"
	backend.Readiness = nil
	backend.Arguments = []string{"-version"}
	document.Manifest.Spec.Services["backend"] = backend
	_, err := NewValidator().Validate(context.Background(), document, root)
	if !errors.Is(err, ErrSemanticInvalid) {
		t.Fatalf("Validate(ready dependency on oneshot) error = %v, want ErrSemanticInvalid", err)
	}
}

func TestValidatorAllowsCompletedDependencyOnlyForOneshot(t *testing.T) {
	root, document := semanticFixture(t)
	delete(document.Manifest.Spec.Ports, "backend")
	backend := document.Manifest.Spec.Services["backend"]
	backend.Mode = "oneshot"
	backend.Readiness = nil
	backend.Arguments = []string{"-version"}
	document.Manifest.Spec.Services["backend"] = backend
	web := document.Manifest.Spec.Services["web"]
	web.DependsOn = map[string]string{"backend": "completed"}
	document.Manifest.Spec.Services["web"] = web
	if _, err := NewValidator().Validate(context.Background(), document, root); err != nil {
		t.Fatalf("Validate(completed oneshot dependency) error = %v", err)
	}

	backend.Mode = "daemon"
	backend.Readiness = cloneHealth(web.Readiness)
	document.Manifest.Spec.Services["backend"] = backend
	if _, err := NewValidator().Validate(context.Background(), document, root); !errors.Is(err, ErrSemanticInvalid) {
		t.Fatalf("Validate(completed daemon dependency) error = %v, want ErrSemanticInvalid", err)
	}
}

func TestValidatorAllowsExactSecretEnvironmentReferences(t *testing.T) {
	root, document := semanticFixture(t)
	backend := document.Manifest.Spec.Services["backend"]
	backend.Environment = map[string]string{"TOKEN": "${secret.api-key}"}
	document.Manifest.Spec.Services["backend"] = backend
	if _, err := NewValidator().Validate(context.Background(), document, root); err != nil {
		t.Fatalf("Validate(exact Secret reference) error = %v", err)
	}
	for _, value := range []string{"prefix-${secret.api-key}", "${secret.api_key}", "${secret.ApiKey}"} {
		backend.Environment["TOKEN"] = value
		document.Manifest.Spec.Services["backend"] = backend
		if _, err := NewValidator().Validate(context.Background(), document, root); !errors.Is(err, ErrTemplateInvalid) {
			t.Fatalf("Validate(%q) error = %v, want invalid template", value, err)
		}
	}
}

func TestSecretReferenceRequiresExactCanonicalPlaceholder(t *testing.T) {
	if name, ok := SecretReference("${secret.database-password}"); !ok || name != "database-password" {
		t.Fatalf("SecretReference(valid) = (%q, %t)", name, ok)
	}
	for _, value := range []string{"prefix-${secret.database-password}", "${secret.database_password}", "${secret.}"} {
		if _, ok := SecretReference(value); ok {
			t.Fatalf("SecretReference(%q) unexpectedly accepted", value)
		}
	}
}

func TestValidatorRestrictsHealthTargetsAndDurations(t *testing.T) {
	root, document := semanticFixture(t)
	web := document.Manifest.Spec.Services["web"]
	web.Readiness.URL = "http://example.com:8080/health"
	document.Manifest.Spec.Services["web"] = web
	_, err := NewValidator().Validate(context.Background(), document, root)
	if !errors.Is(err, ErrHealthTargetUnsafe) {
		t.Fatalf("Validate(remote health) error = %v, want ErrHealthTargetUnsafe", err)
	}

	root, document = semanticFixture(t)
	backend := document.Manifest.Spec.Services["backend"]
	backend.Readiness.Interval = "6s"
	backend.Readiness.Timeout = "10s"
	failures := 2
	backend.Readiness.FailureThreshold = &failures
	document.Manifest.Spec.Services["backend"] = backend
	_, err = NewValidator().Validate(context.Background(), document, root)
	if !errors.Is(err, ErrDurationInvalid) {
		t.Fatalf("Validate(duration relationship) error = %v, want ErrDurationInvalid", err)
	}
}

func TestValidatorValidatesTCPHealthPortForms(t *testing.T) {
	tests := []struct {
		name string
		port any
		want error
	}{
		{name: "static", port: 32102, want: ErrSemanticInvalid},
		{name: "template", port: "${ports.backend}"},
		{name: "suffix", port: "${ports.backend}suffix}", want: ErrTemplateInvalid},
		{name: "unknown port", port: "${ports.missing}", want: ErrReferenceNotFound},
		{name: "privileged", port: 80, want: ErrPortRangeInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root, document := semanticFixture(t)
			backend := document.Manifest.Spec.Services["backend"]
			backend.Readiness.Port = test.port
			document.Manifest.Spec.Services["backend"] = backend
			_, err := NewValidator().Validate(context.Background(), document, root)
			if !errors.Is(err, test.want) {
				t.Fatalf("Validate(TCP port) error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestValidatorHonorsCancellation(t *testing.T) {
	root, document := semanticFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewValidator().Validate(ctx, document, root)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Validate(cancelled) error = %v", err)
	}
}

func semanticFixture(t *testing.T) (string, *Document) {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"backend", "web"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
			t.Fatalf("create service directory: %v", err)
		}
	}
	document, err := newTestLoader(t).Parse([]byte(semanticManifest()))
	if err != nil {
		t.Fatalf("Parse semantic fixture: %v", err)
	}
	return root, document
}

func semanticManifest() string {
	return strings.TrimSpace(`
apiVersion: stackpilot.io/v1alpha1
kind: System
metadata:
  id: sample
  name: Sample
spec:
  ports:
    backend:
      protocol: tcp
      preferred: 8081
      fallbackRange: 8200-8399
  services:
    backend:
      driver: process
      runner: java
      workingDirectory: ./backend
      arguments: ["--port=${ports.backend}"]
      readiness:
        type: tcp
        host: 127.0.0.1
        port: "${ports.backend}"
        timeout: 10s
        interval: 1s
        failureThreshold: 2
    web:
      driver: process
      runner: npm
      workingDirectory: ./web
      arguments: [run, dev]
      dependsOn:
        backend: ready
      readiness:
        type: http
        url: "http://127.0.0.1:32102/health"
        timeout: 10s
        interval: 1s
`) + "\n"
}

func composeManifest(file string) string {
	return strings.TrimSpace(`
apiVersion: stackpilot.io/v1alpha1
kind: System
metadata:
  id: compose-sample
  name: Compose Sample
spec:
  services:
    infrastructure:
      driver: compose
      compose:
        file: `+file+`
        services: [database]
      readiness:
        type: compose
`) + "\n"
}

func composeMappedManifest(service, environmentValue string) string {
	return strings.TrimSpace(`
apiVersion: stackpilot.io/v1alpha1
kind: System
metadata: {id: compose-mapped, name: Compose Mapped}
spec:
  ports:
    database: {protocol: tcp, preferred: 15432}
  services:
    infrastructure:
      driver: compose
      compose:
        file: ./compose.yaml
        services: [database]
        ports:
          database: {service: `+service+`, target: 5432}
        environment:
          database:
            DATABASE_PORT: "`+environmentValue+`"
      readiness: {type: compose}
`) + "\n"
}

func TestPolicyDurationBounds(t *testing.T) {
	root, document := semanticFixture(t)
	document.Manifest.Spec.Policies.StartTimeout = (30*time.Minute + time.Second).String()
	_, err := NewValidator().Validate(context.Background(), document, root)
	if !errors.Is(err, ErrDurationInvalid) {
		t.Fatalf("Validate(policy duration) error = %v, want ErrDurationInvalid", err)
	}
}

func TestManifestErrorCodesAreStable(t *testing.T) {
	tests := []struct {
		err  error
		code string
	}{
		{err: newValidationError("$.spec", "field", ErrSemanticInvalid), code: CodeSemanticInvalid},
		{err: &FeatureError{Path: "$.spec", Feature: "compose"}, code: CodeFeatureNotEnabled},
		{err: errors.New("unrecognized"), code: ""},
	}
	for _, test := range tests {
		code, ok := ErrorCode(test.err)
		if code != test.code || ok != (test.code != "") {
			t.Fatalf("ErrorCode(%v) = (%q, %t), want %q", test.err, code, ok, test.code)
		}
	}
}
