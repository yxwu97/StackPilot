package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"stackpilot/internal/domain"
	"stackpilot/internal/ports"
)

func TestPortLeaseRepositoryReservesExpiresAndReusesEndpoint(t *testing.T) {
	database := openTestDatabase(t)
	seedRuntimeCatalog(t, database)
	repository, err := NewPortLeaseRepository(database)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	first := testReservation("pp_01ARZ3NDEKTSV4RRFFQ69G5FAV", now, now.Add(time.Minute))
	if err := repository.Reserve(context.Background(), first, reserveTestLease(first, "pl_01ARZ3NDEKTSV4RRFFQ69G5FAV", 32102)); err != nil {
		t.Fatalf("Reserve(first) error = %v", err)
	}
	if _, err := database.Exec(`UPDATE operations SET state='succeeded', started_at=?, finished_at=? WHERE id=?`,
		now, now.Add(time.Second), first.OperationID.String()); err != nil {
		t.Fatalf("finish first reservation Operation: %v", err)
	}
	second := testReservation("pp_01ARZ3NDEKTSV4RRFFQ69G5FAW", now.Add(2*time.Minute), now.Add(3*time.Minute))
	var activeSeen int
	err = repository.Reserve(context.Background(), second, func(active []ports.Lease) ([]ports.Lease, error) {
		activeSeen = len(active)
		return reserveTestLease(second, "pl_01ARZ3NDEKTSV4RRFFQ69G5FAW", 32102)(active)
	})
	if err != nil {
		t.Fatalf("Reserve(second) error = %v", err)
	}
	if activeSeen != 0 {
		t.Fatalf("active leases after expiry = %d, want 0", activeSeen)
	}
	var state string
	if err := database.QueryRow(`SELECT state FROM port_leases WHERE id = ?`, "pl_01ARZ3NDEKTSV4RRFFQ69G5FAV").Scan(&state); err != nil || state != "expired" {
		t.Fatalf("expired lease state = %q, %v", state, err)
	}
}

func TestPortLeaseRepositoryEnforcesActiveEndpointUniqueness(t *testing.T) {
	database := openTestDatabase(t)
	seedRuntimeCatalog(t, database)
	repository, _ := NewPortLeaseRepository(database)
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	first := testReservation("pp_01ARZ3NDEKTSV4RRFFQ69G5FAV", now, now.Add(time.Minute))
	if err := repository.Reserve(context.Background(), first, reserveTestLease(first, "pl_01ARZ3NDEKTSV4RRFFQ69G5FAV", 32102)); err != nil {
		t.Fatal(err)
	}
	second := testReservation("pp_01ARZ3NDEKTSV4RRFFQ69G5FAW", now, now.Add(time.Minute))
	err := repository.Reserve(context.Background(), second, reserveTestLease(second, "pl_01ARZ3NDEKTSV4RRFFQ69G5FAW", 32102))
	if !errors.Is(err, ports.ErrLeaseConflict) {
		t.Fatalf("conflicting Reserve() error = %v", err)
	}
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM port_leases`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("lease count after rollback = %d, %v", count, err)
	}
}

func TestPortLeaseRepositoryTransitionsBoundAndReleased(t *testing.T) {
	database := openTestDatabase(t)
	seedRuntimeCatalog(t, database)
	leaseRepository, _ := NewPortLeaseRepository(database)
	runtimeRepository, _ := NewRuntimeInstanceRepository(database, nil)
	system, service := testRuntimePair()
	operationID := domain.OperationID("op_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	now := system.StartedAt
	reservation := testReservation("pp_01ARZ3NDEKTSV4RRFFQ69G5FAV", now, now.Add(time.Minute))
	leaseID := domain.PortLeaseID("pl_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	if err := leaseRepository.Reserve(context.Background(), reservation, reserveTestLease(reservation, leaseID.String(), 32102)); err != nil {
		t.Fatal(err)
	}
	if err := runtimeRepository.Create(context.Background(), operationID, system, service); err != nil {
		t.Fatal(err)
	}
	if err := leaseRepository.MarkBound(context.Background(), leaseID, system.ID, now.Add(time.Second)); err != nil {
		t.Fatalf("MarkBound() error = %v", err)
	}
	leases, err := leaseRepository.ListPlan(context.Background(), reservation.PlanID)
	if err != nil || len(leases) != 1 || leases[0].State != ports.LeaseBound || leases[0].InstanceID == nil || *leases[0].InstanceID != system.ID {
		t.Fatalf("bound leases = %#v, %v", leases, err)
	}
	if err := leaseRepository.Release(context.Background(), leaseID, now.Add(2*time.Second)); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if err := leaseRepository.Release(context.Background(), leaseID, now.Add(3*time.Second)); !errors.Is(err, ports.ErrLeaseState) {
		t.Fatalf("second Release() error = %v", err)
	}
}

func TestPortLeaseRepositoryExpiresReservedDuringReconciliation(t *testing.T) {
	database := openTestDatabase(t)
	seedRuntimeCatalog(t, database)
	repository, _ := NewPortLeaseRepository(database)
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	reservation := testReservation("pp_01ARZ3NDEKTSV4RRFFQ69G5FAV", now, now.Add(time.Minute))
	if err := repository.Reserve(context.Background(), reservation, reserveTestLease(reservation, "pl_01ARZ3NDEKTSV4RRFFQ69G5FAV", 32102)); err != nil {
		t.Fatalf("Reserve() error = %v", err)
	}
	if count, err := repository.ExpireReserved(context.Background(), now.Add(30*time.Second)); err != nil || count != 0 {
		t.Fatalf("early ExpireReserved() = (%d, %v)", count, err)
	}
	if count, err := repository.ExpireReserved(context.Background(), now.Add(time.Minute)); err != nil || count != 0 {
		t.Fatalf("active Operation lease expired = (%d, %v)", count, err)
	}
	if _, err := database.Exec(`UPDATE operations SET state='succeeded', started_at=?, finished_at=? WHERE id=?`,
		now, now.Add(time.Second), reservation.OperationID.String()); err != nil {
		t.Fatalf("finish reservation Operation: %v", err)
	}
	if count, err := repository.ExpireReserved(context.Background(), now.Add(time.Minute)); err != nil || count != 1 {
		t.Fatalf("due ExpireReserved() = (%d, %v)", count, err)
	}
	leases, err := repository.ListPlan(context.Background(), reservation.PlanID)
	if err != nil || len(leases) != 1 || leases[0].State != ports.LeaseExpired {
		t.Fatalf("expired leases = (%#v, %v)", leases, err)
	}
}

func TestPortLeaseRepositoryPersistsOverridesAndStickySuccess(t *testing.T) {
	database := openTestDatabase(t)
	seedRuntimeCatalog(t, database)
	leaseRepository, _ := NewPortLeaseRepository(database)
	runtimeRepository, _ := NewRuntimeInstanceRepository(database, nil)
	system, service := testRuntimePair()
	operationID := domain.OperationID("op_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	now := system.StartedAt
	workspaceID := domain.WorkspaceID("ws_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	if err := leaseRepository.SetWorkspaceOverride(context.Background(), workspaceID, "web", 32102, now); err != nil {
		t.Fatalf("SetWorkspaceOverride() error = %v", err)
	}
	reservation := testReservation("pp_01ARZ3NDEKTSV4RRFFQ69G5FAV", now, now.Add(time.Minute))
	leaseID := domain.PortLeaseID("pl_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	if err := leaseRepository.Reserve(context.Background(), reservation, reserveTestLease(reservation, leaseID.String(), 32103)); err != nil {
		t.Fatal(err)
	}
	if err := runtimeRepository.Create(context.Background(), operationID, system, service); err != nil {
		t.Fatal(err)
	}
	if err := leaseRepository.MarkBound(context.Background(), leaseID, system.ID, now); err != nil {
		t.Fatal(err)
	}
	if err := leaseRepository.RecordSuccessfulPlan(context.Background(), reservation.PlanID, now.Add(time.Second)); err != nil {
		t.Fatalf("RecordSuccessfulPlan() error = %v", err)
	}
	preferences, err := leaseRepository.LoadPreferences(context.Background(), workspaceID)
	if err != nil || preferences.Workspace["web"] != 32102 || preferences.Sticky["web"] != 32103 {
		t.Fatalf("LoadPreferences() = %#v, %v", preferences, err)
	}
}

func testReservation(planID string, now, expiresAt time.Time) ports.Reservation {
	return ports.Reservation{
		PlanID: domain.PortPlanID(planID), WorkspaceID: "ws_01ARZ3NDEKTSV4RRFFQ69G5FAV",
		OperationID: "op_01ARZ3NDEKTSV4RRFFQ69G5FAV", ManifestDigest: testRuntimeDigest,
		Now: now, ExpiresAt: expiresAt,
	}
}

func reserveTestLease(reservation ports.Reservation, id string, port int) ports.SelectLeases {
	return func(_ []ports.Lease) ([]ports.Lease, error) {
		return []ports.Lease{{
			ID: domain.PortLeaseID(id), PlanID: reservation.PlanID,
			WorkspaceID: reservation.WorkspaceID, OperationID: reservation.OperationID,
			ManifestDigest: reservation.ManifestDigest, LogicalName: "web", Protocol: "tcp", Host: "127.0.0.1", Port: port,
			State: ports.LeaseReserved, ExpiresAt: reservation.ExpiresAt,
			CreatedAt: reservation.Now, UpdatedAt: reservation.Now,
		}}, nil
	}
}
