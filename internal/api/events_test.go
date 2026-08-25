package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"stackpilot/internal/domain"
	"stackpilot/internal/events"
)

func TestEventsHandlerReplaysHistoryAndUsesLastEventIDFirst(t *testing.T) {
	store := &memoryEventStore{items: []events.Event{apiTestEvent(1), apiTestEvent(2)}}
	broker := events.NewBroker(4)
	tests := []struct {
		name       string
		query      string
		header     string
		wantFirst  string
		wantAbsent string
	}{
		{name: "query cursor", query: "?cursor=0", wantFirst: "id: 1"},
		{name: "header precedence", query: "?cursor=0", header: "1", wantFirst: "id: 2", wantAbsent: "id: 1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
			defer cancel()
			request := httptest.NewRequest(http.MethodGet, "/api/v1/events"+test.query, nil).WithContext(ctx)
			if test.header != "" {
				request.Header.Set("Last-Event-ID", test.header)
			}
			response := httptest.NewRecorder()
			eventsHandler(store, broker, time.Second)(response, request)
			body := response.Body.String()
			unexpected := test.wantAbsent != "" && strings.Contains(body, test.wantAbsent)
			if response.Code != http.StatusOK || !strings.Contains(body, test.wantFirst) || unexpected {
				t.Fatalf("response = (%d, %q)", response.Code, body)
			}
			if !strings.Contains(body, "event: operation.state.changed") || !strings.Contains(body, `"data":{"version":1}`) {
				t.Fatalf("invalid SSE envelope: %q", body)
			}
			assertEventStreamHeaders(t, response)
		})
	}
}

func TestEventsHandlerSubscribesBeforeReadingHighWaterMark(t *testing.T) {
	store := &memoryEventStore{items: []events.Event{apiTestEvent(1)}}
	broker := events.NewBroker(4)
	store.onFirstBounds = func() {
		store.append(apiTestEvent(2))
		broker.Notify(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancel()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/events?cursor=0", nil).WithContext(ctx)
	response := httptest.NewRecorder()
	eventsHandler(store, broker, time.Second)(response, request)
	if body := response.Body.String(); !strings.Contains(body, "id: 1") || !strings.Contains(body, "id: 2") {
		t.Fatalf("watermark replay body = %q", body)
	}
}

func TestEventsHandlerHeartbeatsAndDoesNotReplayWithoutCursor(t *testing.T) {
	store := &memoryEventStore{items: []events.Event{apiTestEvent(1)}}
	ctx, cancel := context.WithTimeout(context.Background(), 18*time.Millisecond)
	defer cancel()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/events", nil).WithContext(ctx)
	response := httptest.NewRecorder()
	eventsHandler(store, events.NewBroker(2), 5*time.Millisecond)(response, request)
	body := response.Body.String()
	if strings.Contains(body, "id: 1") || !strings.Contains(body, ": heartbeat\n\n") {
		t.Fatalf("initial stream body = %q", body)
	}
}

func TestEventsHandlerRejectsInvalidAndExpiredCursorsBeforeStreaming(t *testing.T) {
	store := &memoryEventStore{items: []events.Event{apiTestEvent(3)}}
	handler := traceMiddleware(eventsHandler(store, events.NewBroker(2), time.Second))
	tests := []struct {
		cursor string
		status int
		code   ErrorCode
	}{
		{cursor: "bad", status: http.StatusBadRequest, code: ErrorRequestValidationFailed},
		{cursor: "0", status: http.StatusConflict, code: ErrorEventCursorExpired},
		{cursor: "4", status: http.StatusBadRequest, code: ErrorRequestValidationFailed},
	}
	for _, test := range tests {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/api/v1/events?cursor="+test.cursor, nil)
		handler.ServeHTTP(response, request)
		var envelope errorEnvelope
		decodeResponse(t, response, &envelope)
		if response.Code != test.status || envelope.Error.Code != test.code {
			t.Fatalf("cursor %q = (%d, %s)", test.cursor, response.Code, envelope.Error.Code)
		}
		assertJSONHeaders(t, response)
	}
}

func assertEventStreamHeaders(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	if response.Header().Get("Content-Type") != "text/event-stream; charset=utf-8" ||
		response.Header().Get("X-Accel-Buffering") != "no" || response.Header().Get("Cache-Control") != "no-cache, no-store" {
		t.Fatalf("SSE headers = %#v", response.Header())
	}
}

func apiTestEvent(id domain.EventID) events.Event {
	return events.Event{
		ID: id, Type: events.TypeOperationStateChanged,
		OccurredAt:  time.Date(2026, 8, 18, 16, 0, int(id), 0, time.UTC),
		WorkspaceID: domain.WorkspaceID("ws_01ARZ3NDEKTSV4RRFFQ69G5FAV"), SystemID: domain.SystemID("btc"),
		OperationID: domain.OperationID("op_01ARZ3NDEKTSV4RRFFQ69G5FAV"), Data: json.RawMessage(`{"version":1}`),
	}
}

type memoryEventStore struct {
	mutex         sync.Mutex
	items         []events.Event
	onFirstBounds func()
	once          sync.Once
}

func (store *memoryEventStore) Bounds(context.Context) (domain.EventID, domain.EventID, bool, error) {
	store.once.Do(func() {
		if store.onFirstBounds != nil {
			store.onFirstBounds()
		}
	})
	store.mutex.Lock()
	defer store.mutex.Unlock()
	if len(store.items) == 0 {
		return 0, 0, false, nil
	}
	return store.items[0].ID, store.items[len(store.items)-1].ID, true, nil
}

func (store *memoryEventStore) ListRange(_ context.Context, after, through domain.EventID, limit int) ([]events.Event, error) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	result := make([]events.Event, 0, limit)
	for _, event := range store.items {
		if event.ID > after && event.ID <= through && len(result) < limit {
			result = append(result, event)
		}
	}
	return result, nil
}

func (store *memoryEventStore) append(event events.Event) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	store.items = append(store.items, event)
}
