package metrics

import (
	"context"
	"errors"
	"testing"
	"time"

	"stackpilot/internal/domain"
)

const (
	queryWorkspaceID = domain.WorkspaceID("ws_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	querySystemID    = domain.SystemInstanceID("si_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	queryServiceID   = domain.ServiceInstanceID("svi_01ARZ3NDEKTSV4RRFFQ69G5FAV")
)

type queryRuntimeStub struct {
	active   bool
	services []domain.ServiceInstance
}

func (stub queryRuntimeStub) GetActive(context.Context, domain.WorkspaceID) (*domain.SystemInstance, bool, error) {
	return &domain.SystemInstance{ID: querySystemID, WorkspaceID: queryWorkspaceID}, stub.active, nil
}

func (stub queryRuntimeStub) ListServices(context.Context, domain.SystemInstanceID) ([]domain.ServiceInstance, error) {
	return stub.services, nil
}

type queryStoreStub struct {
	query      Query
	detail     []Sample
	aggregates []HourlyAggregate
}

func (stub *queryStoreStub) ListSamples(_ context.Context, query Query) ([]Sample, error) {
	stub.query = query
	return stub.detail, nil
}

func (stub *queryStoreStub) ListHourly(_ context.Context, query Query) ([]HourlyAggregate, error) {
	stub.query = query
	return stub.aggregates, nil
}

func TestQueryServiceMapsCurrentRuntimeDetailWithoutPlatformIdentity(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	cpu, memory, processes := 12.5, int64(4096), int64(2)
	store := &queryStoreStub{detail: []Sample{{ServiceInstanceID: queryServiceID, Source: domain.MetricSourceProcessJob,
		Status: domain.MetricAvailable, ObservedAt: now.Add(-time.Minute), CPUPercent: &cpu, MemoryBytes: &memory, ProcessCount: &processes}}}
	service, err := NewQueryService(queryRuntimeStub{active: true, services: []domain.ServiceInstance{{
		ID: queryServiceID, SystemInstanceID: querySystemID, ServiceID: "backend",
	}}}, store)
	if err != nil {
		t.Fatalf("NewQueryService() error = %v", err)
	}
	result, err := service.Query(context.Background(), Window{WorkspaceID: queryWorkspaceID, Start: now.Add(-time.Hour), End: now, Limit: 100})
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if len(result.Series) != 1 || result.Series[0].ServiceID != "backend" || result.Series[0].Source != domain.MetricSourceProcessJob || len(result.Series[0].Points) != 1 {
		t.Fatalf("Query() result = %#v", result)
	}
	if len(store.query.ServiceInstanceIDs) != 1 || store.query.ServiceInstanceIDs[0] != queryServiceID {
		t.Fatalf("persisted query identities = %#v", store.query.ServiceInstanceIDs)
	}
}

func TestQueryServiceMapsHourlyAveragesAndStableSeriesOrder(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	cpuTotal, memoryTotal := 25.0, int64(3072)
	store := &queryStoreStub{aggregates: []HourlyAggregate{{ServiceInstanceID: queryServiceID, Source: domain.MetricSourceCompose,
		BucketStart: now.Add(-time.Hour), SampleCount: 2, AvailableCount: 2, CPUSampleCount: 2, CPUTotalPercent: &cpuTotal,
		MemorySampleCount: 2, MemoryTotalBytes: &memoryTotal}}}
	service, _ := NewQueryService(queryRuntimeStub{active: true, services: []domain.ServiceInstance{{
		ID: queryServiceID, SystemInstanceID: querySystemID, ServiceID: "web",
	}}}, store)
	result, err := service.Query(context.Background(), Window{WorkspaceID: queryWorkspaceID, ServiceIDs: []domain.ServiceID{"web"},
		Start: now.Add(-2 * time.Hour), End: now, Hourly: true, Limit: 100})
	if err != nil {
		t.Fatalf("Query(hourly) error = %v", err)
	}
	point := result.Series[0].Points[0]
	if point.CPUPercent == nil || *point.CPUPercent != 12.5 || point.MemoryBytes == nil || *point.MemoryBytes != 1536 || point.Status != domain.MetricAvailable {
		t.Fatalf("hourly point = %#v", point)
	}
}

func TestQueryServiceRejectsMissingRuntimeAndUnknownService(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	service, _ := NewQueryService(queryRuntimeStub{}, &queryStoreStub{})
	_, err := service.Query(context.Background(), Window{WorkspaceID: queryWorkspaceID, Start: now.Add(-time.Hour), End: now, Limit: 1})
	if !errors.Is(err, ErrRuntimeUnavailable) {
		t.Fatalf("Query(stopped) error = %v, want ErrRuntimeUnavailable", err)
	}
	service, _ = NewQueryService(queryRuntimeStub{active: true, services: []domain.ServiceInstance{{ID: queryServiceID, ServiceID: "web"}}}, &queryStoreStub{})
	_, err = service.Query(context.Background(), Window{WorkspaceID: queryWorkspaceID, ServiceIDs: []domain.ServiceID{"missing"}, Start: now.Add(-time.Hour), End: now, Limit: 1})
	if !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("Query(unknown service) error = %v, want ErrInvalidQuery", err)
	}
}
