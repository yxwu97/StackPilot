package metrics

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"stackpilot/internal/domain"
)

// ErrRuntimeUnavailable reports that a workspace has no active managed runtime.
var ErrRuntimeUnavailable = errors.New("runtime metrics require an active managed instance")

// QueryStore reads bounded persisted detail and hourly metric windows.
type QueryStore interface {
	ListSamples(context.Context, Query) ([]Sample, error)
	ListHourly(context.Context, Query) ([]HourlyAggregate, error)
}

// RuntimeResolver resolves the current managed service identities for a workspace.
type RuntimeResolver interface {
	GetActive(context.Context, domain.WorkspaceID) (*domain.SystemInstance, bool, error)
	ListServices(context.Context, domain.SystemInstanceID) ([]domain.ServiceInstance, error)
}

// Window requests one bounded workspace metric window.
type Window struct {
	WorkspaceID domain.WorkspaceID
	ServiceIDs  []domain.ServiceID
	Start       time.Time
	End         time.Time
	Hourly      bool
	Limit       int
}

// Point is one safe service-level metric point.
type Point struct {
	ObservedAt     time.Time
	Status         domain.MetricStatus
	CPUPercent     *float64
	MemoryBytes    *int64
	ProcessCount   *int64
	ContainerCount *int64
	ReasonCode     string
}

// Series groups safe points by logical service and trusted source.
type Series struct {
	ServiceID domain.ServiceID
	Source    domain.MetricSource
	Points    []Point
}

// WindowResult is a bounded, stable-order metric response.
type WindowResult struct {
	Start  time.Time
	End    time.Time
	Hourly bool
	Series []Series
}

// QueryService owns runtime identity resolution and persisted metric shaping.
type QueryService struct {
	runtime RuntimeResolver
	store   QueryStore
}

// NewQueryService constructs a read-only metric query use case.
func NewQueryService(runtime RuntimeResolver, store QueryStore) (*QueryService, error) {
	if runtime == nil || store == nil {
		return nil, fmt.Errorf("metric query dependencies are required")
	}
	return &QueryService{runtime: runtime, store: store}, nil
}

// Query returns metrics only for the current managed instance of the workspace.
func (service *QueryService) Query(ctx context.Context, window Window) (WindowResult, error) {
	if _, err := domain.ParseWorkspaceID(window.WorkspaceID.String()); err != nil || len(window.ServiceIDs) > MaximumServices {
		return WindowResult{}, ErrInvalidQuery
	}
	system, exists, err := service.runtime.GetActive(ctx, window.WorkspaceID)
	if err != nil {
		return WindowResult{}, fmt.Errorf("resolve active metric runtime: %w", err)
	}
	if !exists {
		return WindowResult{}, ErrRuntimeUnavailable
	}
	services, err := service.runtime.ListServices(ctx, system.ID)
	if err != nil {
		return WindowResult{}, fmt.Errorf("list metric runtime services: %w", err)
	}
	instances, names, err := selectMetricServices(services, window.ServiceIDs)
	if err != nil {
		return WindowResult{}, err
	}
	query := Query{ServiceInstanceIDs: instances, Start: window.Start, End: window.End, Hourly: window.Hourly, Limit: window.Limit}
	if err := ValidateQuery(query); err != nil {
		return WindowResult{}, err
	}
	if window.Hourly {
		return service.queryHourly(ctx, window, query, names)
	}
	return service.queryDetail(ctx, window, query, names)
}

func selectMetricServices(services []domain.ServiceInstance, requested []domain.ServiceID) ([]domain.ServiceInstanceID, map[domain.ServiceInstanceID]domain.ServiceID, error) {
	allowed := make(map[domain.ServiceID]struct{}, len(requested))
	for _, id := range requested {
		if _, err := domain.ParseServiceID(id.String()); err != nil {
			return nil, nil, ErrInvalidQuery
		}
		if _, exists := allowed[id]; exists {
			return nil, nil, ErrInvalidQuery
		}
		allowed[id] = struct{}{}
	}
	instances := make([]domain.ServiceInstanceID, 0, len(services))
	names := make(map[domain.ServiceInstanceID]domain.ServiceID, len(services))
	for _, runtime := range services {
		if len(requested) > 0 {
			if _, selected := allowed[runtime.ServiceID]; !selected {
				continue
			}
			delete(allowed, runtime.ServiceID)
		}
		instances = append(instances, runtime.ID)
		names[runtime.ID] = runtime.ServiceID
	}
	if len(allowed) > 0 || len(instances) == 0 {
		return nil, nil, ErrInvalidQuery
	}
	return instances, names, nil
}

func (service *QueryService) queryDetail(ctx context.Context, window Window, query Query, names map[domain.ServiceInstanceID]domain.ServiceID) (WindowResult, error) {
	values, err := service.store.ListSamples(ctx, query)
	if err != nil {
		return WindowResult{}, err
	}
	series := make(map[seriesKey][]Point)
	for _, value := range values {
		key := seriesKey{serviceID: names[value.ServiceInstanceID], source: value.Source}
		series[key] = append(series[key], Point{ObservedAt: value.ObservedAt, Status: value.Status, CPUPercent: value.CPUPercent,
			MemoryBytes: value.MemoryBytes, ProcessCount: value.ProcessCount, ContainerCount: value.ContainerCount, ReasonCode: value.ReasonCode})
	}
	return windowResult(window, series), nil
}

func (service *QueryService) queryHourly(ctx context.Context, window Window, query Query, names map[domain.ServiceInstanceID]domain.ServiceID) (WindowResult, error) {
	values, err := service.store.ListHourly(ctx, query)
	if err != nil {
		return WindowResult{}, err
	}
	series := make(map[seriesKey][]Point)
	for _, value := range values {
		status := domain.MetricUnavailable
		if value.AvailableCount > 0 {
			status = domain.MetricAvailable
		}
		key := seriesKey{serviceID: names[value.ServiceInstanceID], source: value.Source}
		series[key] = append(series[key], Point{ObservedAt: value.BucketStart, Status: status,
			CPUPercent: averageFloat(value.CPUTotalPercent, value.CPUSampleCount), MemoryBytes: averageInt(value.MemoryTotalBytes, value.MemorySampleCount)})
	}
	return windowResult(window, series), nil
}

type seriesKey struct {
	serviceID domain.ServiceID
	source    domain.MetricSource
}

func windowResult(window Window, values map[seriesKey][]Point) WindowResult {
	keys := make([]seriesKey, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(left, right int) bool {
		if keys[left].serviceID != keys[right].serviceID {
			return keys[left].serviceID < keys[right].serviceID
		}
		return keys[left].source < keys[right].source
	})
	series := make([]Series, 0, len(keys))
	for _, key := range keys {
		points := values[key]
		sort.SliceStable(points, func(left, right int) bool { return points[left].ObservedAt.Before(points[right].ObservedAt) })
		series = append(series, Series{ServiceID: key.serviceID, Source: key.source, Points: points})
	}
	return WindowResult{Start: window.Start, End: window.End, Hourly: window.Hourly, Series: series}
}

func averageFloat(total *float64, count int64) *float64 {
	if total == nil || count < 1 {
		return nil
	}
	value := *total / float64(count)
	return &value
}

func averageInt(total *int64, count int64) *int64 {
	if total == nil || count < 1 {
		return nil
	}
	value := *total / count
	return &value
}
