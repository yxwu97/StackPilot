package orchestrator_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"stackpilot/internal/domain"
	"stackpilot/internal/incident"
	"stackpilot/internal/orchestrator"
	"stackpilot/internal/storage"
)

func TestReconciliationPersistsAutomaticRestartByExitPolicy(t *testing.T) {
	tests := []struct {
		name     string
		policy   string
		exitCode uint32
		want     bool
	}{
		{name: "on-failure abnormal exit", policy: "on-failure", exitCode: 23, want: true},
		{name: "on-failure normal exit", policy: "on-failure", exitCode: 0, want: false},
		{name: "always normal exit", policy: "always", exitCode: 0, want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness := newAutomaticRestartHarness(t, test.policy, 2)
			startAutomaticRestartFixture(t, harness)
			harness.driver.setObservation("exited", &test.exitCode)

			if err := harness.service.ReconcileRuntimes(context.Background()); err != nil {
				t.Fatal(err)
			}
			restart := findOperationByType(t, harness, domain.OperationServiceRestart)
			if (restart != nil) != test.want {
				t.Fatalf("automatic restart Operation = %#v, want created %t", restart, test.want)
			}
			if restart != nil {
				awaitOperation(t, harness.service, restart.ID, domain.OperationSucceeded)
			}
		})
	}
}

func TestAutomaticRestartLimitCreatesIncident(t *testing.T) {
	harness := newAutomaticRestartHarness(t, "on-failure", 1)
	startAutomaticRestartFixture(t, harness)
	exitCode := uint32(17)
	harness.driver.setObservation("exited", &exitCode)
	if err := harness.service.ReconcileRuntimes(context.Background()); err != nil {
		t.Fatal(err)
	}
	restart := findOperationByType(t, harness, domain.OperationServiceRestart)
	if restart == nil {
		t.Fatal("first automatic restart Operation was not created")
	}
	awaitOperation(t, harness.service, restart.ID, domain.OperationSucceeded)

	harness.driver.setObservation("exited", &exitCode)
	if err := harness.service.ReconcileRuntimes(context.Background()); err != nil {
		t.Fatal(err)
	}
	if countOperationsByType(t, harness, domain.OperationServiceRestart) != 1 {
		t.Fatal("restart limit created an additional Operation")
	}
	repository, _ := storage.NewIncidentRepository(harness.database)
	records, err := repository.List(context.Background(), harness.workspace.ID, 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if record.Context.Kind == incident.KindRestartLimit && record.Context.TriggerCode == "RESTART_LIMIT_REACHED" {
			return
		}
	}
	t.Fatalf("restart limit Incident not found in %#v", records)
}

func TestAutomaticRestartReleasesClaimWhenOperationCreationFails(t *testing.T) {
	harness := newAutomaticRestartHarness(t, "on-failure", 2)
	startAutomaticRestartFixture(t, harness)
	manifestPath := filepath.Join(harness.workspace.CanonicalPath, ".stackpilot", "system.yaml")
	contents, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	contents = []byte(strings.Replace(string(contents), "name: Fixture", "name: Changed Fixture", 1))
	if err := os.WriteFile(manifestPath, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := harness.manager.Refresh(context.Background(), harness.workspace.ID); err != nil {
		t.Fatal(err)
	}
	exitCode := uint32(5)
	harness.driver.setObservation("exited", &exitCode)
	if err := harness.service.ReconcileRuntimes(context.Background()); !errors.Is(err, orchestrator.ErrManifestChanged) {
		t.Fatalf("ReconcileRuntimes() error = %v, want ErrManifestChanged", err)
	}
	var claims int
	if err := harness.database.QueryRow(`SELECT COUNT(*) FROM service_restart_attempts`).Scan(&claims); err != nil || claims != 0 {
		t.Fatalf("restart claims after failed Operation creation = %d, %v", claims, err)
	}
}

func newAutomaticRestartHarness(t *testing.T, policy string, maximum int) systemServiceHarness {
	t.Helper()
	return newSystemServiceHarnessWithWriter(t, immediateReadiness{}, func(root string) {
		writeAutomaticRestartManifest(t, root, policy, maximum)
	})
}

func startAutomaticRestartFixture(t *testing.T, harness systemServiceHarness) {
	t.Helper()
	result, err := harness.service.SubmitStart(context.Background(), orchestrator.StartSingleServiceInput{
		WorkspaceID: harness.workspace.ID, SystemID: harness.workspace.SystemID,
		IdempotencySubject: "test-user", IdempotencyKey: "automatic-restart-start", Request: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	awaitOperation(t, harness.service, result.Operation.ID, domain.OperationSucceeded)
}

func findOperationByType(t *testing.T, harness systemServiceHarness, operationType domain.OperationType) *orchestrator.Operation {
	t.Helper()
	operations, err := harness.service.ListOperations(context.Background(), &harness.workspace.ID, 20)
	if err != nil {
		t.Fatal(err)
	}
	for index := range operations {
		if operations[index].Type == operationType {
			return &operations[index]
		}
	}
	return nil
}

func countOperationsByType(t *testing.T, harness systemServiceHarness, operationType domain.OperationType) int {
	t.Helper()
	operations, err := harness.service.ListOperations(context.Background(), &harness.workspace.ID, 20)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, operation := range operations {
		if operation.Type == operationType {
			count++
		}
	}
	return count
}

func writeAutomaticRestartManifest(t *testing.T, root, policy string, maximum int) {
	t.Helper()
	for _, directory := range []string{"backend", ".stackpilot"} {
		if err := os.Mkdir(filepath.Join(root, directory), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	contents := fmt.Sprintf(`apiVersion: stackpilot.io/v1alpha1
kind: System
metadata: {id: fixture, name: Fixture}
spec:
  services:
    backend:
      driver: process
      mode: daemon
      runner: java
      workingDirectory: backend
      arguments: ["-version"]
      stop: {gracefulTimeout: 1s}
      readiness: {type: process, timeout: 3s, interval: 100ms}
      restart: {policy: %s, initialBackoff: 100ms, maxBackoff: 100ms, maxAttempts: %d, stableWindow: 100ms}
`, policy, maximum)
	if err := os.WriteFile(filepath.Join(root, ".stackpilot", "system.yaml"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}
