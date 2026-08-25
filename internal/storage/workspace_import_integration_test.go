package storage

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"stackpilot/internal/domain"
	"stackpilot/internal/importer"
	"stackpilot/internal/manifest"
	"stackpilot/internal/workspace"
)

func TestWorkspaceImportAndStructuredEditWithRealSQLite(t *testing.T) {
	database := openTestDatabase(t)
	manager := newImportTestManager(t, database)
	analyzer, err := importer.NewAnalyzer()
	if err != nil {
		t.Fatal(err)
	}
	repository, err := NewWorkspaceImportRepository(database)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	service, err := workspace.NewImportService(workspace.ImportServiceConfig{Context: ctx, Analyzer: analyzer, Repository: repository, Workspaces: manager})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); service.Wait() })

	root := writeImportWorkspace(t, true)
	draft, err := service.Analyze(ctx, root, "run.bat")
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Apply(ctx, draft.ID, "serve-existing", "test", "same-key")
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := service.Apply(ctx, draft.ID, "serve-existing", "test", "same-key")
	if err != nil {
		t.Fatal(err)
	}
	if created.Operation.ID != replayed.Operation.ID || replayed.Created {
		t.Fatalf("idempotency replay = %#v", replayed)
	}
	operation := waitImportOperation(t, ctx, service, created.Operation.ID)
	if operation.State != domain.OperationSucceeded || operation.WorkspaceID == nil {
		t.Fatalf("import Operation = %#v", operation)
	}
	if _, err := os.Stat(filepath.Join(root, ".stackpilot", "system.yaml")); err != nil {
		t.Fatal(err)
	}

	edit, err := service.CreateEditDraft(ctx, *operation.WorkspaceID, workspace.EditInput{SystemName: "Edited Game", Description: "edited",
		ServiceDisplayNames: map[string]string{"web": "Edited Web"}, PortPreferred: map[string]int{"web": 7461}})
	if err != nil {
		t.Fatal(err)
	}
	editResult, err := service.Apply(ctx, edit.ID, "edit", "test", "edit-key")
	if err != nil {
		t.Fatal(err)
	}
	if current := waitImportOperation(t, ctx, service, editResult.Operation.ID); current.State != domain.OperationSucceeded {
		t.Fatalf("edit Operation = %#v", current)
	}
	definition, err := manager.Definition(ctx, *operation.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if definition.Workspace.SystemName != "Edited Game" {
		t.Fatalf("edited name = %q", definition.Workspace.SystemName)
	}
	target := t.TempDir()
	writeImportFile(t, filepath.Join(target, ".stackpilot", "system.yaml"), definition.Manifest.NormalizedYAML)
	relink, err := service.CreateRelinkDraft(ctx, *operation.WorkspaceID, target)
	if err != nil {
		t.Fatal(err)
	}
	relinkResult, err := service.Apply(ctx, relink.ID, "relink", "test", "relink-key")
	if err != nil {
		t.Fatal(err)
	}
	if current := waitImportOperation(t, ctx, service, relinkResult.Operation.ID); current.State != domain.OperationSucceeded {
		t.Fatalf("relink Operation = %#v", current)
	}
	relinked, err := manager.Get(ctx, *operation.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if relinked.CanonicalPath != target {
		t.Fatalf("relinked path = %q, want %q", relinked.CanonicalPath, target)
	}
	if _, err := os.Stat(filepath.Join(root, ".stackpilot", "system.yaml")); err != nil {
		t.Fatalf("old workspace files changed: %v", err)
	}
}

func TestWorkspaceImportBlocksNonLoopbackService(t *testing.T) {
	database := openTestDatabase(t)
	manager := newImportTestManager(t, database)
	analyzer, _ := importer.NewAnalyzer()
	repository, _ := NewWorkspaceImportRepository(database)
	ctx, cancel := context.WithCancel(context.Background())
	service, err := workspace.NewImportService(workspace.ImportServiceConfig{Context: ctx, Analyzer: analyzer, Repository: repository, Workspaces: manager})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); service.Wait() })
	root := writeImportWorkspace(t, false)
	draft, err := service.Analyze(ctx, root, "run.bat")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Apply(ctx, draft.ID, "serve-existing", "test", "unsafe"); !errors.Is(err, importer.ErrImportIncomplete) {
		t.Fatalf("Apply() = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".stackpilot", "system.yaml")); !os.IsNotExist(err) {
		t.Fatalf("unsafe manifest stat = %v", err)
	}
}

func TestWorkspaceImportCanonicalTargetLockIsDatabaseEnforced(t *testing.T) {
	database := openTestDatabase(t)
	manager := newImportTestManager(t, database)
	analyzer, _ := importer.NewAnalyzer()
	repository, _ := NewWorkspaceImportRepository(database)
	ctx, cancel := context.WithCancel(context.Background())
	service, err := workspace.NewImportService(workspace.ImportServiceConfig{Context: ctx, Analyzer: analyzer, Repository: repository, Workspaces: manager})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); service.Wait() })
	draft, err := service.Analyze(ctx, writeImportWorkspace(t, true), "run.bat")
	if err != nil {
		t.Fatal(err)
	}
	operations := []workspace.ImportOperation{
		{ID: domain.OperationID("op_01ARZ3NDEKTSV4RRFFQ69G5FAA"), DraftID: draft.ID, TargetKey: draft.TargetKey, CandidateID: "serve-existing", Type: "workspace-import-apply", State: domain.OperationQueued, IdempotencySubject: "one", RouteKey: "workspace-import-apply", IdempotencyKey: "one", RequestDigest: strings.Repeat("a", 64), CreatedAt: time.Now().UTC()},
		{ID: domain.OperationID("op_01ARZ3NDEKTSV4RRFFQ69G5FAB"), DraftID: draft.ID, TargetKey: draft.TargetKey, CandidateID: "serve-existing", Type: "workspace-import-apply", State: domain.OperationQueued, IdempotencySubject: "two", RouteKey: "workspace-import-apply", IdempotencyKey: "two", RequestDigest: strings.Repeat("b", 64), CreatedAt: time.Now().UTC()},
	}
	var wait sync.WaitGroup
	errorsSeen := make(chan error, len(operations))
	for _, operation := range operations {
		wait.Add(1)
		go func(candidate workspace.ImportOperation) {
			defer wait.Done()
			_, createErr := repository.CreateImportOperation(ctx, candidate, []string{"verify-source"})
			errorsSeen <- createErr
		}(operation)
	}
	wait.Wait()
	close(errorsSeen)
	succeeded, locked := 0, 0
	for createErr := range errorsSeen {
		if createErr == nil {
			succeeded++
		} else if errors.Is(createErr, workspace.ErrImportAlreadyActive) {
			locked++
		} else {
			t.Fatalf("unexpected create error: %v", createErr)
		}
	}
	if succeeded != 1 || locked != 1 {
		t.Fatalf("concurrent creates = succeeded %d, locked %d", succeeded, locked)
	}
}

func newImportTestManager(t *testing.T, database *sql.DB) *workspace.Manager {
	t.Helper()
	repository, err := NewWorkspaceRepository(database)
	if err != nil {
		t.Fatal(err)
	}
	loader, err := manifest.NewLoader()
	if err != nil {
		t.Fatal(err)
	}
	manager, err := workspace.NewManager(repository, loader, manifest.NewValidatorWithCapabilities("compose", "liveness", "auto-restart"))
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func waitImportOperation(t *testing.T, ctx context.Context, service *workspace.ImportService, id domain.OperationID) *workspace.ImportOperation {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		operation, err := service.GetOperation(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if operation.State.Terminal() {
			return operation
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("workspace import Operation did not finish")
	return nil
}

func writeImportWorkspace(t *testing.T, loopback bool) string {
	t.Helper()
	root := t.TempDir()
	writeImportFile(t, filepath.Join(root, "package.json"), `{"name":"import-game"}`)
	writeImportFile(t, filepath.Join(root, "run.bat"), "@echo off\r\ncd /d \"%~dp0\"\r\nnode tools\\serve.js build\\web\r\n")
	listen := "server.listen(port, () => {});"
	if loopback {
		listen = "server.listen(port, '127.0.0.1', () => {});"
	}
	writeImportFile(t, filepath.Join(root, "tools", "serve.js"), "const PORT = Number('') || 7460;\nconst port = PORT;\n"+listen)
	if err := os.MkdirAll(filepath.Join(root, "build", "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func writeImportFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}
