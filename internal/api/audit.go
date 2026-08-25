package api

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"stackpilot/internal/security"
)

const maximumAuditCaptureBytes = 64 << 10

type auditResponseWriter struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

type auditListDTO struct {
	Items      []auditDTO `json:"items"`
	NextCursor *int64     `json:"nextCursor"`
}

type auditDTO struct {
	ID          int64  `json:"id"`
	SubjectType string `json:"subjectType"`
	Action      string `json:"action"`
	TargetType  string `json:"targetType"`
	TargetID    string `json:"targetId,omitempty"`
	Result      string `json:"result"`
	TraceID     string `json:"traceId"`
	OperationID string `json:"operationId,omitempty"`
	ClientType  string `json:"clientType"`
	ErrorCode   string `json:"errorCode,omitempty"`
	OccurredAt  string `json:"occurredAt"`
}

func (writer *auditResponseWriter) WriteHeader(status int) {
	writer.status = status
	writer.ResponseWriter.WriteHeader(status)
}

func (writer *auditResponseWriter) Write(value []byte) (int, error) {
	if writer.status == 0 {
		writer.status = http.StatusOK
	}
	remaining := maximumAuditCaptureBytes - writer.body.Len()
	if remaining > 0 {
		_, _ = writer.body.Write(value[:min(len(value), remaining)])
	}
	return writer.ResponseWriter.Write(value)
}

func auditMutation(store security.AuditStore, logger *slog.Logger, action, targetType, targetParam string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if store == nil {
			return next
		}
		return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			capture := &auditResponseWriter{ResponseWriter: response}
			next.ServeHTTP(capture, request)
			event := buildAuditEvent(request, capture, action, targetType, targetParam)
			if _, err := store.AppendAudit(request.Context(), event); err != nil && logger != nil {
				logger.Error("audit event persistence failed", "trace_id", event.TraceID, "operation_id", event.OperationID,
					"error_code", "AUDIT_WRITE_FAILED", "error", err)
			}
		})
	}
}

func buildAuditEvent(request *http.Request, capture *auditResponseWriter, action, targetType, targetParam string) security.AuditEvent {
	auth, _ := request.Context().Value(authenticationContextKey{}).(authentication)
	metadata := capturedResponseMetadata(capture.body.Bytes())
	targetID := chi.URLParam(request, targetParam)
	if targetID == "" {
		targetID = metadata.ID
	}
	result := "succeeded"
	if capture.status == http.StatusAccepted {
		result = "accepted"
	} else if capture.status == http.StatusUnauthorized || capture.status == http.StatusForbidden {
		result = "denied"
	} else if capture.status >= http.StatusBadRequest {
		result = "failed"
	}
	subject, client := auditCaller(auth)
	return security.AuditEvent{SubjectType: subject, ClientType: client, Action: action, TargetType: targetType,
		TargetID: targetID, Result: result, TraceID: traceIDFromContext(request.Context()), OperationID: metadata.OperationID,
		ErrorCode: metadata.ErrorCode, OccurredAt: time.Now().UTC()}
}

type responseMetadata struct {
	ID          string
	OperationID string
	ErrorCode   string
}

func capturedResponseMetadata(value []byte) responseMetadata {
	var payload struct {
		ID          string `json:"id"`
		OperationID string `json:"operationId"`
		Error       struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(value, &payload) != nil {
		return responseMetadata{}
	}
	return responseMetadata{ID: payload.ID, OperationID: payload.OperationID, ErrorCode: payload.Error.Code}
}

func auditCaller(auth authentication) (string, string) {
	if auth.kind == authenticationSession {
		return "browser_session", "web"
	}
	if auth.kind == authenticationBearer {
		return "local_token", "cli"
	}
	return "system", "internal"
}

func registerAuditRoutes(router chi.Router, store security.AuditStore) {
	router.Get("/audit-events", listAuditHandler(store))
}

func listAuditHandler(store security.AuditStore) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		after, limit, ok := parseAuditQuery(request)
		if !ok {
			writeRegisteredError(response, request, ErrorRequestValidationFailed)
			return
		}
		events, err := store.ListAudit(request.Context(), after, limit)
		if err != nil {
			writeBoundaryError(response, request, err)
			return
		}
		items := make([]auditDTO, 0, len(events))
		for _, event := range events {
			items = append(items, mapAudit(event))
		}
		var next *int64
		if len(events) == limit {
			value := events[len(events)-1].ID
			next = &value
		}
		writeJSON(response, http.StatusOK, auditListDTO{Items: items, NextCursor: next})
	}
}

func parseAuditQuery(request *http.Request) (int64, int, bool) {
	after, limit := int64(0), 100
	var err error
	if value := request.URL.Query().Get("afterId"); value != "" {
		after, err = strconv.ParseInt(value, 10, 64)
		if err != nil || after < 0 {
			return 0, 0, false
		}
	}
	if value := request.URL.Query().Get("limit"); value != "" {
		limit, err = strconv.Atoi(value)
		if err != nil || limit < 1 || limit > security.MaximumAuditPageSize {
			return 0, 0, false
		}
	}
	return after, limit, true
}

func mapAudit(event security.AuditEvent) auditDTO {
	return auditDTO{ID: event.ID, SubjectType: event.SubjectType, Action: event.Action, TargetType: event.TargetType,
		TargetID: event.TargetID, Result: event.Result, TraceID: event.TraceID, OperationID: event.OperationID,
		ClientType: event.ClientType, ErrorCode: event.ErrorCode, OccurredAt: formatAPITime(event.OccurredAt)}
}
