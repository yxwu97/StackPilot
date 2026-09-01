package changeplan

import (
	"reflect"
	"testing"

	"stackpilot/internal/domain"
	"stackpilot/internal/revision"
)

func TestCompareDoesNotInventDifferencesForUnavailableHistoricalFacts(t *testing.T) {
	from, to := comparableSnapshots()
	from.Files = nil
	to.Files = []revision.FileFact{{Path: "pom.xml", Kind: "dependency", Digest: "file-new", Size: 10}}
	from.Secrets = []revision.SecretFact{{ServiceID: "backend", EnvironmentName: "TOKEN", SystemID: "sample", Name: "token", Provider: "local", Version: 2}}
	to.Secrets = nil
	from.Services[0].Images = []revision.ComposeImageFact{{ComposeService: "backend", Status: revision.SourceUnavailable}}
	to.Services[0].Images = []revision.ComposeImageFact{{ComposeService: "backend", Status: revision.SourceUnavailable}}

	result, err := Compare(from, to, "from-digest", "to-digest")
	if err != nil {
		t.Fatalf("Compare() error = %v", err)
	}
	assertItem(t, result.Items, domain.ChangeItemDependencyFile, ChangeChanged, domain.ChangeRiskHigh, "files.launch-identity")
	assertItem(t, result.Items, domain.ChangeItemSecret, ChangeChanged, domain.ChangeRiskHigh, "secrets.candidate-identity")
	for _, finding := range result.Items {
		if finding.Kind == domain.ChangeItemDependencyFile && finding.Change == ChangeAdded ||
			finding.Kind == domain.ChangeItemSecret && finding.Change == ChangeRemoved {
			t.Fatalf("invented concrete difference from unavailable fact: %#v", finding)
		}
	}
}

func TestCompareClassifiesBlockedAndHighRiskChanges(t *testing.T) {
	from, to := comparableSnapshots()
	from.Services = append(from.Services, revision.ServiceFact{
		ServiceID: "required-worker", Driver: domain.DriverProcess, Mode: domain.ProcessDaemon,
		Required: true, HealthCoverage: domain.HealthCoverageBusiness,
	})
	to.Services[0].HealthCoverage = domain.HealthCoverageProcessOnly
	to.Services[0].Driver = domain.DriverCompose
	to.Services[0].Dependencies = []revision.DependencyFact{{ServiceID: "database", Condition: domain.DependencyReady}}
	to.Git.Dirty = true
	to.Runners[0].ExecutableDigest = "runner-new"

	result, err := Compare(from, to, "from-digest", "to-digest")
	if err != nil {
		t.Fatalf("Compare() error = %v", err)
	}
	if result.State != domain.ChangePlanBlocked || result.Risk != domain.ChangeRiskBlocked || result.BlockedCount != 2 {
		t.Fatalf("blocked result = %#v", result)
	}
	assertItem(t, result.Items, domain.ChangeItemService, ChangeRemoved, domain.ChangeRiskBlocked, "required-worker")
	assertItem(t, result.Items, domain.ChangeItemHealth, ChangeChanged, domain.ChangeRiskBlocked, "backend.health")
	assertItem(t, result.Items, domain.ChangeItemManifest, ChangeChanged, domain.ChangeRiskHigh, "workspace.git.dirty")
	assertItem(t, result.Items, domain.ChangeItemService, ChangeChanged, domain.ChangeRiskHigh, "backend.runtime")
	assertItem(t, result.Items, domain.ChangeItemDependency, ChangeChanged, domain.ChangeRiskHigh, "backend.dependencies")
	assertItem(t, result.Items, domain.ChangeItemRunner, ChangeChanged, domain.ChangeRiskHigh, "backend")
}

func TestCompareBlocksUnsafeGitAndKeepsStableOrdering(t *testing.T) {
	from, to := comparableSnapshots()
	to.Git = revision.GitFact{Status: revision.SourceUnsafe, Reason: "GIT_IDENTITY_UNSAFE"}
	to.Files = []revision.FileFact{
		{Path: "z.lock", Kind: "registered", Digest: "z-new", Size: 10},
		{Path: "a.lock", Kind: "dependency", Digest: "a-new", Size: 10},
	}
	from.Files = []revision.FileFact{
		{Path: "z.lock", Kind: "registered", Digest: "z-old", Size: 10},
		{Path: "a.lock", Kind: "dependency", Digest: "a-old", Size: 10},
	}

	first, err := Compare(from, to, "from-digest", "to-digest")
	if err != nil {
		t.Fatalf("Compare() error = %v", err)
	}
	second, err := Compare(from, to, "from-digest", "to-digest")
	if err != nil {
		t.Fatalf("Compare(replay) error = %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("comparison is not deterministic:\nfirst=%#v\nsecond=%#v", first, second)
	}
	if first.State != domain.ChangePlanBlocked || first.BlockedCount != 1 {
		t.Fatalf("unsafe Git result = %#v", first)
	}
	assertSorted(t, first.Items)
	assertItem(t, first.Items, domain.ChangeItemDependencyFile, ChangeChanged, domain.ChangeRiskHigh, "z.lock")
	assertItem(t, first.Items, domain.ChangeItemDependencyFile, ChangeChanged, domain.ChangeRiskMedium, "a.lock")
}

func TestCompareCleanComparableFactsHaveNoFindings(t *testing.T) {
	from, to := comparableSnapshots()
	result, err := Compare(from, to, "from-digest", "to-digest")
	if err != nil {
		t.Fatalf("Compare() error = %v", err)
	}
	if len(result.Items) != 0 || result.State != domain.ChangePlanReady || result.Risk != domain.ChangeRiskInfo {
		t.Fatalf("clean result = %#v", result)
	}
}

func TestCompareReportsLogicalPortContractChanges(t *testing.T) {
	from, to := comparableSnapshots()
	oldPort, newPort := 32100, 32101
	from.Ports = []revision.PortFact{{Name: "http", Protocol: "tcp", Preferred: &oldPort, ConflictPolicy: "strict", Exposure: "loopback"}}
	to.Ports = []revision.PortFact{{Name: "http", Protocol: "tcp", Preferred: &newPort, ConflictPolicy: "strict", Exposure: "loopback"}}
	result, err := Compare(from, to, "from-digest", "to-digest")
	if err != nil {
		t.Fatalf("Compare() error = %v", err)
	}
	assertItem(t, result.Items, domain.ChangeItemPort, ChangeChanged, domain.ChangeRiskHigh, "http")

	sameValue := oldPort
	to.Ports = []revision.PortFact{{Name: "http", Protocol: "tcp", Preferred: &sameValue, ConflictPolicy: "strict", Exposure: "loopback"}}
	result, err = Compare(from, to, "from-digest", "to-digest")
	if err != nil || len(result.Items) != 0 {
		t.Fatalf("equal port values result = (%#v, %v)", result, err)
	}
}

func comparableSnapshots() (revision.Snapshot, revision.Snapshot) {
	service := revision.ServiceFact{
		ServiceID: "backend", Driver: domain.DriverProcess, Mode: domain.ProcessDaemon, Required: true,
		DefinitionDigest: "definition", CommandDigest: "command", HealthCoverage: domain.HealthCoverageBusiness,
		RestartPolicy: "never",
	}
	runner := revision.RunnerFact{
		ServiceID: "backend", Kind: "process", Version: "1", ExecutableDigest: "runner",
		Status: revision.SourceAvailable,
	}
	from := revision.Snapshot{
		SchemaVersion: revision.SchemaVersion, WorkspaceID: "ws_01ARZ3NDEKTSV4RRFFQ69G5FAV", SystemID: "sample",
		ManifestDigest: "manifest", Git: revision.GitFact{Status: revision.SourceAvailable, Revision: "abc"},
		Services: []revision.ServiceFact{service}, Runners: []revision.RunnerFact{runner},
	}
	to := from
	to.Services = append([]revision.ServiceFact(nil), from.Services...)
	to.Runners = append([]revision.RunnerFact(nil), from.Runners...)
	from.Kind, to.Kind = domain.RevisionRunning, domain.RevisionWorkspace
	return from, to
}

func assertItem(t *testing.T, items []Item, kind domain.ChangeItemKind, change Change, risk domain.ChangeRisk, key string) {
	t.Helper()
	for _, finding := range items {
		if finding.Kind == kind && finding.Change == change && finding.Risk == risk && finding.Key == key {
			return
		}
	}
	t.Fatalf("missing item kind=%q change=%q risk=%q key=%q in %#v", kind, change, risk, key, items)
}

func assertSorted(t *testing.T, items []Item) {
	t.Helper()
	for index := 1; index < len(items); index++ {
		previous, current := items[index-1], items[index]
		if previous.Kind > current.Kind || previous.Kind == current.Kind && previous.Key > current.Key {
			t.Fatalf("items are not stable-sorted at %d: %#v then %#v", index, previous, current)
		}
	}
}
