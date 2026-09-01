package metrics

import (
	"context"
	"errors"
	"testing"
	"time"

	"stackpilot/internal/domain"
	base "stackpilot/internal/driver"
	"stackpilot/internal/driver/compose"
	processdriver "stackpilot/internal/driver/process"
)

func TestSamplerCollectsEligibleProcessAndComposeServices(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	processObserver := samplerProcessObserver{value: base.ResourceObservation{ObservedAt: now, CPUTotalMillis: 100, MemoryBytes: 1024, ActiveProcesses: 2}}
	processSource, _ := NewProcessSource(processObserver, 4)
	composeSource, _ := NewComposeSource(samplerComposeObserver{value: compose.ResourceObservation{
		ObservedAt: now, CPUPercent: 2, MemoryBytes: 2048,
		Containers: []compose.ContainerResourceObservation{{ID: "aaaaaaaaaaaa", ComposeService: "database", CPUPercent: 2, MemoryBytes: 2048}},
	}})
	instanceID := domain.SystemInstanceID("si_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	identity := domain.ProcessIdentity{PID: 42}
	runtimeSource := samplerRuntimeSource{
		instances: []domain.SystemInstance{{ID: instanceID}},
		services: []domain.ServiceInstance{
			{ID: "svi_01ARZ3NDEKTSV4RRFFQ69G5FAV", SystemInstanceID: instanceID, Driver: domain.DriverProcess, ProcessMode: domain.ProcessDaemon, State: domain.ServiceReady, Identity: &identity},
			{ID: "svi_01ARZ3NDEKTSV4RRFFQ69G5FAW", SystemInstanceID: instanceID, Driver: domain.DriverCompose, ProcessMode: domain.ProcessDaemon, State: domain.ServiceDegraded, ComposeIdentity: "encoded"},
			{ID: "svi_01ARZ3NDEKTSV4RRFFQ69G5FAX", SystemInstanceID: instanceID, Driver: domain.DriverProcess, ProcessMode: domain.ProcessOneshot, State: domain.ServiceReady, Identity: &identity},
			{ID: "svi_01ARZ3NDEKTSV4RRFFQ69G5FAY", SystemInstanceID: instanceID, Driver: domain.DriverProcess, ProcessMode: domain.ProcessDaemon, State: domain.ServiceStopped, Identity: &identity},
		},
	}
	store := &samplerStore{}
	sampler, err := NewSampler(SamplerConfig{Runtime: runtimeSource, Store: store, Process: processSource, Compose: composeSource})
	if err != nil {
		t.Fatalf("NewSampler() error = %v", err)
	}
	if err := sampler.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if len(store.samples) != 2 || store.samples[0].Status != domain.MetricAvailable || store.samples[1].Status != domain.MetricAvailable {
		t.Fatalf("stored samples = %#v", store.samples)
	}
}

func TestSourcesReturnExplicitUnavailableStatus(t *testing.T) {
	processSource, _ := NewProcessSource(samplerProcessObserver{err: errors.New("pipe unavailable")}, 1)
	processSample := processSource.Observe(context.Background(), domain.ServiceInstance{ID: "svi_01ARZ3NDEKTSV4RRFFQ69G5FAV", Identity: &domain.ProcessIdentity{}}, DefaultInterval)
	if processSample.Status != domain.MetricUnavailable || processSample.ReasonCode != "SUPERVISOR_UNAVAILABLE" {
		t.Fatalf("process unavailable sample = %#v", processSample)
	}
	composeSource, _ := NewComposeSource(samplerComposeObserver{err: compose.ErrProjectIdentityMismatch})
	composeSample := composeSource.Observe(context.Background(), domain.ServiceInstance{ID: "svi_01ARZ3NDEKTSV4RRFFQ69G5FAW", ComposeIdentity: "bad"}, DefaultInterval)
	if composeSample.Status != domain.MetricUnavailable || composeSample.ReasonCode != "COMPOSE_IDENTITY_MISMATCH" {
		t.Fatalf("Compose unavailable sample = %#v", composeSample)
	}
}

func TestSourcesDistinguishUnsupportedFromUnavailable(t *testing.T) {
	processSource, _ := NewProcessSource(samplerProcessObserver{err: processdriver.ErrResourceUnsupported}, 1)
	sample := processSource.Observe(context.Background(), domain.ServiceInstance{ID: "svi_01ARZ3NDEKTSV4RRFFQ69G5FAV", Identity: &domain.ProcessIdentity{}}, DefaultInterval)
	if sample.Status != domain.MetricUnsupported || sample.ReasonCode != "SUPERVISOR_PROTOCOL_UNSUPPORTED" {
		t.Fatalf("unsupported process sample = %#v", sample)
	}
}

func TestSamplerBoundsQueueAndTimesOutSources(t *testing.T) {
	processSource, _ := NewProcessSource(blockingProcessObserver{}, 1)
	composeSource, _ := NewComposeSource(samplerComposeObserver{})
	instanceID := domain.SystemInstanceID("si_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	identity := domain.ProcessIdentity{PID: 42}
	services := make([]domain.ServiceInstance, 0, 3)
	for _, id := range []domain.ServiceInstanceID{
		"svi_01ARZ3NDEKTSV4RRFFQ69G5FAV", "svi_01ARZ3NDEKTSV4RRFFQ69G5FAW", "svi_01ARZ3NDEKTSV4RRFFQ69G5FAX",
	} {
		services = append(services, domain.ServiceInstance{
			ID: id, SystemInstanceID: instanceID, Driver: domain.DriverProcess,
			ProcessMode: domain.ProcessDaemon, State: domain.ServiceReady, Identity: &identity,
		})
	}
	store := &samplerStore{}
	sampler, err := NewSampler(SamplerConfig{
		Runtime: samplerRuntimeSource{instances: []domain.SystemInstance{{ID: instanceID}}, services: services},
		Store:   store, Process: processSource, Compose: composeSource,
		Interval: MinimumInterval, Workers: 2, QueueCapacity: 2, SampleTimeout: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewSampler() error = %v", err)
	}
	started := time.Now()
	if err := sampler.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("RunOnce() elapsed = %v, want bounded timeout", elapsed)
	}
	if len(store.samples) != 2 {
		t.Fatalf("stored samples = %d, want queue capacity 2", len(store.samples))
	}
	for _, sample := range store.samples {
		if sample.Status != domain.MetricUnavailable || sample.ReasonCode != "SUPERVISOR_TIMEOUT" {
			t.Fatalf("timed out sample = %#v", sample)
		}
	}
}

func TestSamplerStartHasSingleContextOwnerAndExits(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	processSource, _ := NewProcessSource(samplerProcessObserver{value: base.ResourceObservation{
		ObservedAt: now, CPUTotalMillis: 1, MemoryBytes: 1, ActiveProcesses: 1,
	}}, 1)
	composeSource, _ := NewComposeSource(samplerComposeObserver{})
	instanceID := domain.SystemInstanceID("si_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	identity := domain.ProcessIdentity{PID: 42}
	store := &signalingSamplerStore{saved: make(chan struct{}, 1)}
	sampler, err := NewSampler(SamplerConfig{
		Runtime: samplerRuntimeSource{
			instances: []domain.SystemInstance{{ID: instanceID}},
			services: []domain.ServiceInstance{{
				ID: "svi_01ARZ3NDEKTSV4RRFFQ69G5FAV", SystemInstanceID: instanceID,
				Driver: domain.DriverProcess, ProcessMode: domain.ProcessDaemon, State: domain.ServiceReady, Identity: &identity,
			}},
		},
		Store: store, Process: processSource, Compose: composeSource, Interval: MinimumInterval,
	})
	if err != nil {
		t.Fatalf("NewSampler() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := sampler.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	select {
	case <-store.saved:
	case <-time.After(time.Second):
		t.Fatal("sampler did not complete its initial cycle")
	}
	if err := sampler.Start(ctx); err == nil {
		t.Fatal("second Start() unexpectedly succeeded")
	}
	cancel()
	done := make(chan struct{})
	go func() { sampler.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Wait() did not observe context-owned shutdown")
	}
}

func TestSamplerPropagatesRuntimeAndStoreFailures(t *testing.T) {
	processSource, _ := NewProcessSource(samplerProcessObserver{}, 1)
	composeSource, _ := NewComposeSource(samplerComposeObserver{})
	want := errors.New("fixture failure")
	sampler, _ := NewSampler(SamplerConfig{
		Runtime: samplerRuntimeSource{err: want}, Store: &samplerStore{},
		Process: processSource, Compose: composeSource,
	})
	if err := sampler.RunOnce(context.Background()); !errors.Is(err, want) {
		t.Fatalf("RunOnce(runtime failure) error = %v", err)
	}

	instanceID := domain.SystemInstanceID("si_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	identity := domain.ProcessIdentity{PID: 42}
	sampler, _ = NewSampler(SamplerConfig{
		Runtime: samplerRuntimeSource{
			instances: []domain.SystemInstance{{ID: instanceID}},
			services: []domain.ServiceInstance{{
				ID: "svi_01ARZ3NDEKTSV4RRFFQ69G5FAV", SystemInstanceID: instanceID,
				Driver: domain.DriverProcess, ProcessMode: domain.ProcessDaemon, State: domain.ServiceReady, Identity: &identity,
			}},
		},
		Store: &samplerStore{err: want}, Process: processSource, Compose: composeSource,
	})
	if err := sampler.RunOnce(context.Background()); !errors.Is(err, want) {
		t.Fatalf("RunOnce(store failure) error = %v", err)
	}
}

type samplerProcessObserver struct {
	value base.ResourceObservation
	err   error
}

type blockingProcessObserver struct{}

func (blockingProcessObserver) ObserveResources(ctx context.Context, _ base.RuntimeIdentity) (base.ResourceObservation, error) {
	<-ctx.Done()
	return base.ResourceObservation{}, ctx.Err()
}

func (observer samplerProcessObserver) ObserveResources(context.Context, base.RuntimeIdentity) (base.ResourceObservation, error) {
	return observer.value, observer.err
}

type samplerComposeObserver struct {
	value compose.ResourceObservation
	err   error
}

func (observer samplerComposeObserver) ObserveResources(context.Context, string) (compose.ResourceObservation, error) {
	return observer.value, observer.err
}

type samplerRuntimeSource struct {
	instances []domain.SystemInstance
	services  []domain.ServiceInstance
	err       error
}

func (source samplerRuntimeSource) ListActive(context.Context) ([]domain.SystemInstance, error) {
	return append([]domain.SystemInstance(nil), source.instances...), source.err
}

func (source samplerRuntimeSource) ListServices(context.Context, domain.SystemInstanceID) ([]domain.ServiceInstance, error) {
	return append([]domain.ServiceInstance(nil), source.services...), nil
}

type samplerStore struct {
	samples []Sample
	err     error
}

func (store *samplerStore) SaveBatch(_ context.Context, samples []Sample) error {
	store.samples = append(store.samples, samples...)
	return store.err
}

type signalingSamplerStore struct{ saved chan struct{} }

func (store *signalingSamplerStore) SaveBatch(context.Context, []Sample) error {
	select {
	case store.saved <- struct{}{}:
	default:
	}
	return nil
}
