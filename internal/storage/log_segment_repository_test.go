package storage

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"stackpilot/internal/domain"
	"stackpilot/internal/logs"
)

func TestLogSegmentRepositoryPersistsAndQueriesSequenceRanges(t *testing.T) {
	database := openTestDatabase(t)
	serviceInstanceID := seedRuntimeInstance(t, database)
	repository, err := NewLogSegmentRepository(database)
	if err != nil {
		t.Fatalf("NewLogSegmentRepository() error = %v", err)
	}
	base := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	second := logs.Segment{
		ServiceInstanceID: serviceInstanceID, Stream: logs.StreamStderr, Path: "logs/instance/backend/0002-stderr.ndjson",
		FirstSequence: 2, LastSequence: 4, FirstTimestamp: base.Add(time.Second), LastTimestamp: base.Add(3 * time.Second),
		SizeBytes: 300, ClosedAt: base.Add(4 * time.Second),
	}
	first := logs.Segment{
		ServiceInstanceID: serviceInstanceID, Stream: logs.StreamStdout, Path: "logs/instance/backend/0001-stdout.ndjson",
		FirstSequence: 1, LastSequence: 1, FirstTimestamp: base, LastTimestamp: base,
		SizeBytes: 100, ClosedAt: base.Add(time.Second),
	}
	for _, segment := range []logs.Segment{second, first} {
		if err := repository.RegisterClosed(context.Background(), segment); err != nil {
			t.Fatalf("RegisterClosed() error = %v", err)
		}
	}
	segments, err := repository.ListAfter(context.Background(), serviceInstanceID, 1)
	if err != nil {
		t.Fatalf("ListAfter() error = %v", err)
	}
	if len(segments) != 1 || segments[0].FirstSequence != 2 || segments[0].ClosedAt.Location() != time.UTC {
		t.Fatalf("ListAfter() = %#v", segments)
	}
	firstSequence, lastSequence, found, err := repository.SequenceBounds(context.Background(), serviceInstanceID)
	if err != nil || !found || firstSequence != 1 || lastSequence != 4 {
		t.Fatalf("SequenceBounds() = (%d, %d, %v, %v)", firstSequence, lastSequence, found, err)
	}
	lastTimestamp, found, err := repository.LastTimestamp(context.Background(), serviceInstanceID)
	if err != nil || !found || !lastTimestamp.Equal(second.LastTimestamp) {
		t.Fatalf("LastTimestamp() = (%s, %v, %v)", lastTimestamp, found, err)
	}
	if err := repository.RegisterClosed(context.Background(), first); err == nil {
		t.Fatal("duplicate segment path unexpectedly registered")
	}
}

func TestLogSegmentRepositoryRejectsUnsafePathAndMissingRuntime(t *testing.T) {
	database := openTestDatabase(t)
	repository, _ := NewLogSegmentRepository(database)
	now := time.Now().UTC()
	segment := logs.Segment{
		ServiceInstanceID: "svi_01ARZ3NDEKTSV4RRFFQ69G5FAV", Stream: logs.StreamStdout,
		Path: filepath.Join("..", "outside.ndjson"), FirstSequence: 1, LastSequence: 1,
		FirstTimestamp: now, LastTimestamp: now, SizeBytes: 1, ClosedAt: now,
	}
	if err := repository.RegisterClosed(context.Background(), segment); err == nil {
		t.Fatal("unsafe segment path unexpectedly registered")
	}
	segment.Path = "logs/missing.ndjson"
	if err := repository.RegisterClosed(context.Background(), segment); err == nil {
		t.Fatal("missing service instance foreign key unexpectedly accepted")
	}
	if _, err := repository.ListAfter(context.Background(), domain.ServiceInstanceID("bad"), 0); err == nil {
		t.Fatal("invalid service instance query unexpectedly accepted")
	}
}

func seedRuntimeInstance(t *testing.T, database *sql.DB) domain.ServiceInstanceID {
	t.Helper()
	const (
		workspaceID       = "ws_01ARZ3NDEKTSV4RRFFQ69G5FAV"
		systemInstanceID  = "si_01ARZ3NDEKTSV4RRFFQ69G5FAV"
		serviceInstanceID = "svi_01ARZ3NDEKTSV4RRFFQ69G5FAV"
		digest            = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	)
	now := "2026-08-18T12:00:00Z"
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO systems(id,name,created_at,updated_at) VALUES ('btc','BTC',?,?)`, []any{now, now}},
		{`INSERT INTO manifest_snapshots(digest,system_id,api_version,normalized_yaml,parsed_json,created_at) VALUES (?,'btc','stackpilot.io/v1alpha1','{}','{}',?)`, []any{digest, now}},
		{`INSERT INTO workspaces(id,system_id,root_path,canonical_path,manifest_status,last_valid_digest,created_at,updated_at) VALUES (?,'btc','E:\\workspace','E:\\workspace','valid',?,?,?)`, []any{workspaceID, digest, now, now}},
		{`INSERT INTO services(workspace_id,service_id,driver,mode,required,definition_digest) VALUES (?,'backend','process','daemon',1,?)`, []any{workspaceID, digest}},
		{`INSERT INTO system_instances(id,workspace_id,manifest_digest,resolved_spec_digest,state,started_at) VALUES (?,?,?,?, 'running',?)`, []any{systemInstanceID, workspaceID, digest, digest, now}},
		{`INSERT INTO service_instances(id,system_instance_id,service_id,state,state_version,created_at,updated_at) VALUES (?,?,'backend','ready',1,?,?)`, []any{serviceInstanceID, systemInstanceID, now, now}},
	}
	for _, statement := range statements {
		if _, err := database.ExecContext(context.Background(), statement.query, statement.args...); err != nil {
			t.Fatalf("seed runtime schema: %v", err)
		}
	}
	return domain.ServiceInstanceID(serviceInstanceID)
}
