package ports

import (
	"context"
	"errors"
	"io"
	"reflect"
	"sync"
	"testing"
	"time"

	"stackpilot/internal/domain"
)

const (
	testWorkspaceID = domain.WorkspaceID("ws_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	testOperationID = domain.OperationID("op_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	testDigest      = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

func TestPlannerUsesDocumentedCandidatePriority(t *testing.T) {
	probe := &fakeProber{blocked: map[int]bool{4100: true, 4200: true}}
	store := &fakeStore{}
	planner := newTestPlanner(t, store, probe)
	preferred, fallback := 4300, Range{Start: 4400, End: 4401}
	input := testInput("auto", &preferred, &fallback)
	input.RequestOverrides["web"] = 4100
	input.WorkspaceOverride["web"] = 4200
	input.Sticky["web"] = 4301
	plan, err := planner.Plan(context.Background(), input)
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	defer plan.Close()
	assignment := plan.Assignments["web"]
	if assignment.Port != 4301 || assignment.Source != "sticky" || !assignment.Replaced || *assignment.ConflictPort != 4300 {
		t.Fatalf("assignment = %#v", assignment)
	}
	if got := probe.attempts; !reflect.DeepEqual(got, []int{4100, 4200, 4301}) {
		t.Fatalf("probe attempts = %#v", got)
	}
}

func TestPlannerEnforcesStrictAndOverrideOnly(t *testing.T) {
	tests := []struct {
		name       string
		policy     string
		request    map[string]int
		workspace  map[string]int
		blocked    map[int]bool
		wantPort   int
		wantSource string
		wantError  error
	}{
		{name: "strict ignores workspace and fallback", policy: "strict", workspace: map[string]int{"web": 4200}, wantPort: 4300, wantSource: "preferred"},
		{name: "strict request wins", policy: "strict", request: map[string]int{"web": 4100}, wantPort: 4100, wantSource: "request"},
		{name: "override only workspace", policy: "override-only", workspace: map[string]int{"web": 4200}, wantPort: 4200, wantSource: "workspace"},
		{name: "override only exhausted", policy: "override-only", wantError: ErrExhausted},
		{name: "strict occupied", policy: "strict", blocked: map[int]bool{4300: true}, wantError: ErrExhausted},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			preferred, fallback := 4300, Range{Start: 4400, End: 4401}
			input := testInput(test.policy, &preferred, &fallback)
			input.RequestOverrides, input.WorkspaceOverride = test.request, test.workspace
			plan, err := newTestPlanner(t, &fakeStore{}, &fakeProber{blocked: test.blocked}).Plan(context.Background(), input)
			if !errors.Is(err, test.wantError) {
				t.Fatalf("Plan() error = %v, want %v", err, test.wantError)
			}
			if err == nil {
				defer plan.Close()
				if got := plan.Assignments["web"]; got.Port != test.wantPort || got.Source != test.wantSource {
					t.Fatalf("assignment = %#v", got)
				}
			}
		})
	}
}

func TestPlannerSkipsActiveLeaseAndPreventsDuplicateWithinPlan(t *testing.T) {
	store := &fakeStore{active: []Lease{{Protocol: "tcp", Host: "127.0.0.1", Port: 4300, State: LeaseBound}}}
	probe := &fakeProber{}
	planner := newTestPlanner(t, store, probe)
	preferred, fallback := 4300, Range{Start: 4400, End: 4401}
	input := testInput("auto", &preferred, &fallback)
	input.Requirements["api"] = Requirement{Protocol: "tcp", Host: "127.0.0.1", Preferred: &preferred, Fallback: &fallback, ConflictPolicy: "auto"}
	plan, err := planner.Plan(context.Background(), input)
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	defer plan.Close()
	if got := []int{plan.Assignments["api"].Port, plan.Assignments["web"].Port}; !reflect.DeepEqual(got, []int{4400, 4401}) {
		t.Fatalf("planned ports = %#v", got)
	}
}

func TestPlanOwnsAndConcurrentlyReleasesProbes(t *testing.T) {
	preferred, fallback := 4300, Range{Start: 4400, End: 4401}
	input := testInput("auto", &preferred, &fallback)
	input.Requirements["api"] = Requirement{Protocol: "tcp", Host: "127.0.0.1", Preferred: intPointer(4301), ConflictPolicy: "auto"}
	probe := &fakeProber{}
	plan, err := newTestPlanner(t, &fakeStore{}, probe).Plan(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	for _, logicalName := range []string{"api", "web"} {
		wait.Add(1)
		go func() { defer wait.Done(); _ = plan.ReleaseProbe(logicalName) }()
	}
	wait.Wait()
	if err := plan.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if probe.closedCount() != 2 {
		t.Fatalf("closed probes = %d, want 2", probe.closedCount())
	}
}

func TestPlannerRejectsUnknownOverride(t *testing.T) {
	preferred := 4300
	input := testInput("auto", &preferred, nil)
	input.RequestOverrides["unknown"] = 4500
	_, err := newTestPlanner(t, &fakeStore{}, &fakeProber{}).Plan(context.Background(), input)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Plan() error = %v", err)
	}
}

func TestPlannerIgnoresPersistedPreferencesRemovedFromManifest(t *testing.T) {
	preferred := 4300
	input := testInput("auto", &preferred, nil)
	input.WorkspaceOverride["removed-workspace-port"] = 4500
	input.Sticky["removed-sticky-port"] = 4600

	plan, err := newTestPlanner(t, &fakeStore{}, &fakeProber{}).Plan(context.Background(), input)
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	defer plan.Close()
	if assignment := plan.Assignments["web"]; assignment.Port != preferred || assignment.Source != "preferred" {
		t.Fatalf("assignment = %#v", assignment)
	}
}

func TestPlannerAllowsSystemWithoutDeclaredPorts(t *testing.T) {
	store := &fakeStore{}
	input := testInput("auto", nil, nil)
	input.Requirements = map[string]Requirement{}
	plan, err := newTestPlanner(t, store, &fakeProber{}).Plan(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Close()
	if len(plan.Assignments) != 0 || plan.ID == "" || store.calls != 0 {
		t.Fatalf("empty port plan = %+v, store calls = %d", plan, store.calls)
	}
}

func testInput(policy string, preferred *int, fallback *Range) Input {
	return Input{
		WorkspaceID: testWorkspaceID, OperationID: testOperationID, ManifestDigest: testDigest,
		Requirements:     map[string]Requirement{"web": {Protocol: "tcp", Host: "127.0.0.1", Preferred: preferred, Fallback: fallback, ConflictPolicy: policy}},
		RequestOverrides: make(map[string]int), WorkspaceOverride: make(map[string]int), Sticky: make(map[string]int),
	}
}

func newTestPlanner(t *testing.T, store ReservationStore, probe *fakeProber) *Planner {
	t.Helper()
	planner, err := NewPlanner(Config{
		Store: store, TTL: time.Minute,
		Now: func() time.Time { return time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC) }, Probe: probe.open,
	})
	if err != nil {
		t.Fatal(err)
	}
	return planner
}

type fakeStore struct {
	active   []Lease
	reserved []Lease
	calls    int
}

func (store *fakeStore) Reserve(_ context.Context, _ Reservation, selectLeases SelectLeases) error {
	store.calls++
	leases, err := selectLeases(append([]Lease(nil), store.active...))
	store.reserved = append([]Lease(nil), leases...)
	return err
}

type fakeProber struct {
	mutex    sync.Mutex
	blocked  map[int]bool
	attempts []int
	probes   []*fakeProbe
}

func (prober *fakeProber) open(_ context.Context, _ string, port int) (io.Closer, error) {
	prober.mutex.Lock()
	defer prober.mutex.Unlock()
	prober.attempts = append(prober.attempts, port)
	if prober.blocked[port] {
		return nil, errors.New("occupied")
	}
	probe := &fakeProbe{}
	prober.probes = append(prober.probes, probe)
	return probe, nil
}

func (prober *fakeProber) closedCount() int {
	prober.mutex.Lock()
	defer prober.mutex.Unlock()
	count := 0
	for _, probe := range prober.probes {
		if probe.closed {
			count++
		}
	}
	return count
}

type fakeProbe struct{ closed bool }

func (probe *fakeProbe) Close() error { probe.closed = true; return nil }

func intPointer(value int) *int { return &value }
