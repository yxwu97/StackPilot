package ports

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"

	"stackpilot/internal/domain"
)

const defaultReservationTTL = 60 * time.Second

type probeFunc func(context.Context, string, int) (io.Closer, error)

// Config provides the durable store and bounded reservation behavior.
type Config struct {
	Store ReservationStore
	TTL   time.Duration
	Now   func() time.Time
	Probe probeFunc
}

// Planner coordinates deterministic candidates, OS probes, and SQLite reservations.
type Planner struct {
	store ReservationStore
	ttl   time.Duration
	now   func() time.Time
	probe probeFunc
}

// NewPlanner validates and constructs a whole-system port planner.
func NewPlanner(config Config) (*Planner, error) {
	if config.Store == nil {
		return nil, fmt.Errorf("port reservation store is required")
	}
	if config.TTL == 0 {
		config.TTL = defaultReservationTTL
	}
	if config.TTL <= 0 || config.TTL > 10*time.Minute {
		return nil, fmt.Errorf("%w: reservation TTL", ErrInvalidInput)
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.Probe == nil {
		config.Probe = listenTCPProbe
	}
	return &Planner{store: config.Store, ttl: config.TTL, now: config.Now, probe: config.Probe}, nil
}

// Plan validates, probes, and atomically reserves every logical endpoint.
func (planner *Planner) Plan(ctx context.Context, input Input) (*Plan, error) {
	if err := validateInput(input); err != nil {
		return nil, err
	}
	now := planner.now().UTC()
	planID, err := domain.NewPortPlanID(now, rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate port plan ID: %w", err)
	}
	result := &Plan{ID: planID, WorkspaceID: input.WorkspaceID, Assignments: make(map[string]Assignment), ExpiresAt: now.Add(planner.ttl), probes: make(map[string]io.Closer)}
	if len(input.Requirements) == 0 {
		return result, nil
	}
	reservation := Reservation{PlanID: planID, WorkspaceID: input.WorkspaceID, OperationID: input.OperationID, ManifestDigest: input.ManifestDigest, Now: now, ExpiresAt: result.ExpiresAt}
	err = planner.store.Reserve(ctx, reservation, func(active []Lease) ([]Lease, error) {
		return planner.selectLeases(ctx, input, reservation, active, result)
	})
	if err != nil {
		return nil, errors.Join(err, result.Close())
	}
	return result, nil
}

func (planner *Planner) selectLeases(ctx context.Context, input Input, reservation Reservation, active []Lease, plan *Plan) ([]Lease, error) {
	occupied := activeEndpoints(active)
	logicalNames := sortedRequirementNames(input.Requirements)
	leases := make([]Lease, 0, len(logicalNames))
	for _, logicalName := range logicalNames {
		assignment, probe, err := planner.selectAssignment(ctx, input, logicalName, occupied)
		if err != nil {
			return nil, err
		}
		leaseID, err := domain.NewPortLeaseID(reservation.Now, rand.Reader)
		if err != nil {
			_ = probe.Close()
			return nil, fmt.Errorf("generate port lease ID: %w", err)
		}
		assignment.LeaseID = leaseID
		plan.Assignments[logicalName], plan.probes[logicalName] = assignment, probe
		occupied[endpointKey(assignment.Protocol, assignment.Host, assignment.Port)] = true
		leases = append(leases, leaseFromAssignment(reservation, assignment))
	}
	return leases, nil
}

func (planner *Planner) selectAssignment(ctx context.Context, input Input, logicalName string, occupied map[string]bool) (Assignment, io.Closer, error) {
	requirement := input.Requirements[logicalName]
	for _, candidate := range candidates(input, requirement, logicalName) {
		key := endpointKey(requirement.Protocol, requirement.Host, candidate.port)
		if occupied[key] {
			continue
		}
		probe, err := planner.probe(ctx, requirement.Host, candidate.port)
		if err != nil {
			continue
		}
		assignment := assignmentFor(requirement, logicalName, candidate)
		return assignment, probe, nil
	}
	return Assignment{}, nil, fmt.Errorf("%w: %s", ErrExhausted, logicalName)
}

type candidate struct {
	port   int
	source string
}

func candidates(input Input, requirement Requirement, logicalName string) []candidate {
	result := make([]candidate, 0)
	seen := make(map[int]bool)
	appendCandidate := func(port int, source string) {
		if !seen[port] {
			seen[port] = true
			result = append(result, candidate{port: port, source: source})
		}
	}
	if port, exists := input.RequestOverrides[logicalName]; exists {
		appendCandidate(port, "request")
	}
	if requirement.ConflictPolicy != "strict" {
		if port, exists := input.WorkspaceOverride[logicalName]; exists {
			appendCandidate(port, "workspace")
		}
	}
	if requirement.ConflictPolicy == "auto" {
		appendAutomaticCandidates(&result, seen, input, requirement, logicalName)
	} else if requirement.ConflictPolicy == "strict" && requirement.Preferred != nil {
		appendCandidate(*requirement.Preferred, "preferred")
	}
	return result
}

func appendAutomaticCandidates(result *[]candidate, seen map[int]bool, input Input, requirement Requirement, logicalName string) {
	appendOne := func(port int, source string) {
		if !seen[port] {
			seen[port] = true
			*result = append(*result, candidate{port: port, source: source})
		}
	}
	if port, exists := input.Sticky[logicalName]; exists {
		appendOne(port, "sticky")
	}
	if requirement.Preferred != nil {
		appendOne(*requirement.Preferred, "preferred")
	}
	if requirement.Fallback != nil {
		for port := requirement.Fallback.Start; port <= requirement.Fallback.End; port++ {
			appendOne(port, "fallback")
		}
	}
}

func assignmentFor(requirement Requirement, logicalName string, selected candidate) Assignment {
	assignment := Assignment{LogicalName: logicalName, Protocol: requirement.Protocol, Host: requirement.Host, Port: selected.port, Source: selected.source}
	if requirement.Preferred != nil && selected.port != *requirement.Preferred {
		conflict := *requirement.Preferred
		assignment.Replaced, assignment.ConflictPort = true, &conflict
	}
	return assignment
}

func leaseFromAssignment(reservation Reservation, assignment Assignment) Lease {
	return Lease{
		ID: assignment.LeaseID, PlanID: reservation.PlanID, WorkspaceID: reservation.WorkspaceID,
		OperationID: reservation.OperationID, ManifestDigest: reservation.ManifestDigest,
		LogicalName: assignment.LogicalName, Protocol: assignment.Protocol, Host: assignment.Host,
		Port: assignment.Port, State: LeaseReserved, ExpiresAt: reservation.ExpiresAt,
		CreatedAt: reservation.Now, UpdatedAt: reservation.Now,
	}
}

func validateInput(input Input) error {
	if _, err := domain.ParseWorkspaceID(input.WorkspaceID.String()); err != nil {
		return fmt.Errorf("%w: workspace ID", ErrInvalidInput)
	}
	if _, err := domain.ParseOperationID(input.OperationID.String()); err != nil || len(input.ManifestDigest) != 64 {
		return fmt.Errorf("%w: operation, manifest, or requirements", ErrInvalidInput)
	}
	for logicalName, requirement := range input.Requirements {
		if err := validateRequirement(logicalName, requirement); err != nil {
			return err
		}
	}
	return validateOverrideMaps(input)
}

func validateRequirement(logicalName string, requirement Requirement) error {
	if requirement.LogicalName != "" && requirement.LogicalName != logicalName {
		return fmt.Errorf("%w: logical name mismatch", ErrInvalidInput)
	}
	if _, err := domain.ParseServiceID(logicalName); err != nil || requirement.Protocol != "tcp" || requirement.Host != "127.0.0.1" {
		return fmt.Errorf("%w: requirement %s", ErrInvalidInput, logicalName)
	}
	if requirement.ConflictPolicy != "auto" && requirement.ConflictPolicy != "strict" && requirement.ConflictPolicy != "override-only" {
		return fmt.Errorf("%w: conflict policy", ErrInvalidInput)
	}
	if requirement.Preferred != nil && !validPort(*requirement.Preferred) {
		return fmt.Errorf("%w: preferred", ErrInvalidInput)
	}
	if requirement.Fallback != nil && (!validPort(requirement.Fallback.Start) || !validPort(requirement.Fallback.End) || requirement.Fallback.Start > requirement.Fallback.End || requirement.Fallback.End-requirement.Fallback.Start+1 > 2000) {
		return fmt.Errorf("%w: fallback", ErrInvalidInput)
	}
	return nil
}

func validateOverrideMaps(input Input) error {
	for logicalName, port := range input.RequestOverrides {
		if _, exists := input.Requirements[logicalName]; !exists || !validPort(port) {
			return fmt.Errorf("%w: override %s", ErrInvalidInput, logicalName)
		}
	}
	for _, values := range []map[string]int{input.WorkspaceOverride, input.Sticky} {
		for logicalName, port := range values {
			if _, exists := input.Requirements[logicalName]; exists && !validPort(port) {
				return fmt.Errorf("%w: preference %s", ErrInvalidInput, logicalName)
			}
		}
	}
	return nil
}

func validPort(port int) bool { return port >= 1024 && port <= 65535 }

func sortedRequirementNames(requirements map[string]Requirement) []string {
	result := make([]string, 0, len(requirements))
	for logicalName := range requirements {
		result = append(result, logicalName)
	}
	sort.Strings(result)
	return result
}

func activeEndpoints(active []Lease) map[string]bool {
	result := make(map[string]bool, len(active))
	for _, lease := range active {
		result[endpointKey(lease.Protocol, lease.Host, lease.Port)] = true
	}
	return result
}

func endpointKey(protocol, host string, port int) string {
	return fmt.Sprintf("%s|%s|%d", protocol, host, port)
}
