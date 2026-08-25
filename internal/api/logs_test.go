package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"stackpilot/internal/domain"
	"stackpilot/internal/logs"
	"stackpilot/internal/storage"
)

const (
	apiLogWorkspaceID       = "ws_01ARZ3NDEKTSV4RRFFQ69G5FAV"
	apiLogInstanceID        = "si_01ARZ3NDEKTSV4RRFFQ69G5FAV"
	apiLogServiceInstanceID = "svi_01ARZ3NDEKTSV4RRFFQ69G5FAV"
)

func TestLogHistoryReturnsLatestWindowAndOlderCursor(t *testing.T) {
	handler, _, cleanup := newLogAPIHarness(t, []int64{1, 2, 3})
	defer cleanup()
	response := performRequest(handler, http.MethodGet,
		"/api/v1/services/btc/backend/logs?instanceId="+apiLogInstanceID+"&limit=2")
	if response.Code != http.StatusOK {
		t.Fatalf("history status = %d, body = %q", response.Code, response.Body.String())
	}
	var page logPageDTO
	decodeResponse(t, response, &page)
	if len(page.Items) != 2 || page.Items[0].Sequence != 2 || page.Items[1].Sequence != 3 || page.NextCursor == nil || *page.NextCursor != 2 {
		t.Fatalf("latest page = %#v", page)
	}
	if strings.Contains(response.Body.String(), "serviceInstanceId") || strings.Contains(response.Body.String(), "fixture.ndjson") {
		t.Fatalf("history leaked internal metadata: %s", response.Body.String())
	}
	older := performRequest(handler, http.MethodGet,
		"/api/v1/services/btc/backend/logs?instanceId="+apiLogInstanceID+"&cursor=2&limit=2")
	decodeResponse(t, older, &page)
	if older.Code != http.StatusOK || len(page.Items) != 1 || page.Items[0].Sequence != 1 || page.NextCursor != nil {
		t.Fatalf("older page = (%d, %#v)", older.Code, page)
	}
}

func TestLogStreamReplaysHistoryThenBrokerBacklog(t *testing.T) {
	handler, broker, cleanup := newLogAPIHarness(t, []int64{1, 2, 3})
	defer cleanup()
	broker.Publish(domain.ServiceInstanceID(apiLogServiceInstanceID), apiLogEntry(4))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancel()
	request := httptest.NewRequest(http.MethodGet,
		"/api/v1/log-stream?instanceId="+apiLogInstanceID+"&serviceId=backend&afterSequence=1", nil).WithContext(ctx)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	body := response.Body.String()
	for _, sequence := range []string{"id: 2", "id: 3", "id: 4"} {
		if !strings.Contains(body, sequence) {
			t.Fatalf("stream missing %s: %q", sequence, body)
		}
	}
	if strings.Count(body, "id: 4") != 1 || !strings.Contains(body, "event: log.entry") {
		t.Fatalf("stream did not deduplicate backlog: %q", body)
	}
	assertEventStreamHeaders(t, response)
}

func TestLogHistoryMergesCurrentActiveWindow(t *testing.T) {
	handler, broker, cleanup := newLogAPIHarness(t, []int64{1, 2, 3})
	defer cleanup()
	broker.Publish(domain.ServiceInstanceID(apiLogServiceInstanceID), apiLogEntry(4))
	response := performRequest(handler, http.MethodGet,
		"/api/v1/services/btc/backend/logs?instanceId="+apiLogInstanceID+"&limit=2")
	var page logPageDTO
	decodeResponse(t, response, &page)
	if response.Code != http.StatusOK || len(page.Items) != 2 || page.Items[0].Sequence != 3 || page.Items[1].Sequence != 4 {
		t.Fatalf("active history page = (%d, %#v)", response.Code, page)
	}
}

func TestLogStreamRejectsGapOlderThanLiveRing(t *testing.T) {
	handler, broker, cleanup := newLogAPIHarness(t, []int64{1, 2, 3})
	defer cleanup()
	for sequence := int64(5); sequence <= 21; sequence++ {
		broker.Publish(domain.ServiceInstanceID(apiLogServiceInstanceID), apiLogEntry(sequence))
	}
	response := performRequest(handler, http.MethodGet,
		"/api/v1/log-stream?instanceId="+apiLogInstanceID+"&serviceId=backend&afterSequence=3")
	var envelope errorEnvelope
	decodeResponse(t, response, &envelope)
	if response.Code != http.StatusConflict || envelope.Error.Code != ErrorLogCursorExpired {
		t.Fatalf("gap response = (%d, %#v)", response.Code, envelope)
	}
}

func TestLogRoutesMapInvalidMissingAndExpiredQueries(t *testing.T) {
	handler, _, cleanup := newLogAPIHarness(t, []int64{10, 11})
	defer cleanup()
	tests := []struct {
		path   string
		status int
		code   ErrorCode
	}{
		{path: "/api/v1/log-stream?instanceId=bad&serviceId=backend", status: 404, code: ErrorResourceNotFound},
		{path: "/api/v1/log-stream?instanceId=" + apiLogInstanceID + "&serviceId=backend&afterSequence=-1", status: 400, code: ErrorRequestValidationFailed},
		{path: "/api/v1/log-stream?instanceId=" + apiLogInstanceID + "&serviceId=backend&afterSequence=1", status: 409, code: ErrorLogCursorExpired},
		{path: "/api/v1/services/other/backend/logs?instanceId=" + apiLogInstanceID, status: 404, code: ErrorResourceNotFound},
	}
	for _, test := range tests {
		response := performRequest(handler, http.MethodGet, test.path)
		var envelope errorEnvelope
		decodeResponse(t, response, &envelope)
		if response.Code != test.status || envelope.Error.Code != test.code {
			t.Fatalf("GET %s = (%d, %s)", test.path, response.Code, envelope.Error.Code)
		}
	}
}

func newLogAPIHarness(t *testing.T, sequences []int64) (http.Handler, *logs.Broker, func()) {
	t.Helper()
	dataDir := t.TempDir()
	database, err := storage.Open(context.Background(), filepath.Join(dataDir, "api-logs.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	seedAPILogRuntime(t, database)
	index, _ := storage.NewLogSegmentRepository(database)
	broker := logs.NewBroker(8, 16)
	manager, err := logs.NewManager(logs.Config{DataDir: dataDir, Index: index, Publisher: broker})
	if err != nil {
		database.Close()
		t.Fatalf("new log manager: %v", err)
	}
	registerAPILogSegment(t, dataDir, index, sequences)
	resolver, _ := storage.NewRuntimeLogScopeRepository(database)
	handler := newRouter(Config{
		LogManager: manager, LogScopes: resolver, LogBroker: broker, LogHeartbeat: time.Second,
	}, newTestSPAHandler(t))
	return handler, broker, func() { _ = database.Close() }
}

func seedAPILogRuntime(t *testing.T, database *sql.DB) {
	t.Helper()
	now := "2026-08-18T16:00:00Z"
	digest := strings.Repeat("a", 64)
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO systems(id,name,created_at,updated_at) VALUES ('btc','BTC',?,?)`, []any{now, now}},
		{`INSERT INTO manifest_snapshots(digest,system_id,api_version,normalized_yaml,parsed_json,created_at) VALUES (?,'btc','stackpilot.io/v1alpha1','{}','{}',?)`, []any{digest, now}},
		{`INSERT INTO workspaces(id,system_id,root_path,canonical_path,manifest_status,last_valid_digest,created_at,updated_at) VALUES (?,'btc','E:\\workspace','E:\\workspace','valid',?,?,?)`, []any{apiLogWorkspaceID, digest, now, now}},
		{`INSERT INTO services(workspace_id,service_id,driver,mode,required,definition_digest) VALUES (?,'backend','process','daemon',1,?)`, []any{apiLogWorkspaceID, digest}},
		{`INSERT INTO system_instances(id,workspace_id,manifest_digest,resolved_spec_digest,state,started_at) VALUES (?,?,?,?, 'running',?)`, []any{apiLogInstanceID, apiLogWorkspaceID, digest, digest, now}},
		{`INSERT INTO service_instances(id,system_instance_id,service_id,state,state_version,created_at,updated_at) VALUES (?,?,'backend','ready',1,?,?)`, []any{apiLogServiceInstanceID, apiLogInstanceID, now, now}},
	}
	for _, statement := range statements {
		if _, err := database.Exec(statement.query, statement.args...); err != nil {
			t.Fatalf("seed log runtime: %v", err)
		}
	}
}

func registerAPILogSegment(t *testing.T, dataDir string, index *storage.LogSegmentRepository, sequences []int64) {
	t.Helper()
	var contents strings.Builder
	for _, sequence := range sequences {
		payload, err := json.Marshal(apiLogEntry(sequence))
		if err != nil {
			t.Fatal(err)
		}
		contents.Write(payload)
		contents.WriteByte('\n')
	}
	path := filepath.Join(dataDir, "logs", "fixture.ndjson")
	if err := os.WriteFile(path, []byte(contents.String()), 0o600); err != nil {
		t.Fatalf("write fixture segment: %v", err)
	}
	base := time.Date(2026, 8, 18, 16, 0, 0, 0, time.UTC)
	segment := logs.Segment{
		ServiceInstanceID: domain.ServiceInstanceID(apiLogServiceInstanceID), Stream: logs.StreamStdout,
		Path: "logs/fixture.ndjson", FirstSequence: sequences[0], LastSequence: sequences[len(sequences)-1],
		FirstTimestamp: base.Add(time.Duration(sequences[0]) * time.Second),
		LastTimestamp:  base.Add(time.Duration(sequences[len(sequences)-1]) * time.Second),
		SizeBytes:      int64(contents.Len()), ClosedAt: base.Add(time.Minute),
	}
	if err := index.RegisterClosed(context.Background(), segment); err != nil {
		t.Fatalf("register log segment: %v", err)
	}
}

func apiLogEntry(sequence int64) logs.Entry {
	return logs.Entry{
		Timestamp: time.Date(2026, 8, 18, 16, 0, int(sequence), 0, time.UTC), SystemID: "btc",
		InstanceID: domain.SystemInstanceID(apiLogInstanceID), ServiceID: "backend", Stream: logs.StreamStdout,
		Level: "info", Message: "safe log line", Sequence: sequence,
	}
}
