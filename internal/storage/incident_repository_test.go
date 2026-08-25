package storage

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"stackpilot/internal/domain"
	"stackpilot/internal/incident"
)

func TestIncidentRepositoryMergesFingerprintAndPersistsAnalysis(t *testing.T) {
	database := openTestDatabase(t)
	serviceInstanceID := seedRuntimeInstance(t, database)
	var workspaceValue, systemValue, serviceValue string
	if err := database.QueryRow(`SELECT si.workspace_id,si.id,svi.service_id FROM system_instances si JOIN service_instances svi ON svi.system_instance_id=si.id WHERE svi.id=?`, serviceInstanceID.String()).Scan(&workspaceValue, &systemValue, &serviceValue); err != nil {
		t.Fatal(err)
	}
	repository, _ := NewIncidentRepository(database)
	now := time.Date(2026, 8, 18, 8, 0, 0, 0, time.UTC)
	record := testIncidentRecord(t, workspaceValue, systemValue, serviceInstanceID.String(), serviceValue, now, "aaaaaaaaaa")
	stored, created, err := repository.UpsertOpen(context.Background(), record)
	if err != nil || !created || stored.OccurrenceCount != 1 {
		t.Fatalf("first UpsertOpen() = (%#v,%t,%v)", stored, created, err)
	}
	second := testIncidentRecord(t, workspaceValue, systemValue, serviceInstanceID.String(), serviceValue, now.Add(time.Minute), "bbbbbbbbbb")
	second.Fingerprint = record.Fingerprint
	stored, created, err = repository.UpsertOpen(context.Background(), second)
	if err != nil || created || stored.ID != record.ID || stored.OccurrenceCount != 2 || !stored.LastSeenAt.Equal(second.LastSeenAt) {
		t.Fatalf("merged UpsertOpen() = (%#v,%t,%v)", stored, created, err)
	}
	result, _ := json.Marshal(map[string]any{"results": []incident.RuleResult{{RuleID: "process-exit", Confidence: 100}}})
	analysisID, err := repository.AddAnalysis(context.Background(), incident.Analysis{IncidentID: stored.ID, Engine: "rules", SchemaVersion: "1", Result: result, CreatedAt: now})
	if err != nil || analysisID < 1 {
		t.Fatalf("AddAnalysis() = (%d,%v)", analysisID, err)
	}
	updatedContext := stored.Context
	updatedContext.Logs = []incident.LogLine{{ServiceInstanceID: serviceInstanceID, Sequence: 7, Timestamp: now, Stream: "stderr", Message: "redacted"}}
	if err := repository.UpdateContext(context.Background(), stored.ID, updatedContext); err != nil {
		t.Fatalf("UpdateContext() error = %v", err)
	}
	updated, err := repository.Get(context.Background(), stored.ID)
	if err != nil || len(updated.Context.Logs) != 1 || updated.Context.Logs[0].Sequence != 7 {
		t.Fatalf("updated context = (%#v,%v)", updated, err)
	}
	if err := repository.Resolve(context.Background(), stored.ID, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	resolved, err := repository.Get(context.Background(), stored.ID)
	if err != nil || resolved.State != incident.StateResolved || resolved.ResolvedAt == nil {
		t.Fatalf("resolved incident = (%#v,%v)", resolved, err)
	}
}

func testIncidentRecord(t *testing.T, workspaceID, systemID, serviceInstanceID, serviceID string, now time.Time, entropy string) incident.Record {
	t.Helper()
	id, err := domain.NewIncidentID(now, strings.NewReader(entropy))
	if err != nil {
		t.Fatal(err)
	}
	contextValue := incident.Context{
		SchemaVersion: "1", WorkspaceID: domain.WorkspaceID(workspaceID), SystemInstanceID: domain.SystemInstanceID(systemID),
		ServiceInstanceID: domain.ServiceInstanceID(serviceInstanceID), ServiceID: domain.ServiceID(serviceID), Kind: incident.KindProcessExit,
		TriggerCode: "PROCESS_EXITED", WindowStart: now.Add(-time.Minute), WindowEnd: now.Add(time.Minute),
		Dependencies: map[string]domain.ServiceState{}, Ports: map[string]int{}, Evidence: []incident.EvidenceRef{}, Logs: []incident.LogLine{},
	}
	return incident.Record{
		ID: id, Context: contextValue, Severity: incident.SeverityCritical, State: incident.StateOpen,
		Fingerprint:     incident.Fingerprint(contextValue.WorkspaceID, contextValue.ServiceID, contextValue.Kind, contextValue.TriggerCode),
		OccurrenceCount: 1, FirstSeenAt: now, LastSeenAt: now,
	}
}
