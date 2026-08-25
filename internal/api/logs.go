package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"stackpilot/internal/domain"
	"stackpilot/internal/logs"
)

const defaultLogHeartbeat = 15 * time.Second

type logEntryDTO struct {
	Timestamp   string `json:"timestamp"`
	SystemID    string `json:"systemId"`
	InstanceID  string `json:"instanceId"`
	ServiceID   string `json:"serviceId"`
	Stream      string `json:"stream"`
	Level       string `json:"level"`
	Message     string `json:"message"`
	OperationID string `json:"operationId,omitempty"`
	Sequence    int64  `json:"sequence"`
	Truncated   bool   `json:"truncated"`
}

type logPageDTO struct {
	Items      []logEntryDTO `json:"items"`
	NextCursor *int64        `json:"nextCursor"`
}

func registerLogRoutes(router chi.Router, manager *logs.Manager, resolver logs.ScopeResolver, broker *logs.Broker, heartbeat time.Duration) {
	router.Get("/services/{systemID}/{serviceID}/logs", logHistoryHandler(manager, resolver, broker))
	router.Get("/log-stream", logStreamHandler(manager, resolver, broker, heartbeat))
}

func logHistoryHandler(manager *logs.Manager, resolver logs.ScopeResolver, broker *logs.Broker) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		resolved, err := resolveHistoryLogScope(request, resolver)
		if err != nil {
			writeLogBoundaryError(response, request, err)
			return
		}
		query, err := parseLogWindowQuery(request, resolved.Scope)
		if err != nil {
			writeRegisteredError(response, request, ErrorRequestValidationFailed)
			return
		}
		window, err := manager.QueryWindow(request.Context(), query)
		if err != nil {
			writeLogBoundaryError(response, request, err)
			return
		}
		window = mergeLogWindow(window, broker.Snapshot(resolved.Scope.ServiceInstanceID), query)
		items := make([]logEntryDTO, 0, len(window.Entries))
		for _, entry := range window.Entries {
			items = append(items, mapLogEntry(entry))
		}
		var next *int64
		if window.HasMore {
			value := window.NextCursor
			next = &value
		}
		writeJSON(response, http.StatusOK, logPageDTO{Items: items, NextCursor: next})
	}
}

func logStreamHandler(manager *logs.Manager, resolver logs.ScopeResolver, broker *logs.Broker, heartbeat time.Duration) http.HandlerFunc {
	if heartbeat <= 0 {
		heartbeat = defaultLogHeartbeat
	}
	return func(response http.ResponseWriter, request *http.Request) {
		resolved, after, err := resolveStreamLogScope(request, resolver)
		if err != nil {
			writeLogBoundaryError(response, request, err)
			return
		}
		flusher, ok := response.(http.Flusher)
		if !ok {
			writeRegisteredError(response, request, ErrorInternal)
			return
		}
		subscription := broker.Subscribe(resolved.Scope.ServiceInstanceID, after)
		defer subscription.Close()
		if _, err := manager.Query(request.Context(), logs.Query{Scope: resolved.Scope, AfterSequence: after, Limit: 1}); err != nil {
			writeLogBoundaryError(response, request, err)
			return
		}
		if logBacklogHasGap(request.Context(), manager, resolved.Scope.ServiceInstanceID, after, subscription.Backlog()) {
			writeRegisteredError(response, request, ErrorLogCursorExpired)
			return
		}
		setLogStreamHeaders(response)
		response.WriteHeader(http.StatusOK)
		flusher.Flush()
		cursor, err := writeLogHistory(request.Context(), response, flusher, manager, resolved.Scope, after)
		if err != nil {
			return
		}
		cursor, err = writeLogEntries(response, flusher, subscription.Backlog(), cursor)
		if err != nil {
			return
		}
		streamLogEntries(request.Context(), response, flusher, subscription, cursor, heartbeat)
	}
}

func mergeLogWindow(window logs.Window, live []logs.Entry, query logs.WindowQuery) logs.Window {
	limit := query.Limit
	if limit == 0 {
		limit = 500
	}
	bySequence := make(map[int64]logs.Entry, len(window.Entries)+len(live))
	for _, entry := range window.Entries {
		bySequence[entry.Sequence] = entry
	}
	for _, entry := range live {
		if matchesLogWindowQuery(entry, query) {
			bySequence[entry.Sequence] = entry
		}
	}
	entries := make([]logs.Entry, 0, len(bySequence))
	for _, entry := range bySequence {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(left, right int) bool { return entries[left].Sequence < entries[right].Sequence })
	hasMore := window.HasMore || len(entries) > limit
	if len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}
	next := int64(0)
	if hasMore && len(entries) > 0 {
		next = entries[0].Sequence
	}
	return logs.Window{Entries: entries, HasMore: hasMore, NextCursor: next}
}

func matchesLogWindowQuery(entry logs.Entry, query logs.WindowQuery) bool {
	if query.Cursor > 0 && entry.Sequence >= query.Cursor {
		return false
	}
	if query.Level != "" && !strings.EqualFold(entry.Level, query.Level) {
		return false
	}
	if query.From != nil && entry.Timestamp.Before(query.From.UTC()) {
		return false
	}
	return query.To == nil || !entry.Timestamp.After(query.To.UTC())
}

func logBacklogHasGap(ctx context.Context, manager *logs.Manager, id domain.ServiceInstanceID, after int64, backlog []logs.Entry) bool {
	if after == 0 || len(backlog) == 0 || after >= backlog[0].Sequence-1 {
		return false
	}
	_, closedLast, found, err := manager.SequenceBounds(ctx, id)
	return err != nil || !found || closedLast < backlog[0].Sequence-1
}

func resolveHistoryLogScope(request *http.Request, resolver logs.ScopeResolver) (logs.ResolvedScope, error) {
	systemID, err := domain.ParseSystemID(chi.URLParam(request, "systemID"))
	if err != nil {
		return logs.ResolvedScope{}, logs.ErrScopeNotFound
	}
	serviceID, instanceID, err := parseLogScopeQuery(request, chi.URLParam(request, "serviceID"))
	if err != nil {
		return logs.ResolvedScope{}, err
	}
	resolved, err := resolver.Resolve(request.Context(), instanceID, serviceID)
	if err != nil || resolved.Scope.SystemID != systemID {
		return logs.ResolvedScope{}, logs.ErrScopeNotFound
	}
	if workspace := request.URL.Query().Get("workspaceId"); workspace != "" && workspace != resolved.WorkspaceID.String() {
		return logs.ResolvedScope{}, logs.ErrScopeNotFound
	}
	return resolved, nil
}

func resolveStreamLogScope(request *http.Request, resolver logs.ScopeResolver) (logs.ResolvedScope, int64, error) {
	serviceValue, _, err := singleQueryValue(request, "serviceId")
	if err != nil {
		return logs.ResolvedScope{}, 0, err
	}
	serviceID, instanceID, err := parseLogScopeQuery(request, serviceValue)
	if err != nil {
		return logs.ResolvedScope{}, 0, err
	}
	afterValue, supplied, err := singleQueryValue(request, "afterSequence")
	if err != nil {
		return logs.ResolvedScope{}, 0, err
	}
	after, err := parseOptionalInteger(afterValue, supplied, 0, true)
	if err != nil {
		return logs.ResolvedScope{}, 0, err
	}
	resolved, err := resolver.Resolve(request.Context(), instanceID, serviceID)
	return resolved, after, err
}

func parseLogScopeQuery(request *http.Request, serviceValue string) (domain.ServiceID, domain.SystemInstanceID, error) {
	serviceID, err := domain.ParseServiceID(serviceValue)
	if err != nil {
		return "", "", logs.ErrScopeNotFound
	}
	instanceValue, _, err := singleQueryValue(request, "instanceId")
	if err != nil {
		return "", "", logs.ErrScopeNotFound
	}
	instanceID, err := domain.ParseSystemInstanceID(instanceValue)
	if err != nil {
		return "", "", logs.ErrScopeNotFound
	}
	return serviceID, instanceID, nil
}

func parseLogWindowQuery(request *http.Request, scope logs.Scope) (logs.WindowQuery, error) {
	cursorValue, cursorSet, err := singleQueryValue(request, "cursor")
	if err != nil {
		return logs.WindowQuery{}, err
	}
	cursor, err := parseOptionalInteger(cursorValue, cursorSet, 0, false)
	if err != nil {
		return logs.WindowQuery{}, err
	}
	limitValue, limitSet, err := singleQueryValue(request, "limit")
	if err != nil {
		return logs.WindowQuery{}, err
	}
	limit64, err := parseOptionalInteger(limitValue, limitSet, 0, false)
	if err != nil || limit64 > 5000 {
		return logs.WindowQuery{}, logs.ErrInvalidScope
	}
	fromValue, _, err := singleQueryValue(request, "from")
	if err != nil {
		return logs.WindowQuery{}, err
	}
	from, err := parseOptionalAPITime(fromValue)
	if err != nil {
		return logs.WindowQuery{}, err
	}
	toValue, _, err := singleQueryValue(request, "to")
	if err != nil {
		return logs.WindowQuery{}, err
	}
	to, err := parseOptionalAPITime(toValue)
	if err != nil {
		return logs.WindowQuery{}, err
	}
	level, _, err := singleQueryValue(request, "level")
	if err != nil {
		return logs.WindowQuery{}, err
	}
	return logs.WindowQuery{
		Scope: scope, Cursor: cursor, Limit: int(limit64), Level: level, From: from, To: to,
	}, nil
}

func writeLogHistory(ctx context.Context, writer io.Writer, flusher http.Flusher, manager *logs.Manager, scope logs.Scope, cursor int64) (int64, error) {
	for {
		page, err := manager.Query(ctx, logs.Query{Scope: scope, AfterSequence: cursor, Limit: 500})
		if err != nil {
			return cursor, err
		}
		cursor, err = writeLogEntries(writer, flusher, page.Entries, cursor)
		if err != nil || len(page.Entries) < 500 {
			return cursor, err
		}
	}
}

func writeLogEntries(writer io.Writer, flusher http.Flusher, entries []logs.Entry, cursor int64) (int64, error) {
	sort.Slice(entries, func(left, right int) bool { return entries[left].Sequence < entries[right].Sequence })
	for _, entry := range entries {
		if entry.Sequence <= cursor {
			continue
		}
		if err := writeLogEntry(writer, entry); err != nil {
			return cursor, err
		}
		cursor = entry.Sequence
	}
	flusher.Flush()
	return cursor, nil
}

func streamLogEntries(ctx context.Context, writer io.Writer, flusher http.Flusher, subscription *logs.Subscription, cursor int64, heartbeat time.Duration) {
	ticker := time.NewTicker(heartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case entry, open := <-subscription.Entries():
			if !open {
				return
			}
			if entry.Sequence <= cursor {
				continue
			}
			if writeLogEntry(writer, entry) != nil {
				return
			}
			cursor = entry.Sequence
			flusher.Flush()
		case <-ticker.C:
			if _, err := io.WriteString(writer, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func writeLogEntry(writer io.Writer, entry logs.Entry) error {
	payload, err := json.Marshal(mapLogEntry(entry))
	if err != nil {
		return fmt.Errorf("encode log SSE entry: %w", err)
	}
	_, err = fmt.Fprintf(writer, "id: %d\nevent: log.entry\ndata: %s\n\n", entry.Sequence, payload)
	return err
}

func mapLogEntry(entry logs.Entry) logEntryDTO {
	return logEntryDTO{
		Timestamp: formatAPITime(entry.Timestamp), SystemID: entry.SystemID.String(), InstanceID: entry.InstanceID.String(),
		ServiceID: entry.ServiceID.String(), Stream: string(entry.Stream), Level: entry.Level, Message: entry.Message,
		OperationID: entry.OperationID.String(), Sequence: entry.Sequence, Truncated: entry.Truncated,
	}
}

func parseOptionalInteger(value string, supplied bool, fallback int64, allowZero bool) (int64, error) {
	if !supplied {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < 0 || (!allowZero && parsed == 0) {
		return 0, logs.ErrInvalidScope
	}
	return parsed, nil
}

func singleQueryValue(request *http.Request, key string) (string, bool, error) {
	values, supplied := request.URL.Query()[key]
	if !supplied {
		return "", false, nil
	}
	if len(values) != 1 || values[0] == "" {
		return "", false, logs.ErrInvalidScope
	}
	return values[0], true, nil
}

func parseOptionalAPITime(value string) (*time.Time, error) {
	if value == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil, logs.ErrInvalidScope
	}
	parsed = parsed.UTC()
	return &parsed, nil
}

func writeLogBoundaryError(response http.ResponseWriter, request *http.Request, err error) {
	switch {
	case errors.Is(err, logs.ErrCursorExpired):
		writeRegisteredError(response, request, ErrorLogCursorExpired)
	case errors.Is(err, logs.ErrScopeNotFound):
		writeRegisteredError(response, request, ErrorResourceNotFound)
	case errors.Is(err, logs.ErrInvalidScope):
		writeRegisteredError(response, request, ErrorRequestValidationFailed)
	default:
		writeRegisteredError(response, request, ErrorInternal)
	}
}

func setLogStreamHeaders(response http.ResponseWriter) {
	response.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	response.Header().Set("Cache-Control", "no-cache, no-store")
	response.Header().Set("Connection", "keep-alive")
	response.Header().Set("X-Accel-Buffering", "no")
}
