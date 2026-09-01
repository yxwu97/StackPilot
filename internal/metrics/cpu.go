package metrics

import (
	"sync"
	"time"

	"stackpilot/internal/domain"
)

// Counter contains cumulative Job Object facts used to derive normalized CPU.
type Counter struct {
	ServiceInstanceID domain.ServiceInstanceID
	ObservedAt        time.Time
	CPUTotalMillis    int64
	MemoryBytes       int64
	ProcessCount      int64
}

// CPUTracker keeps one previous cumulative counter per service instance.
type CPUTracker struct {
	mu       sync.Mutex
	previous map[domain.ServiceInstanceID]Counter
	cores    int
}

// NewCPUTracker constructs a machine-normalized CPU tracker.
func NewCPUTracker(logicalProcessors int) *CPUTracker {
	if logicalProcessors < 1 {
		logicalProcessors = 1
	}
	return &CPUTracker{previous: make(map[domain.ServiceInstanceID]Counter), cores: logicalProcessors}
}

// Sample records a cumulative counter and derives CPU when a prior point exists.
func (tracker *CPUTracker) Sample(counter Counter, interval time.Duration) Sample {
	tracker.mu.Lock()
	previous, found := tracker.previous[counter.ServiceInstanceID]
	tracker.previous[counter.ServiceInstanceID] = counter
	tracker.mu.Unlock()
	sample := Sample{
		ServiceInstanceID: counter.ServiceInstanceID, Source: domain.MetricSourceProcessJob,
		Status: domain.MetricAvailable, ObservedAt: counter.ObservedAt, Interval: interval,
		CPUTotalMillis: int64Pointer(counter.CPUTotalMillis), MemoryBytes: int64Pointer(counter.MemoryBytes),
		ProcessCount: int64Pointer(counter.ProcessCount),
	}
	if found {
		sample.CPUPercent = tracker.percentage(previous, counter)
	}
	return sample
}

// Forget removes the previous counter after an identity or lifecycle change.
func (tracker *CPUTracker) Forget(id domain.ServiceInstanceID) {
	tracker.mu.Lock()
	delete(tracker.previous, id)
	tracker.mu.Unlock()
}

func (tracker *CPUTracker) percentage(previous, current Counter) *float64 {
	elapsed := current.ObservedAt.Sub(previous.ObservedAt)
	delta := current.CPUTotalMillis - previous.CPUTotalMillis
	if elapsed <= 0 || delta < 0 {
		return nil
	}
	value := float64(delta) / float64(elapsed.Milliseconds()) / float64(tracker.cores) * 100
	if value < 0 || value > 100 {
		return nil
	}
	return &value
}

func int64Pointer(value int64) *int64 { return &value }
