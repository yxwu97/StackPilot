package orchestrator

import (
	"fmt"
	"sort"

	"stackpilot/internal/domain"
	"stackpilot/internal/manifest"
)

// DAG is an immutable, deterministic service dependency plan.
type DAG struct {
	dependencies map[string]map[string]domain.DependencyCondition
	downstream   map[string][]string
	layers       [][]string
}

// NewDAG validates services defensively and computes stable topological layers.
func NewDAG(services map[string]manifest.Service) (*DAG, error) {
	dependencies, downstream, err := dependencyMaps(services)
	if err != nil {
		return nil, err
	}
	layers, err := topologicalLayers(dependencies, downstream)
	if err != nil {
		return nil, err
	}
	return &DAG{dependencies: dependencies, downstream: downstream, layers: layers}, nil
}

// Layers returns dependency-first layers with stable service ordering.
func (graph *DAG) Layers() [][]string { return cloneLayers(graph.layers, false) }

// ReverseLayers returns dependent-first layers for system stop and compensation.
func (graph *DAG) ReverseLayers() [][]string { return cloneLayers(graph.layers, true) }

// DownstreamClosure returns the target and all transitive dependents in topological order.
func (graph *DAG) DownstreamClosure(target string) ([]string, error) {
	if _, exists := graph.dependencies[target]; !exists {
		return nil, fmt.Errorf("%w: unknown service %q", ErrInvalidDependencyGraph, target)
	}
	included := map[string]bool{target: true}
	for _, layer := range graph.layers {
		for _, serviceID := range layer {
			if hasIncludedDependency(graph.dependencies[serviceID], included) {
				included[serviceID] = true
			}
		}
	}
	return filterLayers(graph.layers, included), nil
}

// DependenciesSatisfied reports whether every declared dependency releases serviceID.
func (graph *DAG) DependenciesSatisfied(serviceID string, states map[string]domain.ServiceState) (bool, error) {
	dependencies, exists := graph.dependencies[serviceID]
	if !exists {
		return false, fmt.Errorf("%w: unknown service %q", ErrInvalidDependencyGraph, serviceID)
	}
	for dependency, condition := range dependencies {
		if !states[dependency].Satisfies(condition) {
			return false, nil
		}
	}
	return true, nil
}

func dependencyMaps(services map[string]manifest.Service) (map[string]map[string]domain.DependencyCondition, map[string][]string, error) {
	dependencies := make(map[string]map[string]domain.DependencyCondition, len(services))
	downstream := make(map[string][]string, len(services))
	for serviceID := range services {
		dependencies[serviceID] = make(map[string]domain.DependencyCondition)
		downstream[serviceID] = nil
	}
	for serviceID, service := range services {
		for dependency, rawCondition := range service.DependsOn {
			if _, exists := services[dependency]; !exists || dependency == serviceID {
				return nil, nil, fmt.Errorf("%w: %s depends on %s", ErrInvalidDependencyGraph, serviceID, dependency)
			}
			condition := domain.DependencyCondition(rawCondition)
			if !condition.Valid() {
				return nil, nil, fmt.Errorf("%w: invalid condition %q", ErrInvalidDependencyGraph, rawCondition)
			}
			dependencies[serviceID][dependency] = condition
			downstream[dependency] = append(downstream[dependency], serviceID)
		}
	}
	for serviceID := range downstream {
		sort.Strings(downstream[serviceID])
	}
	return dependencies, downstream, nil
}

func topologicalLayers(dependencies map[string]map[string]domain.DependencyCondition, downstream map[string][]string) ([][]string, error) {
	indegree := make(map[string]int, len(dependencies))
	ready := make([]string, 0, len(dependencies))
	for serviceID, values := range dependencies {
		indegree[serviceID] = len(values)
		if len(values) == 0 {
			ready = append(ready, serviceID)
		}
	}
	sort.Strings(ready)
	layers := make([][]string, 0)
	visited := 0
	for len(ready) > 0 {
		layer := append([]string(nil), ready...)
		layers = append(layers, layer)
		visited += len(layer)
		ready = releaseLayer(layer, downstream, indegree)
	}
	if visited != len(dependencies) {
		return nil, fmt.Errorf("%w: cycle detected", ErrInvalidDependencyGraph)
	}
	return layers, nil
}

func releaseLayer(layer []string, downstream map[string][]string, indegree map[string]int) []string {
	next := make([]string, 0)
	for _, serviceID := range layer {
		for _, dependent := range downstream[serviceID] {
			indegree[dependent]--
			if indegree[dependent] == 0 {
				next = append(next, dependent)
			}
		}
	}
	sort.Strings(next)
	return next
}

func cloneLayers(source [][]string, reverse bool) [][]string {
	result := make([][]string, len(source))
	for index := range source {
		sourceIndex := index
		if reverse {
			sourceIndex = len(source) - 1 - index
		}
		result[index] = append([]string(nil), source[sourceIndex]...)
	}
	return result
}

func hasIncludedDependency(dependencies map[string]domain.DependencyCondition, included map[string]bool) bool {
	for dependency := range dependencies {
		if included[dependency] {
			return true
		}
	}
	return false
}

func filterLayers(layers [][]string, included map[string]bool) []string {
	result := make([]string, 0, len(included))
	for _, layer := range layers {
		for _, serviceID := range layer {
			if included[serviceID] {
				result = append(result, serviceID)
			}
		}
	}
	return result
}
