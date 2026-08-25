package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"stackpilot/internal/security"
)

func TestSecretAPINeverReturnsValue(t *testing.T) {
	provider := newStubSecretProvider()
	handler := newRouter(Config{Secrets: provider}, newTestSPAHandler(t))
	plaintext := []byte("api-plaintext-must-not-return")
	created := performJSONRequest(handler, http.MethodPut, "/api/v1/secrets/aiws/database-password", map[string][]byte{"value": plaintext})
	if created.Code != http.StatusOK || strings.Contains(created.Body.String(), string(plaintext)) || !strings.Contains(created.Body.String(), `"version":1`) {
		t.Fatalf("PUT Secret = (%d, %q)", created.Code, created.Body.String())
	}
	metadata := performRequest(handler, http.MethodGet, "/api/v1/secrets/aiws/database-password")
	if metadata.Code != http.StatusOK || strings.Contains(metadata.Body.String(), string(plaintext)) || !strings.Contains(metadata.Body.String(), `"provider":"dpapi-file"`) {
		t.Fatalf("GET Secret metadata = (%d, %q)", metadata.Code, metadata.Body.String())
	}
	deleted := performRequest(handler, http.MethodDelete, "/api/v1/secrets/aiws/database-password")
	if deleted.Code != http.StatusOK || strings.Contains(deleted.Body.String(), string(plaintext)) {
		t.Fatalf("DELETE Secret = (%d, %q)", deleted.Code, deleted.Body.String())
	}
	missing := performRequest(handler, http.MethodGet, "/api/v1/secrets/aiws/database-password")
	assertErrorCode(t, missing, http.StatusNotFound, ErrorSecretNotFound)
}

func TestSecretAPIBrowserMutationRequiresCSRFAndAuditsMetadataOnly(t *testing.T) {
	auth := newStubAuthenticator()
	audit := &memoryAuditStore{}
	provider := newStubSecretProvider()
	handler := newRouter(Config{Secrets: provider, Auth: auth, Audit: audit}, newTestSPAHandler(t))
	body := mustJSON(t, map[string][]byte{"value": []byte("browser-secret-never-audited")})
	denied := httptest.NewRequest(http.MethodPut, "http://127.0.0.1/api/v1/secrets/aiws/api-key", bytes.NewReader(body))
	denied.Header.Set("Content-Type", "application/json")
	denied.Header.Set("Origin", "http://127.0.0.1")
	denied.AddCookie(&http.Cookie{Name: sessionCookieName, Value: auth.session})
	deniedResponse := httptest.NewRecorder()
	handler.ServeHTTP(deniedResponse, denied)
	assertErrorCode(t, deniedResponse, http.StatusForbidden, ErrorBrowserRequestRejected)

	accepted := httptest.NewRequest(http.MethodPut, "http://127.0.0.1/api/v1/secrets/aiws/api-key", bytes.NewReader(body))
	accepted.Header.Set("Content-Type", "application/json")
	accepted.Header.Set("Origin", "http://127.0.0.1")
	accepted.Header.Set(browserCSRFHeader, auth.csrf)
	accepted.AddCookie(&http.Cookie{Name: sessionCookieName, Value: auth.session})
	acceptedResponse := httptest.NewRecorder()
	handler.ServeHTTP(acceptedResponse, accepted)
	if acceptedResponse.Code != http.StatusOK {
		t.Fatalf("accepted Secret mutation = (%d, %q)", acceptedResponse.Code, acceptedResponse.Body.String())
	}
	if len(audit.events) != 2 || audit.events[0].Result != "denied" || audit.events[1].TargetID != "aiws/api-key" {
		t.Fatalf("Secret audit events = %+v", audit.events)
	}
	for _, event := range audit.events {
		if strings.Contains(event.TargetID, "browser-secret") {
			t.Fatalf("Secret audit leaked value: %+v", event)
		}
	}
}

func TestSecretAPIRejectsMalformedInput(t *testing.T) {
	handler := newRouter(Config{Secrets: newStubSecretProvider()}, newTestSPAHandler(t))
	invalidKey := performJSONRequest(handler, http.MethodPut, "/api/v1/secrets/AIWS/key", map[string][]byte{"value": []byte("value")})
	assertErrorCode(t, invalidKey, http.StatusBadRequest, ErrorRequestValidationFailed)
	unknown := performJSONRequest(handler, http.MethodPut, "/api/v1/secrets/aiws/key", map[string]string{"plaintext": "unsafe"})
	assertErrorCode(t, unknown, http.StatusBadRequest, ErrorRequestValidationFailed)
}

type stubSecretProvider struct {
	metadata map[security.SecretKey]security.SecretMetadata
	values   map[security.SecretKey][]byte
}

func newStubSecretProvider() *stubSecretProvider {
	return &stubSecretProvider{metadata: make(map[security.SecretKey]security.SecretMetadata), values: make(map[security.SecretKey][]byte)}
}

func (provider *stubSecretProvider) Set(_ context.Context, key security.SecretKey, value []byte) (security.SecretMetadata, error) {
	version := provider.metadata[key].Version + 1
	metadata := security.SecretMetadata{Key: key, Provider: security.SecretProviderDPAPIFile, Version: version,
		UpdatedAt: time.Date(2026, 8, 18, 4, 0, int(version), 0, time.UTC)}
	provider.metadata[key] = metadata
	provider.values[key] = append([]byte(nil), value...)
	return metadata, nil
}

func (provider *stubSecretProvider) Resolve(_ context.Context, key security.SecretKey) (security.ResolvedSecret, error) {
	value, found := provider.values[key]
	if !found {
		return security.ResolvedSecret{}, security.ErrSecretNotFound
	}
	return security.ResolvedSecret{Metadata: provider.metadata[key], Value: append([]byte(nil), value...)}, nil
}

func (provider *stubSecretProvider) Metadata(_ context.Context, key security.SecretKey) (security.SecretMetadata, bool, error) {
	metadata, found := provider.metadata[key]
	return metadata, found, nil
}

func (provider *stubSecretProvider) Delete(_ context.Context, key security.SecretKey) error {
	if _, found := provider.metadata[key]; !found {
		return security.ErrSecretNotFound
	}
	delete(provider.metadata, key)
	delete(provider.values, key)
	return nil
}

var _ security.SecretProvider = (*stubSecretProvider)(nil)
