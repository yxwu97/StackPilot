package api

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"testing"

	"gopkg.in/yaml.v3"
)

type openAPIContract struct {
	OpenAPI    string         `yaml:"openapi"`
	Paths      map[string]any `yaml:"paths"`
	Components struct {
		Schemas map[string]struct {
			Enum       []string                   `yaml:"enum"`
			Required   []string                   `yaml:"required"`
			Properties map[string]openAPIProperty `yaml:"properties"`
		} `yaml:"schemas"`
	} `yaml:"components"`
	ErrorCodes map[string]struct {
		HTTPStatus     int      `yaml:"httpStatus"`
		Retryable      bool     `yaml:"retryable"`
		AllowedDetails []string `yaml:"allowedDetails"`
	} `yaml:"x-stackpilot-error-codes"`
}

type openAPIProperty struct {
	Pattern     string `yaml:"pattern"`
	Example     string `yaml:"example"`
	Description string `yaml:"description"`
}

func TestOpenAPIContractMatchesRoutesAndErrorRegistry(t *testing.T) {
	contract := loadOpenAPIContract(t)
	if contract.OpenAPI == "" {
		t.Fatal("OpenAPI version is empty")
	}
	sessionSchema := contract.Components.Schemas["SessionResponse"]
	if !equalStrings(sessionSchema.Required, []string{"csrf", "expiresAt"}) || len(sessionSchema.Properties) != 2 {
		t.Fatalf("SessionResponse renewal contract is incomplete: required=%v properties=%v", sessionSchema.Required, sessionSchema.Properties)
	}
	versionProperty := contract.Components.Schemas["VersionResponse"].Properties["version"]
	if versionProperty.Pattern != `^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$` || versionProperty.Example != "0.1.0" {
		t.Fatalf("VersionResponse.version contract = %#v, want canonical product version", versionProperty)
	}
	for _, path := range []string{
		"/health/live", "/health/ready", "/version", "/api/v1/workspaces",
		"/api/v1/auth/bootstrap", "/api/v1/auth/session",
		"/api/v1/auth/token/rotate", "/api/v1/audit-events",
		"/api/v1/secrets/{systemId}/{secretName}",
		"/api/v1/workspaces/{workspaceId}", "/api/v1/systems", "/api/v1/systems/{systemId}",
		"/api/v1/workspaces/{workspaceId}/refresh",
		"/api/v1/services/{systemId}/{serviceId}",
		"/api/v1/systems/{systemId}/start", "/api/v1/systems/{systemId}/stop",
		"/api/v1/systems/{systemId}/restart", "/api/v1/services/{systemId}/{serviceId}/restart",
		"/api/v1/systems/{systemId}/status", "/api/v1/operations/{operationId}",
		"/api/v1/operations",
		"/api/v1/operations/{operationId}/cancel",
		"/api/v1/events",
		"/api/v1/services/{systemId}/{serviceId}/logs", "/api/v1/log-stream",
		"/api/v1/incidents", "/api/v1/incidents/{incidentId}", "/api/v1/incidents/{incidentId}/analyze",
	} {
		if _, ok := contract.Paths[path]; !ok {
			t.Errorf("OpenAPI path %q is missing", path)
		}
	}

	openAPICodes := contract.Components.Schemas["ErrorCode"].Enum
	sort.Strings(openAPICodes)
	registeredCodes := make([]string, 0, len(errorRegistry))
	for code, spec := range errorRegistry {
		registeredCodes = append(registeredCodes, string(code))
		metadata, ok := contract.ErrorCodes[string(code)]
		if !ok {
			t.Errorf("OpenAPI error metadata for %s is missing", code)
			continue
		}
		if metadata.HTTPStatus != spec.HTTPStatus || metadata.Retryable != spec.Retryable {
			t.Errorf("OpenAPI metadata for %s = (%d, %t), want (%d, %t)", code, metadata.HTTPStatus, metadata.Retryable, spec.HTTPStatus, spec.Retryable)
		}
		if !equalStrings(metadata.AllowedDetails, spec.AllowedDetails) {
			t.Errorf("OpenAPI allowedDetails for %s = %v, want %v", code, metadata.AllowedDetails, spec.AllowedDetails)
		}
	}
	sort.Strings(registeredCodes)
	if !equalStrings(openAPICodes, registeredCodes) {
		t.Fatalf("OpenAPI error codes = %v, registered codes = %v", openAPICodes, registeredCodes)
	}
}

func loadOpenAPIContract(t *testing.T) openAPIContract {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate contract test source")
	}
	path := filepath.Join(filepath.Dir(filename), "..", "..", "api", "openapi.yaml")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read OpenAPI contract: %v", err)
	}
	var contract openAPIContract
	if err := yaml.Unmarshal(contents, &contract); err != nil {
		t.Fatalf("parse OpenAPI contract: %v", err)
	}
	return contract
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func TestRegisteredErrorMessagesAndStatusesAreSafe(t *testing.T) {
	for code, spec := range errorRegistry {
		t.Run(string(code), func(t *testing.T) {
			if spec.HTTPStatus < 400 || spec.HTTPStatus > 599 {
				t.Errorf("HTTP status %s is outside the error range", strconv.Itoa(spec.HTTPStatus))
			}
			if spec.Message == "" {
				t.Error("safe user-facing message is empty")
			}
		})
	}
}
