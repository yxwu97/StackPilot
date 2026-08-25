package orchestrator_test

import (
	"errors"
	"reflect"
	"testing"

	"stackpilot/internal/manifest"
	"stackpilot/internal/orchestrator"
)

func TestResolveFailurePolicyUsesDefaultsAndOverrides(t *testing.T) {
	if got := orchestrator.ResolveFailurePolicy(manifest.Policies{}, orchestrator.FailurePolicyOverride{}); got != (orchestrator.FailurePolicy{FailFast: true, KeepReadyServices: true}) {
		t.Fatalf("default policy = %#v", got)
	}
	falseValue, trueValue := false, true
	got := orchestrator.ResolveFailurePolicy(manifest.Policies{FailFast: &trueValue}, orchestrator.FailurePolicyOverride{
		FailFast: &falseValue, CleanupOnFailure: &trueValue, KeepReadyServices: &falseValue,
	})
	want := orchestrator.FailurePolicy{FailFast: false, CleanupOnFailure: true, KeepReadyServices: false}
	if got != want {
		t.Fatalf("overridden policy = %#v, want %#v", got, want)
	}
}

func TestCompensationLayersRespectCleanupScope(t *testing.T) {
	graph, err := orchestrator.NewDAG(map[string]manifest.Service{
		"db": {}, "api": {DependsOn: map[string]string{"db": "ready"}},
		"web": {DependsOn: map[string]string{"api": "ready"}}, "metrics": {},
	})
	if err != nil {
		t.Fatal(err)
	}
	created := map[string]bool{"db": true, "api": true, "web": true, "metrics": true}
	keepUnrelated := orchestrator.FailurePolicy{CleanupOnFailure: true, KeepReadyServices: true}
	want := [][]string{{"web"}, {"api"}}
	if got := graph.CompensationLayers(keepUnrelated, created, []string{"api"}); !reflect.DeepEqual(got, want) {
		t.Fatalf("selective compensation = %#v, want %#v", got, want)
	}
	cleanupAll := orchestrator.FailurePolicy{CleanupOnFailure: true}
	want = [][]string{{"web"}, {"api"}, {"db", "metrics"}}
	if got := graph.CompensationLayers(cleanupAll, created, []string{"api"}); !reflect.DeepEqual(got, want) {
		t.Fatalf("full compensation = %#v, want %#v", got, want)
	}
	if got := graph.CompensationLayers(orchestrator.FailurePolicy{}, created, []string{"api"}); got != nil {
		t.Fatalf("disabled compensation = %#v, want nil", got)
	}
}

func TestFailureCollectorPreservesPrimaryAndSortsConcurrent(t *testing.T) {
	collector := &orchestrator.FailureCollector{}
	primary := errors.New("primary")
	collector.Add(orchestrator.ServiceFailure{ServiceID: "web", ErrorCode: "WEB_FAILED", Cause: primary})
	collector.Add(orchestrator.ServiceFailure{ServiceID: "worker", ErrorCode: "WORKER_FAILED"})
	collector.Add(orchestrator.ServiceFailure{ServiceID: "api", ErrorCode: "API_FAILED"})
	collector.Add(orchestrator.ServiceFailure{ServiceID: "web", ErrorCode: "DUPLICATE"})
	report, ok := collector.Report()
	if !ok || report.Primary.ServiceID != "web" || !errors.Is(report.Primary.Cause, primary) {
		t.Fatalf("primary report = %#v, %v", report.Primary, ok)
	}
	if got := []string{report.Concurrent[0].ServiceID, report.Concurrent[1].ServiceID}; !reflect.DeepEqual(got, []string{"api", "worker"}) {
		t.Fatalf("concurrent services = %#v", got)
	}
}
