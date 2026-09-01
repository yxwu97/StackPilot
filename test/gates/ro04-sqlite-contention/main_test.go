package main

import (
	"context"
	"testing"
)

func TestExecuteUsesIsolatedNineteenServiceWorkload(t *testing.T) {
	report, err := execute(context.Background())
	if err != nil {
		t.Fatalf("execute() error = %v", err)
	}
	if report.SchemaVersion != "ro04-sqlite-contention/v1" || report.Scope != "isolated-sqlite-wal" || report.DatabaseMode != "temporary-wal" {
		t.Fatalf("report identity = %#v", report)
	}
	if report.ServiceCount != fixtureServices || report.PreloadedRows != fixtureServices*preloadCycles {
		t.Fatalf("fixture scale = services %d, rows %d", report.ServiceCount, report.PreloadedRows)
	}
	if report.Baseline.HealthWrite.Count != baselineCycles || report.Baseline.RuntimeReconcile.Count != baselineCycles ||
		report.Concurrent.HealthWrite.Count != concurrentCycles || report.Concurrent.RuntimeReconcile.Count != concurrentCycles ||
		report.Concurrent.MetricBatch.Count != concurrentCycles || report.Concurrent.Compaction.Count != 4 {
		t.Fatalf("workload counts = %#v", report)
	}
	if report.GateStatus != "passed" || len(report.Blockers) != 0 {
		t.Fatalf("isolated contention Gate = %#v", report)
	}
}
