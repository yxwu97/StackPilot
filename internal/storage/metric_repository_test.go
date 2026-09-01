package storage

import (
	"context"
	"testing"
	"time"

	"stackpilot/internal/domain"
	"stackpilot/internal/metrics"
)

func TestMetricRepositoryBatchQueryAndRetention(t *testing.T) {
	database := openTestDatabase(t)
	serviceID := seedRuntimeInstance(t, database)
	repository, err := NewMetricRepository(database)
	if err != nil {
		t.Fatalf("NewMetricRepository() error = %v", err)
	}
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	oldTime := now.Add(-25 * time.Hour)
	cpu, memory, processes := 20.0, int64(4096), int64(2)
	samples := []metrics.Sample{
		{ServiceInstanceID: serviceID, Source: domain.MetricSourceProcessJob, Status: domain.MetricAvailable, ObservedAt: oldTime, Interval: metrics.DefaultInterval, CPUPercent: &cpu, MemoryBytes: &memory, ProcessCount: &processes},
		{ServiceInstanceID: serviceID, Source: domain.MetricSourceProcessJob, Status: domain.MetricUnavailable, ObservedAt: oldTime.Add(30 * time.Second), Interval: metrics.DefaultInterval, ReasonCode: "SUPERVISOR_UNAVAILABLE"},
		{ServiceInstanceID: serviceID, Source: domain.MetricSourceProcessJob, Status: domain.MetricAvailable, ObservedAt: now.Add(-time.Hour), Interval: metrics.DefaultInterval, CPUPercent: &cpu, MemoryBytes: &memory, ProcessCount: &processes},
	}
	if err := repository.SaveBatch(context.Background(), samples); err != nil {
		t.Fatalf("SaveBatch() error = %v", err)
	}
	query := metrics.Query{ServiceInstanceIDs: []domain.ServiceInstanceID{serviceID}, Start: now.Add(-26 * time.Hour), End: now, Limit: 10}
	details, err := repository.ListSamples(context.Background(), query)
	if err != nil || len(details) != 3 {
		t.Fatalf("ListSamples() = %d, %v", len(details), err)
	}
	removed, err := repository.CompactDefault(context.Background(), now)
	if err != nil || removed != 2 {
		t.Fatalf("CompactDefault() = %d, %v", removed, err)
	}
	details, err = repository.ListSamples(context.Background(), query)
	if err != nil || len(details) != 1 {
		t.Fatalf("ListSamples(compacted) = %d, %v", len(details), err)
	}
	query.Hourly = true
	aggregates, err := repository.ListHourly(context.Background(), query)
	if err != nil || len(aggregates) != 1 {
		t.Fatalf("ListHourly() = %#v, %v", aggregates, err)
	}
	if aggregates[0].SampleCount != 2 || aggregates[0].AvailableCount != 1 || aggregates[0].CPUSampleCount != 1 || aggregates[0].CPUTotalPercent == nil || *aggregates[0].CPUTotalPercent != 20 {
		t.Fatalf("hourly aggregate = %#v", aggregates[0])
	}
}

func TestMetricRepositoryRejectsInvalidBatchAtomically(t *testing.T) {
	database := openTestDatabase(t)
	serviceID := seedRuntimeInstance(t, database)
	repository, _ := NewMetricRepository(database)
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	value := int64(1)
	valid := metrics.Sample{ServiceInstanceID: serviceID, Source: domain.MetricSourceProcessJob, Status: domain.MetricAvailable, ObservedAt: now, Interval: metrics.DefaultInterval, ProcessCount: &value}
	invalid := valid
	invalid.ObservedAt = now.In(time.Local)
	if err := repository.SaveBatch(context.Background(), []metrics.Sample{valid, invalid}); err == nil {
		t.Fatal("invalid batch unexpectedly persisted")
	}
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM runtime_metric_samples`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("metric count = %d, %v", count, err)
	}
}

func TestMetricRepositoryPersistsContainerDetailsOnlyForCompose(t *testing.T) {
	database := openTestDatabase(t)
	serviceID := seedRuntimeInstance(t, database)
	repository, _ := NewMetricRepository(database)
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	cpu, memory, containers := 2.5, int64(2048), int64(1)
	sample := metrics.Sample{
		ServiceInstanceID: serviceID, Source: domain.MetricSourceCompose, Status: domain.MetricAvailable,
		ObservedAt: now, Interval: metrics.DefaultInterval, CPUPercent: &cpu, MemoryBytes: &memory, ContainerCount: &containers,
		Containers: []metrics.ContainerSample{{ContainerID: "aaaaaaaaaaaa", ComposeService: "database", CPUPercent: cpu, MemoryBytes: memory}},
	}
	if err := repository.SaveBatch(context.Background(), []metrics.Sample{sample}); err != nil {
		t.Fatalf("SaveBatch(Compose) error = %v", err)
	}
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM runtime_container_metric_samples`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("container metric count = %d, %v", count, err)
	}
	if _, err := database.Exec(`INSERT INTO runtime_container_metric_samples(metric_sample_id,container_id,compose_service,cpu_percent,memory_bytes)
		VALUES ((SELECT id FROM runtime_metric_samples LIMIT 1),'aaaaaaaaaaaa','cache',1,1)`); err == nil {
		t.Fatal("duplicate container detail unexpectedly accepted")
	}
	processCount := int64(1)
	process := metrics.Sample{ServiceInstanceID: serviceID, Source: domain.MetricSourceProcessJob, Status: domain.MetricAvailable, ObservedAt: now.Add(time.Second), Interval: metrics.DefaultInterval, ProcessCount: &processCount}
	if err := repository.SaveBatch(context.Background(), []metrics.Sample{process}); err != nil {
		t.Fatalf("SaveBatch(process) error = %v", err)
	}
	if _, err := database.Exec(`INSERT INTO runtime_container_metric_samples(metric_sample_id,container_id,compose_service,cpu_percent,memory_bytes)
        VALUES ((SELECT id FROM runtime_metric_samples WHERE source='process-job'),'cccccccccccc','cache',1,1)`); err == nil {
		t.Fatal("container detail on process sample unexpectedly accepted")
	}
}
