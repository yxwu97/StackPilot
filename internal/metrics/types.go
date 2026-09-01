// Package metrics owns bounded resource observations and sampling calculations.
package metrics

import (
	"errors"
	"time"

	"stackpilot/internal/domain"
)

const (
	DefaultInterval = 30 * time.Second
	MinimumInterval = 10 * time.Second
	MaximumInterval = 5 * time.Minute
	MaximumServices = 100
	MaximumPoints   = 2000
	MaximumWindow   = 31 * 24 * time.Hour
)

var (
	ErrInvalidSample = errors.New("runtime metric sample is invalid")
	ErrInvalidQuery  = errors.New("runtime metric query is invalid")
)

// Sample is one bounded service-level resource observation.
type Sample struct {
	ID                int64
	ServiceInstanceID domain.ServiceInstanceID
	Source            domain.MetricSource
	Status            domain.MetricStatus
	ObservedAt        time.Time
	Interval          time.Duration
	CPUTotalMillis    *int64
	CPUPercent        *float64
	MemoryBytes       *int64
	ProcessCount      *int64
	ContainerCount    *int64
	ReasonCode        string
	Containers        []ContainerSample
}

// ContainerSample is one exact Compose container observation.
type ContainerSample struct {
	ContainerID    string
	ComposeService string
	CPUPercent     float64
	MemoryBytes    int64
}

// HourlyAggregate summarizes persisted resource samples for one UTC hour.
type HourlyAggregate struct {
	ServiceInstanceID domain.ServiceInstanceID
	Source            domain.MetricSource
	BucketStart       time.Time
	SampleCount       int64
	AvailableCount    int64
	CPUSampleCount    int64
	CPUMinPercent     *float64
	CPUMaxPercent     *float64
	CPUTotalPercent   *float64
	MemorySampleCount int64
	MemoryMinBytes    *int64
	MemoryMaxBytes    *int64
	MemoryTotalBytes  *int64
}

// Query defines a bounded detail or hourly metric time window.
type Query struct {
	ServiceInstanceIDs []domain.ServiceInstanceID
	Start              time.Time
	End                time.Time
	Hourly             bool
	Limit              int
}
