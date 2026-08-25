package storage

import (
	"context"
	"testing"
	"time"

	"stackpilot/internal/security"
)

func TestAuditRepositoryPersistsAndPagesSafeRecords(t *testing.T) {
	database := openTestDatabase(t)
	repository, err := NewAuditRepository(database)
	if err != nil {
		t.Fatal(err)
	}
	first := security.AuditEvent{
		SubjectType: "local_token", Action: "workspace.register", TargetType: "workspace", TargetID: "ws_test",
		Result: "succeeded", TraceID: "tr_test", ClientType: "cli", OccurredAt: time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC),
	}
	created, err := repository.AppendAudit(context.Background(), first)
	if err != nil || created.ID < 1 {
		t.Fatalf("AppendAudit() = (%+v, %v)", created, err)
	}
	second := first
	second.Action, second.Result, second.ErrorCode, second.OccurredAt = "system.start", "failed", "PORT_CONFLICT", first.OccurredAt.Add(time.Second)
	second, err = repository.AppendAudit(context.Background(), second)
	if err != nil {
		t.Fatal(err)
	}
	page, err := repository.ListAudit(context.Background(), created.ID, 10)
	if err != nil || len(page) != 1 || page[0].ID != second.ID || page[0].ErrorCode != "PORT_CONFLICT" {
		t.Fatalf("ListAudit() = (%+v, %v)", page, err)
	}
}
