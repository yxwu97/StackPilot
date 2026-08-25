package orchestrator

import (
	"sort"
	"sync"

	"stackpilot/internal/manifest"
)

// FailurePolicy is the effective policy for one immutable start attempt.
type FailurePolicy struct {
	FailFast          bool
	CleanupOnFailure  bool
	KeepReadyServices bool
}

// ResolveFailurePolicy applies safe request overrides to defaulted manifest policies.
func ResolveFailurePolicy(policies manifest.Policies, override FailurePolicyOverride) FailurePolicy {
	result := FailurePolicy{
		FailFast:          boolValue(policies.FailFast, true),
		CleanupOnFailure:  boolValue(policies.CleanupOnFailure, false),
		KeepReadyServices: boolValue(policies.KeepReadyServices, true),
	}
	applyBoolOverride(&result.FailFast, override.FailFast)
	applyBoolOverride(&result.CleanupOnFailure, override.CleanupOnFailure)
	applyBoolOverride(&result.KeepReadyServices, override.KeepReadyServices)
	return result
}

// CompensationLayers returns created services to stop in dependency-safe order.
func (graph *DAG) CompensationLayers(policy FailurePolicy, created map[string]bool, failed []string) [][]string {
	if !policy.CleanupOnFailure {
		return nil
	}
	selected := selectCompensationServices(graph, policy, created, failed)
	return filterLayerSet(graph.ReverseLayers(), selected)
}

// ServiceFailure is one stable service-scoped failure report.
type ServiceFailure struct {
	ServiceID string
	ErrorCode string
	Cause     error
}

// FailureReport contains the first observed failure and all other concurrent failures.
type FailureReport struct {
	Primary    ServiceFailure
	Concurrent []ServiceFailure
}

// FailureCollector safely aggregates failures from a bounded concurrent layer.
type FailureCollector struct {
	mutex    sync.Mutex
	failures []ServiceFailure
	seen     map[string]bool
}

// Add records the first failure for a service and preserves first-observed primary ordering.
func (collector *FailureCollector) Add(failure ServiceFailure) {
	collector.mutex.Lock()
	defer collector.mutex.Unlock()
	if collector.seen == nil {
		collector.seen = make(map[string]bool)
	}
	if collector.seen[failure.ServiceID] {
		return
	}
	collector.seen[failure.ServiceID] = true
	collector.failures = append(collector.failures, failure)
}

// Report returns a stable snapshot without exposing collector storage.
func (collector *FailureCollector) Report() (FailureReport, bool) {
	collector.mutex.Lock()
	defer collector.mutex.Unlock()
	if len(collector.failures) == 0 {
		return FailureReport{}, false
	}
	result := FailureReport{Primary: collector.failures[0]}
	result.Concurrent = append([]ServiceFailure(nil), collector.failures[1:]...)
	sort.Slice(result.Concurrent, func(i, j int) bool {
		return result.Concurrent[i].ServiceID < result.Concurrent[j].ServiceID
	})
	return result, true
}

func selectCompensationServices(graph *DAG, policy FailurePolicy, created map[string]bool, failed []string) map[string]bool {
	selected := make(map[string]bool)
	if !policy.KeepReadyServices {
		for serviceID, exists := range created {
			selected[serviceID] = exists
		}
		return selected
	}
	for _, failedService := range failed {
		closure, err := graph.DownstreamClosure(failedService)
		if err != nil {
			continue
		}
		for _, serviceID := range closure {
			selected[serviceID] = created[serviceID]
		}
	}
	return selected
}

func filterLayerSet(layers [][]string, selected map[string]bool) [][]string {
	result := make([][]string, 0, len(layers))
	for _, layer := range layers {
		filtered := make([]string, 0, len(layer))
		for _, serviceID := range layer {
			if selected[serviceID] {
				filtered = append(filtered, serviceID)
			}
		}
		if len(filtered) > 0 {
			result = append(result, filtered)
		}
	}
	return result
}

func boolValue(value *bool, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return *value
}

func applyBoolOverride(target *bool, override *bool) {
	if override != nil {
		*target = *override
	}
}
