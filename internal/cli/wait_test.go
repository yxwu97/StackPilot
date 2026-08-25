package cli

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestWaitForOperationUsesEventStreamAndReturnsTerminalState(t *testing.T) {
	var operationReads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v1/operations/op_test":
			state := "running"
			if operationReads.Add(1) > 1 {
				state = "succeeded"
			}
			response.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(response, `{"id":"op_test","state":%q,"steps":[]}`, state)
		case "/api/v1/events":
			response.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(response, "event: operation.state-changed\ndata: {\"operationId\":\"op_test\"}\n\n")
		default:
			response.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client, _ := newAPIClient(server.URL, []byte("token"))
	defer client.Close()
	operation, err := waitForOperation(context.Background(), client, "op_test", &bytes.Buffer{})
	if err != nil || operation.State != "succeeded" || operationReads.Load() != 2 {
		t.Fatalf("waitForOperation() = (%+v, %v), reads=%d", operation, err, operationReads.Load())
	}
}
