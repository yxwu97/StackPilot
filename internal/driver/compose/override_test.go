package compose

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"stackpilot/internal/domain"
)

const (
	testOperationID = domain.OperationID("op_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	testWorkspaceID = domain.WorkspaceID("ws_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	testInstanceID  = domain.SystemInstanceID("si_01ARZ3NDEKTSV4RRFFQ69G5FAV")
)

func TestOverrideGeneratorWritesOnlyApprovedFields(t *testing.T) {
	generator := newTestOverrideGenerator(t)
	request := validOverrideRequest()
	result, err := generator.Generate(request)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	contents, err := os.ReadFile(result.Path)
	if err != nil {
		t.Fatalf("read override: %v", err)
	}
	assertApprovedOverride(t, contents)
	if result.Digest == "" || !strings.HasSuffix(result.Path, filepath.Join(testOperationID.String(), overrideFileName)) {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestOverrideGeneratorIsIdempotentAndDetectsConflict(t *testing.T) {
	generator := newTestOverrideGenerator(t)
	request := validOverrideRequest()
	first, err := generator.Generate(request)
	if err != nil {
		t.Fatalf("first Generate() error = %v", err)
	}
	second, err := generator.Generate(request)
	if err != nil {
		t.Fatalf("second Generate() error = %v", err)
	}
	if first.Digest != second.Digest || first.Path != second.Path {
		t.Fatalf("idempotent results differ: first=%+v second=%+v", first, second)
	}
	request.Environment["database"]["DATABASE_NAME"] = "changed"
	if _, err := generator.Generate(request); !errors.Is(err, ErrOverrideConflict) {
		t.Fatalf("Generate(conflict) error = %v, want ErrOverrideConflict", err)
	}
}

func TestOverrideGeneratorRejectsInvalidInputs(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*OverrideRequest)
	}{
		{name: "undeclared service", mutate: func(request *OverrideRequest) {
			request.Ports["database"] = PortOverride{Service: "missing", Target: 5432, Published: 15432}
		}},
		{name: "invalid target", mutate: func(request *OverrideRequest) {
			request.Ports["database"] = PortOverride{Service: "database", Target: 0, Published: 15432}
		}},
		{name: "privileged host port", mutate: func(request *OverrideRequest) {
			request.Ports["database"] = PortOverride{Service: "database", Target: 5432, Published: 80}
		}},
		{name: "invalid environment name", mutate: func(request *OverrideRequest) { request.Environment["database"]["BAD-NAME"] = "value" }},
		{name: "unexpanded template", mutate: func(request *OverrideRequest) { request.Environment["database"]["DATABASE_PORT"] = "${ports.database}" }},
		{name: "nul environment", mutate: func(request *OverrideRequest) { request.Environment["database"]["DATABASE_NAME"] = "bad\x00value" }},
		{name: "newline environment", mutate: func(request *OverrideRequest) { request.Environment["database"]["DATABASE_NAME"] = "bad\nvalue" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := validOverrideRequest()
			test.mutate(&request)
			if _, err := newTestOverrideGenerator(t).Generate(request); !errors.Is(err, ErrOverrideInvalid) {
				t.Fatalf("Generate() error = %v, want ErrOverrideInvalid", err)
			}
		})
	}
}

func TestOverrideGeneratorRejectsRuntimeLinkEscape(t *testing.T) {
	dataDir := t.TempDir()
	outside := t.TempDir()
	runtimeDirectory := filepath.Join(dataDir, "runtime")
	if err := os.Symlink(outside, runtimeDirectory); err != nil {
		t.Skipf("directory symlinks are unavailable: %v", err)
	}
	generator, err := NewOverrideGenerator(dataDir)
	if err != nil {
		t.Fatalf("NewOverrideGenerator() error = %v", err)
	}
	if _, err := generator.Generate(validOverrideRequest()); !errors.Is(err, ErrOverrideInvalid) {
		t.Fatalf("Generate(link escape) error = %v, want ErrOverrideInvalid", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "operations")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("escape created an outside directory: %v", err)
	}
}

func TestValidateOverrideBytesRejectsUnknownAndMultiDocumentYAML(t *testing.T) {
	expected := buildOverride(validOverrideRequest())
	unknown := []byte("services:\n  database:\n    command: [unsafe]\n    labels: {}\n")
	if err := validateOverrideBytes(unknown, expected); !errors.Is(err, ErrOverrideInvalid) {
		t.Fatalf("unknown field error = %v, want ErrOverrideInvalid", err)
	}
	contents, err := yaml.Marshal(expected)
	if err != nil {
		t.Fatalf("marshal expected override: %v", err)
	}
	contents = append(contents, []byte("---\nservices: {}\n")...)
	if err := validateOverrideBytes(contents, expected); !errors.Is(err, ErrOverrideInvalid) {
		t.Fatalf("multiple documents error = %v, want ErrOverrideInvalid", err)
	}
}

func validOverrideRequest() OverrideRequest {
	return OverrideRequest{
		OperationID: testOperationID,
		SystemID:    domain.SystemID("btc"),
		WorkspaceID: testWorkspaceID,
		InstanceID:  testInstanceID,
		Services:    []string{"web", "database"},
		Ports: map[string]PortOverride{
			"web":      {Service: "web", Target: 8080, Published: 18080},
			"database": {Service: "database", Target: 5432, Published: 15432},
		},
		Environment: map[string]map[string]string{
			"database": {"DATABASE_NAME": "stackpilot", "DATABASE_PORT": "15432"},
		},
	}
}

func newTestOverrideGenerator(t *testing.T) *OverrideGenerator {
	t.Helper()
	generator, err := NewOverrideGenerator(t.TempDir())
	if err != nil {
		t.Fatalf("NewOverrideGenerator() error = %v", err)
	}
	return generator
}

func assertApprovedOverride(t *testing.T, contents []byte) {
	t.Helper()
	for _, forbidden := range [][]byte{[]byte("command:"), []byte("volumes:"), []byte("privileged:"), []byte("secret"), []byte("${")} {
		if bytes.Contains(bytes.ToLower(contents), forbidden) {
			t.Fatalf("override contains forbidden field or value %q:\n%s", forbidden, contents)
		}
	}
	text := string(contents)
	for _, expected := range []string{"host_ip: 127.0.0.1", "published: \"15432\"", "target: 5432", "DATABASE_NAME: stackpilot", "stackpilot.instance:", "stackpilot.service:"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("override missing %q:\n%s", expected, text)
		}
	}
	if strings.Index(text, "database:") > strings.Index(text, "web:") {
		t.Fatalf("services are not deterministically sorted:\n%s", text)
	}
}
