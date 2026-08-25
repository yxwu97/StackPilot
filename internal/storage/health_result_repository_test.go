package storage

import (
	"context"
	"strings"
	"testing"
	"time"

	"stackpilot/internal/domain"
	"stackpilot/internal/health"
)

func TestHealthResultRepositoryPersistsAndListsNewestFirst(t *testing.T) {
	database := openTestDatabase(t)
	serviceInstanceID := seedRuntimeInstance(t, database)
	repository, err := NewHealthResultRepository(database)
	if err != nil {
		t.Fatalf("NewHealthResultRepository() error = %v", err)
	}
	base := time.Date(2026, 8, 18, 13, 0, 0, 0, time.UTC)
	results := []health.Result{
		{Kind: health.KindTCP, CheckedAt: base, Duration: 12 * time.Millisecond, ErrorCode: health.CodeTCPRefused, Summary: "refused"},
		{Kind: health.KindHTTP, CheckedAt: base.Add(time.Second), Duration: 3 * time.Millisecond, Success: true, Summary: "HTTP status 200"},
		{Kind: health.KindCompose, CheckedAt: base.Add(2 * time.Second), Duration: 5 * time.Millisecond, ErrorCode: health.CodeContainerUnhealthy, Summary: "unhealthy"},
	}
	for _, result := range results {
		if err := repository.Record(context.Background(), serviceInstanceID, result); err != nil {
			t.Fatalf("Record() error = %v", err)
		}
	}
	stored, err := repository.ListRecent(context.Background(), serviceInstanceID, 10)
	if err != nil {
		t.Fatalf("ListRecent() error = %v", err)
	}
	if len(stored) != 3 || stored[0].Kind != health.KindCompose || stored[0].ErrorCode != health.CodeContainerUnhealthy || stored[1].Kind != health.KindHTTP || !stored[1].Success {
		t.Fatalf("ListRecent() = %#v", stored)
	}
	if stored[2].Duration != 12*time.Millisecond || stored[0].CheckedAt.Location() != time.UTC {
		t.Fatalf("stored timing = %#v", stored)
	}
}

func TestHealthResultRepositoryRejectsUnsafeOrInconsistentResults(t *testing.T) {
	database := openTestDatabase(t)
	serviceInstanceID := seedRuntimeInstance(t, database)
	repository, _ := NewHealthResultRepository(database)
	now := time.Now().UTC()
	cases := []health.Result{
		{Kind: health.Kind("unknown"), CheckedAt: now, Success: true, Summary: "ok"},
		{Kind: health.KindTCP, CheckedAt: now, Success: true, ErrorCode: health.CodeTCPTimeout, Summary: "bad"},
		{Kind: health.KindTCP, CheckedAt: now, Summary: strings.Repeat("x", 2049), ErrorCode: health.CodeTCPRefused},
	}
	for _, result := range cases {
		if err := repository.Record(context.Background(), serviceInstanceID, result); err == nil {
			t.Fatalf("Record(%#v) unexpectedly succeeded", result)
		}
	}
	if err := repository.Record(context.Background(), domain.ServiceInstanceID("bad"), health.Result{
		Kind: health.KindTCP, CheckedAt: now, ErrorCode: health.CodeTCPRefused, Summary: "refused",
	}); err == nil {
		t.Fatal("Record() accepted an invalid service instance ID")
	}
	if _, err := repository.ListRecent(context.Background(), serviceInstanceID, maximumHealthResults+1); err == nil {
		t.Fatal("ListRecent() accepted an unbounded limit")
	}
}

func TestHealthResultRepositoryEnforcesForeignKeyAndCascade(t *testing.T) {
	database := openTestDatabase(t)
	repository, _ := NewHealthResultRepository(database)
	missing := domain.ServiceInstanceID("svi_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	result := health.Result{Kind: health.KindProcess, CheckedAt: time.Now().UTC(), ErrorCode: health.CodeProcessExited, Summary: "exited"}
	if err := repository.Record(context.Background(), missing, result); err == nil {
		t.Fatal("Record() accepted a missing service instance")
	}
	serviceInstanceID := seedRuntimeInstance(t, database)
	if err := repository.Record(context.Background(), serviceInstanceID, result); err != nil {
		t.Fatalf("Record() error = %v", err)
	}
	if _, err := database.Exec(`DELETE FROM system_instances`); err != nil {
		t.Fatalf("delete runtime instance: %v", err)
	}
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM health_results`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("health result count after cascade = %d, error = %v", count, err)
	}
}

func TestHealthResultRepositoryCompactsBoundedOldDetails(t *testing.T) {
	database := openTestDatabase(t)
	serviceInstanceID := seedRuntimeInstance(t, database)
	repository, _ := NewHealthResultRepository(database)
	base := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	for index := 0; index < 5; index++ {
		result := health.Result{Kind: health.KindTCP, CheckedAt: base.Add(time.Duration(index) * time.Minute), Duration: time.Duration(index+1) * time.Millisecond, Success: true, Summary: "healthy"}
		if err := repository.Record(context.Background(), serviceInstanceID, result); err != nil {
			t.Fatal(err)
		}
	}
	policy := HealthRetentionPolicy{DetailWindow: time.Hour, RecentLimit: 2, BatchLimit: 2}
	now := base.Add(48 * time.Hour)
	removed, err := repository.Compact(context.Background(), now, policy)
	if err != nil || removed != 2 {
		t.Fatalf("first Compact() = (%d, %v)", removed, err)
	}
	removed, err = repository.Compact(context.Background(), now, policy)
	if err != nil || removed != 1 {
		t.Fatalf("second Compact() = (%d, %v)", removed, err)
	}
	var details, checks, successes, total, maximum int
	if err := database.QueryRow(`SELECT COUNT(*) FROM health_results`).Scan(&details); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT check_count,success_count,duration_total_ms,duration_max_ms FROM health_hourly_aggregates`).Scan(&checks, &successes, &total, &maximum); err != nil {
		t.Fatal(err)
	}
	if details != 2 || checks != 3 || successes != 3 || total != 6 || maximum != 3 {
		t.Fatalf("compaction = details %d aggregate (%d,%d,%d,%d)", details, checks, successes, total, maximum)
	}
}
