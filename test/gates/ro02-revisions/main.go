// ro02-revisions executes the read-only real-workspace revision Gate.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
	"time"

	"stackpilot/internal/capability"
	"stackpilot/internal/domain"
	"stackpilot/internal/manifest"
	"stackpilot/internal/revision"
	"stackpilot/internal/runner"
	"stackpilot/internal/security"
	"stackpilot/internal/storage"
	"stackpilot/internal/workspace"
)

type workspacePaths []string

func (paths *workspacePaths) String() string { return strings.Join(*paths, ",") }
func (paths *workspacePaths) Set(value string) error {
	if value == "" {
		return fmt.Errorf("workspace path is required")
	}
	*paths = append(*paths, value)
	return nil
}

type gateReport struct {
	SchemaVersion string         `json:"schemaVersion"`
	GeneratedAt   time.Time      `json:"generatedAt"`
	Platform      string         `json:"platform"`
	GoVersion     string         `json:"goVersion"`
	Systems       []systemReport `json:"systems"`
}

type systemReport struct {
	SystemID          domain.SystemID `json:"systemId"`
	RevisionDigest    string          `json:"revisionDigest"`
	ManifestDigest    string          `json:"manifestDigest"`
	GitStatus         string          `json:"gitStatus"`
	GitDirty          bool            `json:"gitDirty"`
	ServiceCount      int             `json:"serviceCount"`
	FileCount         int             `json:"fileCount"`
	RunnerAvailable   int             `json:"runnerAvailable"`
	RunnerUnavailable int             `json:"runnerUnavailable"`
	Coverage          map[string]int  `json:"coverage"`
	Deterministic     bool            `json:"deterministic"`
}

func main() {
	dataDir := flag.String("data-dir", "", "absolute isolated Gate data directory")
	var roots workspacePaths
	flag.Var(&roots, "workspace", "absolute workspace root; repeat for each system")
	flag.Parse()
	if err := run(*dataDir, roots); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(dataDir string, roots []string) error {
	if dataDir == "" || len(roots) == 0 {
		return fmt.Errorf("data directory and at least one workspace are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	service, manager, database, err := newGateService(ctx, dataDir)
	if err != nil {
		return err
	}
	defer database.Close()
	report, err := collectAll(ctx, service, manager, roots)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(report)
}

func newGateService(ctx context.Context, dataDir string) (*revision.Service, *workspace.Manager, *sql.DB, error) {
	database, err := storage.OpenDataDir(ctx, dataDir)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("open isolated Gate database: %w", err)
	}
	fail := func(failure error) (*revision.Service, *workspace.Manager, *sql.DB, error) {
		_ = database.Close()
		return nil, nil, nil, failure
	}
	workspaceRepository, err := storage.NewWorkspaceRepository(database)
	if err != nil {
		return fail(err)
	}
	loader, err := manifest.NewLoader()
	if err != nil {
		return fail(err)
	}
	manager, err := workspace.NewManager(workspaceRepository, loader, manifest.NewValidatorWithCapabilities(capability.PublishedManifestAliases()...))
	if err != nil {
		return fail(err)
	}
	resolvedRunner, err := runner.NewResolver(runner.Config{})
	if err != nil {
		return fail(err)
	}
	gitProbe, err := revision.NewGitProbe("")
	if err != nil {
		return fail(err)
	}
	collector, err := revision.NewCollector(revision.CollectorConfig{
		Workspaces: manager, Runtime: emptyRuntime{}, ResolvedSpecs: emptyResolvedSpecs{},
		SecretVersions: emptySecretVersions{}, Runners: resolvedRunner, Git: gitProbe,
	})
	if err != nil {
		return fail(err)
	}
	repository, err := storage.NewRevisionRepository(database)
	if err != nil {
		return fail(err)
	}
	service, err := revision.NewService(collector, repository)
	if err != nil {
		return fail(err)
	}
	return service, manager, database, nil
}

func collectAll(ctx context.Context, service *revision.Service, manager *workspace.Manager, roots []string) (gateReport, error) {
	report := gateReport{
		SchemaVersion: "ro02-real-workspace/v1",
		GeneratedAt:   time.Now().UTC(),
		Platform:      runtime.GOOS + "/" + runtime.GOARCH,
		GoVersion:     runtime.Version(),
		Systems:       make([]systemReport, 0, len(roots)),
	}
	for _, root := range roots {
		record, err := manager.Register(ctx, root)
		if err != nil {
			return gateReport{}, fmt.Errorf("register real workspace: %w", err)
		}
		item, err := collectOne(ctx, service, record, root)
		if err != nil {
			return gateReport{}, fmt.Errorf("collect %s workspace revision: %w", record.SystemID, err)
		}
		report.Systems = append(report.Systems, item)
	}
	sort.Slice(report.Systems, func(i, j int) bool { return report.Systems[i].SystemID < report.Systems[j].SystemID })
	return report, nil
}

func collectOne(ctx context.Context, service *revision.Service, workspaceRecord *workspace.Record, root string) (systemReport, error) {
	first, err := service.Collect(ctx, workspaceRecord.ID, domain.RevisionWorkspace)
	if err != nil {
		return systemReport{}, err
	}
	second, err := service.Collect(ctx, workspaceRecord.ID, domain.RevisionWorkspace)
	if err != nil {
		return systemReport{}, err
	}
	if first.ID != second.ID || first.Digest != second.Digest {
		return systemReport{}, fmt.Errorf("repeated collection was not idempotent")
	}
	if strings.Contains(strings.ToLower(string(first.JSON)), strings.ToLower(root)) {
		return systemReport{}, fmt.Errorf("snapshot contains an absolute workspace path")
	}
	var snapshot revision.Snapshot
	if err := json.Unmarshal(first.JSON, &snapshot); err != nil {
		return systemReport{}, fmt.Errorf("decode persisted revision: %w", err)
	}
	return summarize(snapshot, first.Digest), nil
}

func summarize(snapshot revision.Snapshot, digest string) systemReport {
	result := systemReport{
		SystemID: snapshot.SystemID, RevisionDigest: digest, ManifestDigest: snapshot.ManifestDigest,
		GitStatus: string(snapshot.Git.Status), GitDirty: snapshot.Git.Dirty,
		ServiceCount: len(snapshot.Services), FileCount: len(snapshot.Files), Deterministic: true,
		Coverage: map[string]int{},
	}
	for _, fact := range snapshot.Runners {
		if fact.Status == revision.SourceAvailable {
			result.RunnerAvailable++
		} else {
			result.RunnerUnavailable++
		}
	}
	for _, fact := range snapshot.Services {
		result.Coverage[string(fact.HealthCoverage)]++
	}
	return result
}

type emptyRuntime struct{}

func (emptyRuntime) GetActive(context.Context, domain.WorkspaceID) (*domain.SystemInstance, bool, error) {
	return nil, false, nil
}
func (emptyRuntime) ListServices(context.Context, domain.SystemInstanceID) ([]domain.ServiceInstance, error) {
	return nil, nil
}

type emptyResolvedSpecs struct{}

func (emptyResolvedSpecs) LoadResolvedSpec(context.Context, string) ([]byte, error) {
	return nil, revision.ErrSourceUnavailable
}

type emptySecretVersions struct{}

func (emptySecretVersions) ListServiceSecretVersions(context.Context, domain.ServiceInstanceID) ([]security.ServiceSecretVersion, error) {
	return nil, nil
}
