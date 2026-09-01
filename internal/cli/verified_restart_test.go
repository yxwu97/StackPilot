package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestVerifiedRestartClientSendsOnlyPlanAndWorkspaceIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/workspaces/ws_test/verified-restart" || request.Method != http.MethodPost {
			t.Errorf("verified restart request = %s %s", request.Method, request.URL.Path)
		}
		var body map[string]string
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body) != 1 || body["changePlanId"] != "plan_test" {
			t.Errorf("verified restart body = %#v", body)
		}
		_, _ = response.Write([]byte(`{"operationId":"op_test","state":"queued"}`))
	}))
	defer server.Close()
	client, err := newAPIClient(server.URL, []byte("token"))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	operation, err := submitVerifiedRestart(context.Background(), client, workspaceDTO{ID: "ws_test"}, "plan_test")
	if err != nil || operation.OperationID != "op_test" {
		t.Fatalf("submitVerifiedRestart() = (%#v, %v)", operation, err)
	}
}
