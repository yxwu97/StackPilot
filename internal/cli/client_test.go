package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestValidateServerURLRequiresLiteralLoopbackOrigin(t *testing.T) {
	for _, value := range []string{
		"https://127.0.0.1:32100", "http://localhost:32100", "http://example.test:32100",
		"http://127.0.0.1", "http://127.0.0.1:32100/path", "http://user@127.0.0.1:32100",
	} {
		if _, err := validateServerURL(value); err == nil {
			t.Errorf("validateServerURL(%q) error = nil", value)
		}
	}
	if value, err := validateServerURL(defaultServerURL); err != nil || value != defaultServerURL {
		t.Fatalf("valid server URL = (%q, %v)", value, err)
	}
}

func TestAPIClientSendsBearerJSONAndIdempotencyKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer local-token" || request.Header.Get("Content-Type") != "application/json" || request.Header.Get("Idempotency-Key") == "" {
			t.Errorf("headers = %+v", request.Header)
		}
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusAccepted)
		_, _ = response.Write([]byte(`{"operationId":"op_test","state":"queued"}`))
	}))
	defer server.Close()
	client, err := newAPIClient(server.URL, []byte("local-token"))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	var result operationRefDTO
	if err := client.JSON(context.Background(), http.MethodPost, "/start", map[string]string{"workspaceId": "ws_test"}, &result); err != nil {
		t.Fatal(err)
	}
	if result.OperationID != "op_test" || result.State != "queued" {
		t.Fatalf("operation response = %+v", result)
	}
}

func TestAPIClientMapsSafeErrorsAndRefusesRedirects(t *testing.T) {
	redirectTargetHit := false
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirectTargetHit = true }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/redirect" {
			http.Redirect(response, request, target.URL, http.StatusFound)
			return
		}
		response.WriteHeader(http.StatusUnauthorized)
		_, _ = response.Write([]byte(`{"error":{"code":"AUTH_TOKEN_INVALID","message":"invalid","traceId":"tr_test"}}`))
	}))
	defer server.Close()
	client, _ := newAPIClient(server.URL, []byte("token"))
	defer client.Close()
	if err := client.JSON(context.Background(), http.MethodGet, "/auth", nil, nil); exitCodeFor(err) != 5 {
		t.Fatalf("authentication exit = %d, error=%v", exitCodeFor(err), err)
	}
	err := client.JSON(context.Background(), http.MethodGet, "/redirect", nil, nil)
	if err == nil || redirectTargetHit || !strings.Contains(err.Error(), "302") {
		t.Fatalf("redirect result = (hit=%t, error=%v)", redirectTargetHit, err)
	}
}

func TestReorderFlagArgsSupportsFlagsAfterTarget(t *testing.T) {
	got := reorderFlagArgs([]string{"btc", "--wait", "--output", "json"}, map[string]bool{"--wait": true})
	want := []string{"--wait", "--output", "json", "btc"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("reorderFlagArgs() = %v, want %v", got, want)
	}
}
