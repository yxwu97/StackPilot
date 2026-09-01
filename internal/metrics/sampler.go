package metrics

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"stackpilot/internal/domain"
)

const (
	DefaultWorkers       = 4
	DefaultQueueCapacity = 128
	DefaultSampleTimeout = 5 * time.Second
	retentionInterval    = time.Hour
)

// RuntimeSource supplies active systems and their persisted service instances.
type RuntimeSource interface {
	ListActive(context.Context) ([]domain.SystemInstance, error)
	ListServices(context.Context, domain.SystemInstanceID) ([]domain.ServiceInstance, error)
}

// Store persists one bounded sample batch atomically.
type Store interface {
	SaveBatch(context.Context, []Sample) error
}

// RetentionStore compacts detail samples into bounded history.
type RetentionStore interface {
	CompactDefault(context.Context, time.Time) (int64, error)
}

// SamplerConfig defines resource sources, limits, and lifecycle ownership.
type SamplerConfig struct {
	Runtime       RuntimeSource
	Store         Store
	Retention     RetentionStore
	Process       *ProcessSource
	Compose       *ComposeSource
	Interval      time.Duration
	Workers       int
	QueueCapacity int
	SampleTimeout time.Duration
	Logger        *slog.Logger
}

// Sampler periodically observes eligible services with a bounded worker pool.
type Sampler struct {
	config  SamplerConfig
	mu      sync.Mutex
	started bool
	wait    sync.WaitGroup
}

// NewSampler validates limits and constructs a resource sampler.
func NewSampler(config SamplerConfig) (*Sampler, error) {
	if config.Runtime == nil || config.Store == nil || config.Process == nil || config.Compose == nil {
		return nil, fmt.Errorf("metric sampler dependencies are required")
	}
	applySamplerDefaults(&config)
	if config.Interval < MinimumInterval || config.Interval > MaximumInterval || config.Workers < 1 || config.Workers > 32 ||
		config.QueueCapacity < 1 || config.QueueCapacity > 4096 || config.SampleTimeout <= 0 || config.SampleTimeout > config.Interval {
		return nil, fmt.Errorf("metric sampler configuration is invalid")
	}
	return &Sampler{config: config}, nil
}

// Start launches one context-owned sampling loop.
func (sampler *Sampler) Start(ctx context.Context) error {
	sampler.mu.Lock()
	defer sampler.mu.Unlock()
	if sampler.started {
		return fmt.Errorf("metric sampler is already started")
	}
	sampler.started = true
	sampler.wait.Add(1)
	go sampler.loop(ctx)
	return nil
}

// Wait blocks until the started sampling loop exits.
func (sampler *Sampler) Wait() { sampler.wait.Wait() }

// RunOnce performs one bounded observation and persistence cycle.
func (sampler *Sampler) RunOnce(ctx context.Context) error {
	services, err := sampler.eligibleServices(ctx)
	if err != nil {
		return err
	}
	if len(services) == 0 {
		return nil
	}
	if len(services) > sampler.config.QueueCapacity {
		services = services[:sampler.config.QueueCapacity]
		sampler.log("metric sample queue capacity reached", "reason", "queue_full")
	}
	samples := sampler.observeServices(ctx, services)
	if len(samples) == 0 {
		return nil
	}
	return sampler.config.Store.SaveBatch(ctx, samples)
}

func (sampler *Sampler) loop(ctx context.Context) {
	defer sampler.wait.Done()
	nextRetention := time.Now().UTC()
	for {
		if err := sampler.RunOnce(ctx); err != nil && ctx.Err() == nil {
			sampler.log("metric sample cycle failed", "error_code", "METRIC_SAMPLE_CYCLE_FAILED", "error", err)
		}
		if !time.Now().UTC().Before(nextRetention) {
			if _, err := sampler.compact(ctx); err != nil && ctx.Err() == nil {
				sampler.log("metric retention failed", "error_code", "METRIC_RETENTION_FAILED", "error", err)
			}
			nextRetention = time.Now().UTC().Add(retentionInterval)
		}
		timer := time.NewTimer(sampler.jitteredInterval())
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
		}
	}
}

func (sampler *Sampler) compact(ctx context.Context) (int64, error) {
	if sampler.config.Retention == nil {
		return 0, nil
	}
	return sampler.config.Retention.CompactDefault(ctx, time.Now().UTC())
}

func (sampler *Sampler) eligibleServices(ctx context.Context) ([]domain.ServiceInstance, error) {
	instances, err := sampler.config.Runtime.ListActive(ctx)
	if err != nil {
		return nil, fmt.Errorf("list metric runtime instances: %w", err)
	}
	result := make([]domain.ServiceInstance, 0)
	for _, instance := range instances {
		services, err := sampler.config.Runtime.ListServices(ctx, instance.ID)
		if err != nil {
			return nil, fmt.Errorf("list metric services: %w", err)
		}
		for _, service := range services {
			if service.ProcessMode == domain.ProcessDaemon && (service.State == domain.ServiceReady || service.State == domain.ServiceDegraded) {
				result = append(result, service)
			}
		}
	}
	return result, nil
}

func (sampler *Sampler) observeServices(ctx context.Context, services []domain.ServiceInstance) []Sample {
	jobs := make(chan domain.ServiceInstance)
	results := make(chan Sample, len(services))
	var workers sync.WaitGroup
	for index := 0; index < sampler.config.Workers; index++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for service := range jobs {
				results <- sampler.observeOne(ctx, service)
			}
		}()
	}
	go func() {
		for _, service := range services {
			jobs <- service
		}
		close(jobs)
		workers.Wait()
		close(results)
	}()
	samples := make([]Sample, 0, len(services))
	for sample := range results {
		samples = append(samples, sample)
	}
	return samples
}

func (sampler *Sampler) observeOne(ctx context.Context, service domain.ServiceInstance) Sample {
	probeContext, cancel := context.WithTimeout(ctx, sampler.config.SampleTimeout)
	defer cancel()
	if service.Driver == domain.DriverCompose {
		return sampler.config.Compose.Observe(probeContext, service, sampler.config.Interval)
	}
	return sampler.config.Process.Observe(probeContext, service, sampler.config.Interval)
}

func (sampler *Sampler) jitteredInterval() time.Duration {
	var value [1]byte
	if _, err := rand.Read(value[:]); err != nil {
		return sampler.config.Interval
	}
	percent := float64(int(value[0])-128) / 1280
	return sampler.config.Interval + time.Duration(float64(sampler.config.Interval)*percent)
}

func (sampler *Sampler) log(message string, arguments ...any) {
	if sampler.config.Logger != nil {
		sampler.config.Logger.Error(message, arguments...)
	}
}

func applySamplerDefaults(config *SamplerConfig) {
	if config.Interval == 0 {
		config.Interval = DefaultInterval
	}
	if config.Workers == 0 {
		config.Workers = DefaultWorkers
	}
	if config.QueueCapacity == 0 {
		config.QueueCapacity = DefaultQueueCapacity
	}
	if config.SampleTimeout == 0 {
		config.SampleTimeout = DefaultSampleTimeout
	}
}
