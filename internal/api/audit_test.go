package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"stackpilot/internal/security"
)

func TestAuditMiddlewareRecordsSuccessfulAndDeniedBrowserMutations(t *testing.T) {
	auth := newStubAuthenticator()
	audit := &memoryAuditStore{}
	handler := newWorkspaceAPIHandlerWithSecurity(t, auth, audit)
	root := createAPIWorkspaceFixture(t, validAPIManifest())

	accepted := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/v1/workspaces", bytes.NewReader(mustJSON(t, map[string]string{"path": root})))
	accepted.Header.Set("Content-Type", "application/json")
	accepted.Header.Set("Origin", "http://127.0.0.1")
	accepted.Header.Set(browserCSRFHeader, auth.csrf)
	accepted.AddCookie(&http.Cookie{Name: sessionCookieName, Value: auth.session})
	acceptedResponse := httptest.NewRecorder()
	handler.ServeHTTP(acceptedResponse, accepted)
	if acceptedResponse.Code != http.StatusCreated {
		t.Fatalf("accepted mutation = (%d, %q)", acceptedResponse.Code, acceptedResponse.Body.String())
	}

	denied := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/v1/workspaces", strings.NewReader(`{"path":"sensitive"}`))
	denied.Header.Set("Content-Type", "application/json")
	denied.Header.Set("Origin", "http://127.0.0.1")
	denied.AddCookie(&http.Cookie{Name: sessionCookieName, Value: auth.session})
	deniedResponse := httptest.NewRecorder()
	handler.ServeHTTP(deniedResponse, denied)
	assertErrorCode(t, deniedResponse, http.StatusForbidden, ErrorBrowserRequestRejected)

	if len(audit.events) != 2 || audit.events[0].Result != "succeeded" || audit.events[0].SubjectType != "browser_session" ||
		audit.events[0].TargetID == "" || audit.events[1].Result != "denied" || audit.events[1].ErrorCode != string(ErrorBrowserRequestRejected) {
		t.Fatalf("audit events = %+v", audit.events)
	}
	for _, event := range audit.events {
		if strings.Contains(event.TargetID, root) || strings.Contains(event.TargetID, "sensitive") {
			t.Fatalf("audit event leaked request input: %+v", event)
		}
	}

	query := httptest.NewRequest(http.MethodGet, "/api/v1/audit-events", nil)
	query.AddCookie(&http.Cookie{Name: sessionCookieName, Value: auth.session})
	queryResponse := httptest.NewRecorder()
	handler.ServeHTTP(queryResponse, query)
	if queryResponse.Code != http.StatusOK || !strings.Contains(queryResponse.Body.String(), `"action":"workspace.register"`) || strings.Contains(queryResponse.Body.String(), root) {
		t.Fatalf("audit query = (%d, %q)", queryResponse.Code, queryResponse.Body.String())
	}
}

type memoryAuditStore struct {
	events []security.AuditEvent
}

func (store *memoryAuditStore) AppendAudit(_ context.Context, event security.AuditEvent) (security.AuditEvent, error) {
	event.ID = int64(len(store.events) + 1)
	store.events = append(store.events, event)
	return event, nil
}

func (store *memoryAuditStore) ListAudit(_ context.Context, after int64, limit int) ([]security.AuditEvent, error) {
	result := make([]security.AuditEvent, 0, limit)
	for _, event := range store.events {
		if event.ID > after && len(result) < limit {
			result = append(result, event)
		}
	}
	return result, nil
}

var _ security.AuditStore = (*memoryAuditStore)(nil)
