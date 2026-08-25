package orchestrator_test

import (
	"errors"
	"reflect"
	"testing"

	"stackpilot/internal/domain"
	"stackpilot/internal/manifest"
	"stackpilot/internal/orchestrator"
)

func TestDAGBuildsDeterministicLayersAndReverse(t *testing.T) {
	services := map[string]manifest.Service{
		"web":    {DependsOn: map[string]string{"api": "ready"}},
		"worker": {DependsOn: map[string]string{"db": "ready"}},
		"api":    {DependsOn: map[string]string{"db": "ready"}},
		"db":     {},
		"cache":  {},
	}
	graph, err := orchestrator.NewDAG(services)
	if err != nil {
		t.Fatalf("NewDAG() error = %v", err)
	}
	want := [][]string{{"cache", "db"}, {"api", "worker"}, {"web"}}
	if got := graph.Layers(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Layers() = %#v, want %#v", got, want)
	}
	wantReverse := [][]string{{"web"}, {"api", "worker"}, {"cache", "db"}}
	if got := graph.ReverseLayers(); !reflect.DeepEqual(got, wantReverse) {
		t.Fatalf("ReverseLayers() = %#v, want %#v", got, wantReverse)
	}
}

func TestDAGDownstreamClosureHandlesDiamond(t *testing.T) {
	services := map[string]manifest.Service{
		"root":  {},
		"left":  {DependsOn: map[string]string{"root": "ready"}},
		"right": {DependsOn: map[string]string{"root": "ready"}},
		"leaf":  {DependsOn: map[string]string{"left": "ready", "right": "ready"}},
		"other": {},
	}
	graph, err := orchestrator.NewDAG(services)
	if err != nil {
		t.Fatalf("NewDAG() error = %v", err)
	}
	want := []string{"root", "left", "right", "leaf"}
	if got, err := graph.DownstreamClosure("root"); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("DownstreamClosure(root) = %#v, %v; want %#v", got, err, want)
	}
	if got, err := graph.DownstreamClosure("left"); err != nil || !reflect.DeepEqual(got, []string{"left", "leaf"}) {
		t.Fatalf("DownstreamClosure(left) = %#v, %v", got, err)
	}
}

func TestDAGChecksDependencyConditions(t *testing.T) {
	services := map[string]manifest.Service{
		"daemon": {}, "job": {},
		"app": {DependsOn: map[string]string{"daemon": "ready", "job": "completed"}},
	}
	graph, err := orchestrator.NewDAG(services)
	if err != nil {
		t.Fatalf("NewDAG() error = %v", err)
	}
	states := map[string]domain.ServiceState{"daemon": domain.ServiceReady, "job": domain.ServiceCompleted}
	if satisfied, err := graph.DependenciesSatisfied("app", states); err != nil || !satisfied {
		t.Fatalf("DependenciesSatisfied() = %v, %v", satisfied, err)
	}
	states["job"] = domain.ServiceReady
	if satisfied, err := graph.DependenciesSatisfied("app", states); err != nil || satisfied {
		t.Fatalf("DependenciesSatisfied() = %v, %v; want false", satisfied, err)
	}
}

func TestDAGRejectsInvalidGraphsDefensively(t *testing.T) {
	tests := []map[string]manifest.Service{
		{"app": {DependsOn: map[string]string{"missing": "ready"}}},
		{"app": {DependsOn: map[string]string{"app": "ready"}}},
		{"app": {DependsOn: map[string]string{"db": "started"}}, "db": {}},
		{"a": {DependsOn: map[string]string{"b": "ready"}}, "b": {DependsOn: map[string]string{"a": "ready"}}},
	}
	for _, services := range tests {
		if _, err := orchestrator.NewDAG(services); !errors.Is(err, orchestrator.ErrInvalidDependencyGraph) {
			t.Fatalf("NewDAG(%#v) error = %v", services, err)
		}
	}
}

func TestDAGReturnsIndependentCopies(t *testing.T) {
	graph, err := orchestrator.NewDAG(map[string]manifest.Service{"a": {}, "b": {}})
	if err != nil {
		t.Fatal(err)
	}
	first := graph.Layers()
	first[0][0] = "changed"
	if got := graph.Layers()[0][0]; got != "a" {
		t.Fatalf("Layers() exposed mutable state: %q", got)
	}
}
