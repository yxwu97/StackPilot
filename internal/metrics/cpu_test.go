package metrics

import (
	"testing"
	"time"

	"stackpilot/internal/domain"
)

func TestCPUTrackerNormalizesAndRejectsCounterDiscontinuity(t *testing.T) {
	tracker := NewCPUTracker(4)
	id := domain.ServiceInstanceID("svi_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	start := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	first := tracker.Sample(Counter{ServiceInstanceID: id, ObservedAt: start, CPUTotalMillis: 1000, MemoryBytes: 1024, ProcessCount: 2}, DefaultInterval)
	if first.CPUPercent != nil || first.CPUTotalMillis == nil || *first.CPUTotalMillis != 1000 {
		t.Fatalf("first sample = %#v", first)
	}
	second := tracker.Sample(Counter{ServiceInstanceID: id, ObservedAt: start.Add(10 * time.Second), CPUTotalMillis: 3000, MemoryBytes: 2048, ProcessCount: 3}, DefaultInterval)
	if second.CPUPercent == nil || *second.CPUPercent != 5 {
		t.Fatalf("normalized CPU = %#v", second.CPUPercent)
	}
	rollback := tracker.Sample(Counter{ServiceInstanceID: id, ObservedAt: start.Add(20 * time.Second), CPUTotalMillis: 10, MemoryBytes: 2048, ProcessCount: 1}, DefaultInterval)
	if rollback.CPUPercent != nil {
		t.Fatalf("rollback CPU = %v", *rollback.CPUPercent)
	}
}

func TestValidateSampleRequiresExplicitUnavailableReason(t *testing.T) {
	sample := Sample{
		ServiceInstanceID: "svi_01ARZ3NDEKTSV4RRFFQ69G5FAV", Source: domain.MetricSourceProcessJob,
		Status: domain.MetricUnavailable, ObservedAt: time.Now().UTC(), Interval: DefaultInterval,
	}
	if ValidateSample(sample) == nil {
		t.Fatal("unavailable sample without reason unexpectedly accepted")
	}
	sample.ReasonCode = "SUPERVISOR_UNAVAILABLE"
	if err := ValidateSample(sample); err != nil {
		t.Fatalf("ValidateSample() error = %v", err)
	}
}
