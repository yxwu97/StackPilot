package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestChangePlanClientUsesOnlyRegisteredWorkspaceAndPersistentReference(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer token" {
			t.Errorf("Authorization header was not set")
		}
		switch request.URL.Path {
		case "/api/v1/workspaces/ws_test/change-plans":
			if request.Method != http.MethodPost || request.ContentLength > 0 {
				t.Errorf("change plan request = %s contentLength=%d", request.Method, request.ContentLength)
			}
			_, _ = response.Write([]byte(`{"operationId":"op_test","state":"queued"}`))
		case "/api/v1/change-plans/plan_test":
			if request.Method != http.MethodGet {
				t.Errorf("get plan method = %s", request.Method)
			}
			_, _ = response.Write([]byte(`{"id":"plan_test","workspaceId":"ws_test","systemId":"sample","createdByOperationId":"op_test","fromRevision":{},"toRevision":{},"ruleVersion":"change-risk/v1","state":"ready","risk":"high","itemCount":1,"blockedCount":0,"items":[{"kind":"manifest","change":"changed","risk":"high","key":"workspace.git","summary":"Git identity unavailable."}],"createdAt":"2026-08-31T12:00:00Z"}`))
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	client, err := newAPIClient(server.URL, []byte("token"))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	operation, err := submitChangePlan(context.Background(), client, workspaceDTO{ID: "ws_test"})
	if err != nil || operation.OperationID != "op_test" {
		t.Fatalf("submitChangePlan() = (%#v, %v)", operation, err)
	}
	plan, err := getChangePlan(context.Background(), client, "plan_test")
	if err != nil || plan.ID != "plan_test" || len(plan.Items) != 1 {
		t.Fatalf("getChangePlan() = (%#v, %v)", plan, err)
	}
}

func TestPersistedChangePlanIDRequiresSucceededPersistStep(t *testing.T) {
	valid := operationDTO{State: "succeeded", Steps: []operationStepDTO{{Key: "persist-plan", State: "succeeded", DetailRef: "plan_test"}}}
	if id, err := persistedChangePlanID(valid); err != nil || id != "plan_test" {
		t.Fatalf("persistedChangePlanID(valid) = (%q, %v)", id, err)
	}
	for _, operation := range []operationDTO{
		{State: "failed"},
		{State: "succeeded", Steps: []operationStepDTO{{Key: "persist-plan", State: "failed"}}},
	} {
		if _, err := persistedChangePlanID(operation); err == nil {
			t.Fatalf("persistedChangePlanID(%#v) error = nil", operation)
		}
	}
}
