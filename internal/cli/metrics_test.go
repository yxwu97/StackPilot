package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestGetMetricsUsesAuthenticatedBoundedWorkspaceQuery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer token" {
			t.Errorf("Authorization header was not set")
		}
		if request.URL.Path != "/api/v1/workspaces/ws_test/metrics" || request.URL.Query().Get("granularity") != "detail" || request.URL.Query().Get("serviceId") != "backend" {
			t.Errorf("metric request = %s", request.URL.String())
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"from":"2026-08-31T10:00:00Z","to":"2026-08-31T11:00:00Z","granularity":"detail","series":[{"serviceId":"backend","source":"process-job","points":[{"observedAt":"2026-08-31T10:30:00Z","status":"available","cpuPercent":2.5}]}]}`))
	}))
	defer server.Close()
	client, err := newAPIClient(server.URL, []byte("token"))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	start := time.Date(2026, 8, 31, 10, 0, 0, 0, time.UTC)
	result, err := getMetrics(context.Background(), client, workspaceDTO{ID: "ws_test"}, start, start.Add(time.Hour), "detail", "backend")
	if err != nil || len(result.Series) != 1 || len(result.Series[0].Points) != 1 {
		t.Fatalf("getMetrics() = (%#v, %v)", result, err)
	}
}

func TestMetricTimesRequireUTCIncreasingWindow(t *testing.T) {
	start, end, err := metricTimes("2026-08-31T10:00:00Z", "2026-08-31T11:00:00Z")
	if err != nil || end.Sub(start) != time.Hour || start.Location() != time.UTC {
		t.Fatalf("metricTimes(valid) = (%v, %v, %v)", start, end, err)
	}
	for _, values := range [][2]string{{"2026-08-31T10:00:00+08:00", "2026-08-31T11:00:00+08:00"}, {"2026-08-31T12:00:00Z", "2026-08-31T11:00:00Z"}} {
		if _, _, err := metricTimes(values[0], values[1]); err == nil {
			t.Fatalf("metricTimes(%q, %q) error = nil", values[0], values[1])
		}
	}
}
