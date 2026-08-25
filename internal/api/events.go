package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"stackpilot/internal/domain"
	"stackpilot/internal/events"
)

const defaultEventHeartbeat = 15 * time.Second

type eventDTO struct {
	ID                int64           `json:"id"`
	Type              string          `json:"type"`
	OccurredAt        string          `json:"occurredAt"`
	WorkspaceID       string          `json:"workspaceId"`
	SystemID          string          `json:"systemId"`
	InstanceID        string          `json:"instanceId,omitempty"`
	ServiceInstanceID string          `json:"serviceInstanceId,omitempty"`
	OperationID       string          `json:"operationId,omitempty"`
	Data              json.RawMessage `json:"data"`
}

func eventsHandler(store events.Store, broker *events.Broker, heartbeat time.Duration) http.HandlerFunc {
	if heartbeat <= 0 {
		heartbeat = defaultEventHeartbeat
	}
	return func(response http.ResponseWriter, request *http.Request) {
		cursor, supplied, err := parseEventCursor(request)
		if err != nil {
			writeRegisteredError(response, request, ErrorRequestValidationFailed)
			return
		}
		flusher, ok := response.(http.Flusher)
		if !ok {
			writeRegisteredError(response, request, ErrorInternal)
			return
		}
		subscription := broker.Subscribe()
		defer subscription.Close()
		first, high, found, err := store.Bounds(request.Context())
		if err != nil {
			writeRegisteredError(response, request, ErrorInternal)
			return
		}
		cursor, err = validateEventCursor(cursor, supplied, first, high, found)
		if err != nil {
			writeEventCursorError(response, request, err)
			return
		}
		setEventStreamHeaders(response)
		response.WriteHeader(http.StatusOK)
		flusher.Flush()
		if cursor, err = writeEventRange(request.Context(), response, flusher, store, cursor, high); err != nil {
			return
		}
		streamCommittedEvents(request.Context(), response, flusher, store, subscription, cursor, heartbeat)
	}
}

func streamCommittedEvents(ctx context.Context, writer io.Writer, flusher http.Flusher, store events.Store, subscription *events.Subscription, cursor domain.EventID, heartbeat time.Duration) {
	ticker := time.NewTicker(heartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case _, open := <-subscription.Events():
			if !open {
				return
			}
			_, high, found, err := store.Bounds(ctx)
			if err != nil || !found || high <= cursor {
				continue
			}
			cursor, err = writeEventRange(ctx, writer, flusher, store, cursor, high)
			if err != nil {
				return
			}
		case <-ticker.C:
			if _, err := io.WriteString(writer, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func writeEventRange(ctx context.Context, writer io.Writer, flusher http.Flusher, store events.Store, cursor, through domain.EventID) (domain.EventID, error) {
	for cursor < through {
		page, err := store.ListRange(ctx, cursor, through, events.MaximumPageSize)
		if err != nil {
			return cursor, err
		}
		if len(page) == 0 {
			return through, nil
		}
		for _, event := range page {
			if err := writeEvent(writer, event); err != nil {
				return cursor, err
			}
			cursor = event.ID
		}
		flusher.Flush()
		if len(page) < events.MaximumPageSize {
			return through, nil
		}
	}
	return cursor, nil
}

func writeEvent(writer io.Writer, event events.Event) error {
	payload, err := json.Marshal(mapEvent(event))
	if err != nil {
		return fmt.Errorf("encode SSE event: %w", err)
	}
	_, err = fmt.Fprintf(writer, "id: %d\nevent: %s\ndata: %s\n\n", event.ID, event.Type, payload)
	return err
}

func mapEvent(event events.Event) eventDTO {
	return eventDTO{
		ID: int64(event.ID), Type: event.Type, OccurredAt: formatAPITime(event.OccurredAt),
		WorkspaceID: event.WorkspaceID.String(), SystemID: event.SystemID.String(),
		InstanceID: event.InstanceID.String(), ServiceInstanceID: event.ServiceInstanceID.String(),
		OperationID: event.OperationID.String(), Data: event.Data,
	}
}

func parseEventCursor(request *http.Request) (domain.EventID, bool, error) {
	value, supplied := request.Header.Get("Last-Event-ID"), request.Header.Get("Last-Event-ID") != ""
	if !supplied {
		values, exists := request.URL.Query()["cursor"]
		if exists {
			if len(values) != 1 || values[0] == "" {
				return 0, false, events.ErrInvalidCursor
			}
			value, supplied = values[0], true
		}
	}
	if !supplied {
		return 0, false, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < 0 {
		return 0, false, events.ErrInvalidCursor
	}
	return domain.EventID(parsed), true, nil
}

func validateEventCursor(cursor domain.EventID, supplied bool, first, last domain.EventID, found bool) (domain.EventID, error) {
	if !supplied {
		return last, nil
	}
	if !found {
		if cursor == 0 {
			return 0, nil
		}
		return 0, events.ErrInvalidCursor
	}
	if cursor > last {
		return 0, events.ErrInvalidCursor
	}
	if cursor < first-1 {
		return 0, events.ErrCursorExpired
	}
	return cursor, nil
}

func writeEventCursorError(response http.ResponseWriter, request *http.Request, err error) {
	if errors.Is(err, events.ErrCursorExpired) {
		writeRegisteredError(response, request, ErrorEventCursorExpired)
		return
	}
	writeRegisteredError(response, request, ErrorRequestValidationFailed)
}

func setEventStreamHeaders(response http.ResponseWriter) {
	response.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	response.Header().Set("Cache-Control", "no-cache, no-store")
	response.Header().Set("Connection", "keep-alive")
	response.Header().Set("X-Accel-Buffering", "no")
}
