package metrics

import (
	"math"
	"strings"
	"time"

	"stackpilot/internal/domain"
)

// ValidateSample enforces the closed resource sample contract.
func ValidateSample(sample Sample) error {
	if _, err := domain.ParseServiceInstanceID(sample.ServiceInstanceID.String()); err != nil || sample.Source.Validate() != nil || sample.Status.Validate() != nil {
		return ErrInvalidSample
	}
	if sample.ObservedAt.IsZero() || sample.ObservedAt.Location() != time.UTC || sample.Interval < MinimumInterval || sample.Interval > MaximumInterval {
		return ErrInvalidSample
	}
	available := sample.CPUTotalMillis != nil || sample.CPUPercent != nil || sample.MemoryBytes != nil || sample.ProcessCount != nil || sample.ContainerCount != nil
	if sample.Status == domain.MetricAvailable {
		if !available || sample.ReasonCode != "" {
			return ErrInvalidSample
		}
	} else if available || len(sample.ReasonCode) < 3 || len(sample.ReasonCode) > 128 {
		return ErrInvalidSample
	}
	if sample.Source == domain.MetricSourceProcessJob && sample.ContainerCount != nil || sample.Source == domain.MetricSourceCompose && sample.ProcessCount != nil {
		return ErrInvalidSample
	}
	if negative(sample.CPUTotalMillis) || negative(sample.MemoryBytes) || negative(sample.ProcessCount) || negative(sample.ContainerCount) {
		return ErrInvalidSample
	}
	if sample.CPUPercent != nil && (math.IsNaN(*sample.CPUPercent) || math.IsInf(*sample.CPUPercent, 0) || *sample.CPUPercent < 0 || *sample.CPUPercent > 100) {
		return ErrInvalidSample
	}
	if err := validateContainers(sample); err != nil {
		return err
	}
	return nil
}

func validateContainers(sample Sample) error {
	if len(sample.Containers) == 0 {
		return nil
	}
	if sample.Source != domain.MetricSourceCompose || sample.Status != domain.MetricAvailable || sample.ContainerCount == nil || int64(len(sample.Containers)) != *sample.ContainerCount {
		return ErrInvalidSample
	}
	seen := make(map[string]struct{}, len(sample.Containers))
	for _, item := range sample.Containers {
		if len(item.ContainerID) < 12 || len(item.ContainerID) > 64 || item.ComposeService == "" || len(item.ComposeService) > 128 || item.MemoryBytes < 0 ||
			math.IsNaN(item.CPUPercent) || math.IsInf(item.CPUPercent, 0) || item.CPUPercent < 0 || item.CPUPercent > 100 || strings.ContainsAny(item.ContainerID, " \t\r\n") {
			return ErrInvalidSample
		}
		if _, exists := seen[item.ContainerID]; exists {
			return ErrInvalidSample
		}
		seen[item.ContainerID] = struct{}{}
	}
	return nil
}

// ValidateQuery enforces service, time-window, and point-count limits.
func ValidateQuery(query Query) error {
	if len(query.ServiceInstanceIDs) < 1 || len(query.ServiceInstanceIDs) > MaximumServices || query.Limit < 1 || query.Limit > MaximumPoints {
		return ErrInvalidQuery
	}
	if query.Start.IsZero() || query.End.IsZero() || query.Start.Location() != time.UTC || query.End.Location() != time.UTC || !query.Start.Before(query.End) || query.End.Sub(query.Start) > MaximumWindow {
		return ErrInvalidQuery
	}
	seen := make(map[domain.ServiceInstanceID]struct{}, len(query.ServiceInstanceIDs))
	for _, id := range query.ServiceInstanceIDs {
		if _, err := domain.ParseServiceInstanceID(id.String()); err != nil {
			return ErrInvalidQuery
		}
		if _, exists := seen[id]; exists {
			return ErrInvalidQuery
		}
		seen[id] = struct{}{}
	}
	return nil
}

func negative(value *int64) bool { return value != nil && *value < 0 }
