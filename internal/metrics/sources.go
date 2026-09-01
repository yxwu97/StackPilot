package metrics

import (
	"context"
	"errors"
	"time"

	"stackpilot/internal/domain"
	base "stackpilot/internal/driver"
	"stackpilot/internal/driver/compose"
	processdriver "stackpilot/internal/driver/process"
)

// ProcessSource observes complete supervised process trees through the driver.
type ProcessSource struct {
	observer base.ResourceObserver
	tracker  *CPUTracker
}

// NewProcessSource constructs the Job Object resource adapter.
func NewProcessSource(observer base.ResourceObserver, logicalProcessors int) (*ProcessSource, error) {
	if observer == nil {
		return nil, ErrInvalidSample
	}
	return &ProcessSource{observer: observer, tracker: NewCPUTracker(logicalProcessors)}, nil
}

// Observe returns an available, unavailable, or unsupported process-tree sample.
func (source *ProcessSource) Observe(ctx context.Context, service domain.ServiceInstance, interval time.Duration) Sample {
	if service.Identity == nil {
		return unavailableSample(service.ID, domain.MetricSourceProcessJob, interval, "PROCESS_IDENTITY_UNAVAILABLE")
	}
	observed, err := source.observer.ObserveResources(ctx, *service.Identity)
	if err != nil {
		status := domain.MetricUnavailable
		if errors.Is(err, processdriver.ErrResourceUnsupported) {
			status = domain.MetricUnsupported
		}
		return missingSample(service.ID, domain.MetricSourceProcessJob, status, interval, processReason(err))
	}
	if observed.MemoryBytes > uint64(^uint64(0)>>1) {
		return unavailableSample(service.ID, domain.MetricSourceProcessJob, interval, "PROCESS_COUNTER_INVALID")
	}
	return source.tracker.Sample(Counter{
		ServiceInstanceID: service.ID, ObservedAt: observed.ObservedAt, CPUTotalMillis: observed.CPUTotalMillis,
		MemoryBytes: int64(observed.MemoryBytes), ProcessCount: int64(observed.ActiveProcesses),
	}, interval)
}

// ComposeObserver returns one strict-identity Compose resource observation.
type ComposeObserver interface {
	ObserveResources(context.Context, string) (compose.ResourceObservation, error)
}

// ComposeSource adapts exact Compose container observations to metric samples.
type ComposeSource struct {
	observer ComposeObserver
}

// NewComposeSource constructs the Compose resource adapter.
func NewComposeSource(observer ComposeObserver) (*ComposeSource, error) {
	if observer == nil {
		return nil, ErrInvalidSample
	}
	return &ComposeSource{observer: observer}, nil
}

// Observe returns an aggregated service sample with bounded container details.
func (source *ComposeSource) Observe(ctx context.Context, service domain.ServiceInstance, interval time.Duration) Sample {
	if service.ComposeIdentity == "" {
		return unavailableSample(service.ID, domain.MetricSourceCompose, interval, "COMPOSE_IDENTITY_UNAVAILABLE")
	}
	observed, err := source.observer.ObserveResources(ctx, service.ComposeIdentity)
	if err != nil {
		status := domain.MetricUnavailable
		if errors.Is(err, compose.ErrPlatformUnsupported) {
			status = domain.MetricUnsupported
		}
		return missingSample(service.ID, domain.MetricSourceCompose, status, interval, composeReason(err))
	}
	cpu, memory, count := observed.CPUPercent, observed.MemoryBytes, int64(len(observed.Containers))
	containers := make([]ContainerSample, 0, len(observed.Containers))
	for _, item := range observed.Containers {
		containers = append(containers, ContainerSample{ContainerID: item.ID, ComposeService: item.ComposeService, CPUPercent: item.CPUPercent, MemoryBytes: item.MemoryBytes})
	}
	return Sample{
		ServiceInstanceID: service.ID, Source: domain.MetricSourceCompose, Status: domain.MetricAvailable,
		ObservedAt: observed.ObservedAt, Interval: interval, CPUPercent: &cpu, MemoryBytes: &memory,
		ContainerCount: &count, Containers: containers,
	}
}

func unavailableSample(id domain.ServiceInstanceID, source domain.MetricSource, interval time.Duration, reason string) Sample {
	return missingSample(id, source, domain.MetricUnavailable, interval, reason)
}

func missingSample(id domain.ServiceInstanceID, source domain.MetricSource, status domain.MetricStatus, interval time.Duration, reason string) Sample {
	return Sample{ServiceInstanceID: id, Source: source, Status: status, ObservedAt: time.Now().UTC(), Interval: interval, ReasonCode: reason}
}

func processReason(err error) string {
	switch {
	case errors.Is(err, processdriver.ErrResourceUnsupported):
		return "SUPERVISOR_PROTOCOL_UNSUPPORTED"
	case errors.Is(err, base.ErrIdentityMismatch), errors.Is(err, processdriver.ErrIdentityMismatch):
		return "PROCESS_IDENTITY_MISMATCH"
	case errors.Is(err, context.DeadlineExceeded):
		return "SUPERVISOR_TIMEOUT"
	default:
		return "SUPERVISOR_UNAVAILABLE"
	}
}

func composeReason(err error) string {
	switch {
	case errors.Is(err, compose.ErrPlatformUnsupported):
		return "COMPOSE_STATS_UNSUPPORTED"
	case errors.Is(err, compose.ErrProjectIdentityMismatch):
		return "COMPOSE_IDENTITY_MISMATCH"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, compose.ErrLifecycleTimeout):
		return "COMPOSE_STATS_TIMEOUT"
	default:
		return "COMPOSE_STATS_UNAVAILABLE"
	}
}
