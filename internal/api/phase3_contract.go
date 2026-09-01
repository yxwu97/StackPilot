package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"stackpilot/internal/capability"
	"stackpilot/internal/domain"
	"stackpilot/internal/metrics"
	"stackpilot/internal/workspace"
)

type metricQueryService interface {
	Query(context.Context, metrics.Window) (metrics.WindowResult, error)
}

type metricPointDTO struct {
	ObservedAt     string   `json:"observedAt"`
	Status         string   `json:"status"`
	CPUPercent     *float64 `json:"cpuPercent,omitempty"`
	MemoryBytes    *int64   `json:"memoryBytes,omitempty"`
	ProcessCount   *int64   `json:"processCount,omitempty"`
	ContainerCount *int64   `json:"containerCount,omitempty"`
	ReasonCode     string   `json:"reasonCode,omitempty"`
}

type metricSeriesDTO struct {
	ServiceID string           `json:"serviceId"`
	Source    string           `json:"source"`
	Points    []metricPointDTO `json:"points"`
}

type metricSeriesListDTO struct {
	From        string            `json:"from"`
	To          string            `json:"to"`
	Granularity string            `json:"granularity"`
	Series      []metricSeriesDTO `json:"series"`
}

func registerPhase3ContractRoutes(router chi.Router, config Config) {
	if capabilityEnabled(config.Capabilities, capability.Phase3ResourceMonitoring) {
		router.Get("/workspaces/{workspaceID}/metrics", workspaceMetricsHandler(config.Workspaces, config.MetricQueries))
	} else {
		router.Get("/workspaces/{workspaceID}/metrics", featureNotEnabledHandler(capability.Phase3ResourceMonitoring))
	}
	registerChangePlanRoutes(router, config)
	if capabilityEnabled(config.Capabilities, capability.Phase3VerifiedRestart) {
		router.With(auditMutation(config.Audit, config.Logger, "verified-restart.create", "workspace", "workspaceID"),
			browserMutationGuard(config.Auth)).Post("/workspaces/{workspaceID}/verified-restart", verifiedRestartHandler(config.Workspaces, config.SingleService))
	} else {
		router.With(browserMutationGuard(config.Auth)).Post("/workspaces/{workspaceID}/verified-restart", featureNotEnabledHandler(capability.Phase3VerifiedRestart))
	}
}

func capabilityEnabled(values []string, name string) bool {
	for _, value := range values {
		if value == name {
			return true
		}
	}
	return false
}

func workspaceMetricsHandler(workspaces *workspace.Manager, queries metricQueryService) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		if workspaces == nil || queries == nil {
			writeRegisteredError(response, request, ErrorInternal)
			return
		}
		window, err := parseMetricWindow(request)
		if err != nil {
			writeRegisteredError(response, request, ErrorMetricQueryInvalid)
			return
		}
		if _, err := workspaces.Get(request.Context(), window.WorkspaceID); err != nil {
			writeRegisteredError(response, request, mapMetricWorkspaceError(err))
			return
		}
		result, err := queries.Query(request.Context(), window)
		if err != nil {
			writeRegisteredError(response, request, mapMetricQueryError(err))
			return
		}
		writeJSON(response, http.StatusOK, mapMetricWindow(result))
	}
}

func parseMetricWindow(request *http.Request) (metrics.Window, error) {
	workspaceID, err := domain.ParseWorkspaceID(chi.URLParam(request, "workspaceID"))
	if err != nil {
		return metrics.Window{}, err
	}
	start, err := parseUTCMetricTime(request.URL.Query().Get("from"))
	if err != nil {
		return metrics.Window{}, err
	}
	end, err := parseUTCMetricTime(request.URL.Query().Get("to"))
	if err != nil {
		return metrics.Window{}, err
	}
	if !start.Before(end) || end.Sub(start) > metrics.MaximumWindow {
		return metrics.Window{}, metrics.ErrInvalidQuery
	}
	granularity := request.URL.Query().Get("granularity")
	if granularity != "detail" && granularity != "hour" {
		return metrics.Window{}, metrics.ErrInvalidQuery
	}
	serviceIDs, err := parseMetricServiceIDs(request.URL.Query()["serviceId"])
	if err != nil {
		return metrics.Window{}, err
	}
	return metrics.Window{WorkspaceID: workspaceID, ServiceIDs: serviceIDs, Start: start, End: end,
		Hourly: granularity == "hour", Limit: metrics.MaximumPoints}, nil
}

func parseUTCMetricTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, err
	}
	_, offset := parsed.Zone()
	if offset != 0 {
		return time.Time{}, metrics.ErrInvalidQuery
	}
	return parsed.UTC(), nil
}

func parseMetricServiceIDs(values []string) ([]domain.ServiceID, error) {
	if len(values) > metrics.MaximumServices {
		return nil, metrics.ErrInvalidQuery
	}
	result := make([]domain.ServiceID, 0, len(values))
	seen := make(map[domain.ServiceID]struct{}, len(values))
	for _, value := range values {
		id, err := domain.ParseServiceID(value)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[id]; exists {
			return nil, metrics.ErrInvalidQuery
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	return result, nil
}

func mapMetricWorkspaceError(err error) ErrorCode {
	if errors.Is(err, workspace.ErrNotFound) {
		return ErrorWorkspaceNotFound
	}
	return ErrorInternal
}

func mapMetricQueryError(err error) ErrorCode {
	switch {
	case errors.Is(err, metrics.ErrInvalidQuery):
		return ErrorMetricQueryInvalid
	case errors.Is(err, metrics.ErrRuntimeUnavailable):
		return ErrorMetricSourceUnavailable
	default:
		return ErrorInternal
	}
}

func mapMetricWindow(result metrics.WindowResult) metricSeriesListDTO {
	granularity := "detail"
	if result.Hourly {
		granularity = "hour"
	}
	series := make([]metricSeriesDTO, 0, len(result.Series))
	for _, value := range result.Series {
		points := make([]metricPointDTO, 0, len(value.Points))
		for _, point := range value.Points {
			points = append(points, metricPointDTO{ObservedAt: point.ObservedAt.UTC().Format(time.RFC3339Nano), Status: string(point.Status),
				CPUPercent: point.CPUPercent, MemoryBytes: point.MemoryBytes, ProcessCount: point.ProcessCount,
				ContainerCount: point.ContainerCount, ReasonCode: point.ReasonCode})
		}
		series = append(series, metricSeriesDTO{ServiceID: value.ServiceID.String(), Source: string(value.Source), Points: points})
	}
	return metricSeriesListDTO{From: result.Start.UTC().Format(time.RFC3339Nano), To: result.End.UTC().Format(time.RFC3339Nano), Granularity: granularity, Series: series}
}

func featureNotEnabledHandler(feature string) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		writeRegisteredErrorDetails(response, request, ErrorFeatureNotEnabled, map[string]any{"feature": feature})
	}
}
