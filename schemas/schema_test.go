package schemas_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v5"
	"gopkg.in/yaml.v3"

	"stackpilot/internal/capability"
)

const schemaURL = "https://stackpilot.dev/schemas/system-v1alpha1.schema.json"

func TestManifestExamplesMatchSchema(t *testing.T) {
	schema := compileSchema(t)
	paths, err := filepath.Glob(filepath.Join("examples", "*.yaml"))
	if err != nil {
		t.Fatalf("list manifest examples: %v", err)
	}
	if len(paths) < 2 {
		t.Fatalf("manifest example count = %d, want at least 2", len(paths))
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			document := readYAMLDocument(t, path)
			if err := schema.Validate(document); err != nil {
				t.Fatalf("validate example: %v", err)
			}
		})
	}
}

func TestSchemaRejectsInvalidManifestShapes(t *testing.T) {
	schema := compileSchema(t)
	tests := []struct {
		name     string
		manifest string
	}{
		{name: "wrong API version", manifest: manifestWith(`apiVersion: stackpilot.io/v1`)},
		{name: "wrong kind", manifest: manifestWith(`kind: Service`)},
		{name: "unknown root field", manifest: manifestWith(`unexpected: true`)},
		{name: "invalid system ID", manifest: manifestWith("metadata:\n  id: Invalid_ID\n  name: Invalid")},
		{name: "no services", manifest: manifestWith("spec:\n  services: {}")},
		{name: "invalid service ID", manifest: manifestWith(validService("Invalid_ID"))},
		{name: "unknown service field", manifest: manifestWith(validService("app") + "      command: arbitrary\n")},
		{name: "daemon without readiness", manifest: manifestWith(serviceWithoutReadiness("app", "daemon"))},
		{name: "oneshot with readiness", manifest: manifestWith(validServiceWithMode("app", "oneshot"))},
		{name: "invalid dependency condition", manifest: manifestWith(validService("app") + "      dependsOn:\n        upstream: started\n")},
		{name: "unsafe environment name", manifest: manifestWith(validService("app") + "      environment:\n        BAD-NAME: value\n")},
		{name: "privileged port", manifest: manifestWith(serviceWithPrivilegedPort())},
		{name: "tcp target missing", manifest: manifestWith(serviceWithIncompleteTCPHealth())},
		{name: "python venv missing", manifest: manifestWith(strings.Replace(validService("app"), "runner: java", "runner: python-venv", 1))},
		{name: "venv on non-python", manifest: manifestWith(validService("app") + "      virtualEnvironment: .venv\n")},
		{name: "compose missing definition", manifest: manifestWith("spec:\n  services:\n    app:\n      driver: compose\n      readiness:\n        type: compose\n")},
		{name: "compose with process fields", manifest: manifestWith(composeService("app", "") + "      runner: java\n")},
		{name: "compose oneshot", manifest: manifestWith(strings.Replace(composeService("app", ""), "driver: compose", "driver: compose\n      mode: oneshot", 1))},
		{name: "process compose readiness", manifest: manifestWith(strings.Replace(validService("app"), "type: process", "type: compose", 1))},
		{name: "compose duplicate service", manifest: manifestWith(composeService("app", "          - database\n"))},
		{name: "compose invalid build policy", manifest: manifestWith(strings.Replace(composeService("app", ""), "        services:\n", "        buildPolicy: sometimes\n        services:\n", 1))},
		{name: "compose invalid readiness", manifest: manifestWith(strings.Replace(composeService("app", ""), "        services:\n", "        readiness:\n          database: started\n        services:\n", 1))},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := decodeYAML(t, []byte(test.manifest))
			if err := schema.Validate(document); err == nil {
				t.Fatal("schema accepted invalid manifest")
			}
		})
	}
}

func TestSchemaAcceptsPythonVenvBinding(t *testing.T) {
	schema := compileSchema(t)
	manifest := strings.Replace(validService("app"), "runner: java", "runner: python-venv\n      virtualEnvironment: .venv", 1)
	if err := schema.Validate(decodeYAML(t, []byte(manifestWith(manifest)))); err != nil {
		t.Fatalf("validate python-venv service: %v", err)
	}
}

func TestSchemaAcceptsGoRunner(t *testing.T) {
	manifest := strings.Replace(validService("app"), "runner: java", "runner: go", 1)
	if err := compileSchema(t).Validate(decodeYAML(t, []byte(manifestWith(manifest)))); err != nil {
		t.Fatalf("validate go service: %v", err)
	}
}

func TestSchemaAcceptsComposeReferenceShape(t *testing.T) {
	schema := compileSchema(t)
	if err := schema.Validate(decodeYAML(t, []byte(manifestWith(composeService("infra", ""))))); err != nil {
		t.Fatalf("validate Compose service: %v", err)
	}
	mapped := strings.Replace(composeService("infra", ""), "      readiness:", "        ports:\n          database:\n            service: database\n            target: 5432\n        environment:\n          database:\n            DATABASE_PORT: \"${ports.database}\"\n      readiness:", 1)
	mapped = strings.Replace(mapped, "spec:\n", "spec:\n  ports:\n    database:\n      protocol: tcp\n      preferred: 15432\n", 1)
	if err := schema.Validate(decodeYAML(t, []byte(manifestWith(mapped)))); err != nil {
		t.Fatalf("validate mapped Compose service: %v", err)
	}
}

func TestSchemaAcceptsComposeBuildAndReadinessPolicy(t *testing.T) {
	schema := compileSchema(t)
	value := strings.Replace(composeService("infra", ""), "        services:\n", "        buildPolicy: always\n        readiness:\n          database: running\n        services:\n", 1)
	if err := schema.Validate(decodeYAML(t, []byte(manifestWith(value)))); err != nil {
		t.Fatalf("validate Compose build policy: %v", err)
	}
}

func TestSchemaCapabilityAnnotationsUseRegisteredNames(t *testing.T) {
	t.Parallel()
	contents, err := os.ReadFile("system-v1alpha1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var document any
	if err := json.Unmarshal(contents, &document); err != nil {
		t.Fatal(err)
	}
	for _, name := range schemaCapabilityNames(document) {
		if !capability.Known(name) {
			t.Errorf("schema capability %q is not in the Go registry", name)
		}
	}
}

func schemaCapabilityNames(value any) []string {
	var result []string
	switch typed := value.(type) {
	case []any:
		for _, child := range typed {
			result = append(result, schemaCapabilityNames(child)...)
		}
	case map[string]any:
		for key, child := range typed {
			if key == "x-stackpilot-capability" {
				switch annotation := child.(type) {
				case string:
					result = append(result, annotation)
				case map[string]any:
					for _, name := range annotation {
						if text, ok := name.(string); ok {
							result = append(result, text)
						}
					}
				}
			}
			result = append(result, schemaCapabilityNames(child)...)
		}
	}
	return result
}

func compileSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	contents, err := os.ReadFile("system-v1alpha1.schema.json")
	if err != nil {
		t.Fatalf("read manifest schema: %v", err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource(schemaURL, strings.NewReader(string(contents))); err != nil {
		t.Fatalf("add manifest schema: %v", err)
	}
	schema, err := compiler.Compile(schemaURL)
	if err != nil {
		t.Fatalf("compile manifest schema: %v", err)
	}
	return schema
}

func readYAMLDocument(t *testing.T, path string) any {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read YAML document: %v", err)
	}
	return decodeYAML(t, contents)
}

func decodeYAML(t *testing.T, contents []byte) any {
	t.Helper()
	var yamlValue any
	if err := yaml.Unmarshal(contents, &yamlValue); err != nil {
		t.Fatalf("parse YAML: %v", err)
	}
	jsonValue, err := json.Marshal(yamlValue)
	if err != nil {
		t.Fatalf("normalize YAML as JSON: %v", err)
	}
	var document any
	if err := json.Unmarshal(jsonValue, &document); err != nil {
		t.Fatalf("parse normalized JSON: %v", err)
	}
	return document
}

func manifestWith(replacement string) string {
	base := "apiVersion: stackpilot.io/v1alpha1\nkind: System\nmetadata:\n  id: example\n  name: Example\n" + validService("app")
	key := strings.SplitN(replacement, ":", 2)[0] + ":"
	lines := strings.Split(base, "\n")
	for index, line := range lines {
		if strings.HasPrefix(line, key) {
			end := index + 1
			for end < len(lines) && (strings.HasPrefix(lines[end], " ") || lines[end] == "") {
				end++
			}
			lines = append(lines[:index], append(strings.Split(replacement, "\n"), lines[end:]...)...)
			return strings.Join(lines, "\n")
		}
	}
	return strings.TrimSuffix(base, "\n") + "\n" + replacement + "\n"
}

func validService(name string) string {
	return validServiceWithMode(name, "daemon")
}

func validServiceWithMode(name, mode string) string {
	return serviceWithoutReadiness(name, mode) + "      readiness:\n        type: process\n"
}

func serviceWithoutReadiness(name, mode string) string {
	return "spec:\n  services:\n    " + name + ":\n      driver: process\n      mode: " + mode + "\n      runner: java\n      workingDirectory: ./app\n      arguments: []\n"
}

func serviceWithPrivilegedPort() string {
	return "spec:\n  ports:\n    web:\n      protocol: tcp\n      preferred: 80\n" +
		"  services:\n    app:\n      driver: process\n      runner: java\n      workingDirectory: ./app\n      arguments: []\n" +
		"      readiness:\n        type: process\n"
}

func serviceWithIncompleteTCPHealth() string {
	return "spec:\n  services:\n    app:\n      driver: process\n      runner: java\n      workingDirectory: ./app\n      arguments: []\n" +
		"      readiness:\n        type: tcp\n"
}

func composeService(name, extraServices string) string {
	return "spec:\n  services:\n    " + name + ":\n      driver: compose\n" +
		"      compose:\n        file: ./infra/compose.yaml\n        services:\n          - database\n" + extraServices +
		"      readiness:\n        type: compose\n"
}
