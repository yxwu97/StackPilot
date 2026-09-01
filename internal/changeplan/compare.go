package changeplan

import (
	"fmt"
	"sort"

	"stackpilot/internal/domain"
	"stackpilot/internal/revision"
)

// Compare applies change-risk/v1 to two versioned snapshots.
func Compare(from, to revision.Snapshot, fromDigest, toDigest string) (Result, error) {
	if from.Kind != domain.RevisionRunning || to.Kind != domain.RevisionWorkspace || from.WorkspaceID != to.WorkspaceID ||
		from.SystemID != to.SystemID || fromDigest == "" || toDigest == "" || fromDigest == toDigest {
		return Result{}, ErrInvalidInput
	}
	items := make([]Item, 0)
	if from.ManifestDigest != to.ManifestDigest {
		items = append(items, item(domain.ChangeItemManifest, ChangeChanged, domain.ChangeRiskMedium, "manifest", "The validated system manifest changed."))
	}
	items = append(items, compareGit(to.Git)...)
	manifestChanged := from.ManifestDigest != to.ManifestDigest
	items = append(items, compareServices(from.Services, to.Services, manifestChanged)...)
	items = append(items, compareRunners(from.Runners, to.Runners)...)
	items = append(items, comparePorts(from.Ports, to.Ports)...)
	items = append(items, compareFiles(from.Files, to.Files)...)
	items = append(items, compareSecrets(from.Secrets, to.Secrets)...)
	if len(items) > MaximumItems {
		return Result{}, ErrInvalidInput
	}
	sort.Slice(items, func(left, right int) bool {
		if items[left].Kind != items[right].Kind {
			return items[left].Kind < items[right].Kind
		}
		if items[left].Key != items[right].Key {
			return items[left].Key < items[right].Key
		}
		return items[left].Change < items[right].Change
	})
	risk, blocked := aggregateRisk(items)
	state := domain.ChangePlanReady
	if blocked > 0 {
		state, risk = domain.ChangePlanBlocked, domain.ChangeRiskBlocked
	}
	return Result{SchemaVersion: ResultSchemaVersion, FromDigest: fromDigest, ToDigest: toDigest,
		RuleVersion: RuleVersion, State: state, Risk: risk, BlockedCount: blocked, Items: items}, nil
}

func comparePorts(fromValues, toValues []revision.PortFact) []Item {
	from, to := portMap(fromValues), portMap(toValues)
	items := make([]Item, 0)
	for _, key := range unionPortKeys(from, to) {
		left, leftOK := from[key]
		right, rightOK := to[key]
		if leftOK && rightOK && equalPort(left, right) {
			continue
		}
		change := ChangeChanged
		if !leftOK {
			change = ChangeAdded
		} else if !rightOK {
			change = ChangeRemoved
		}
		items = append(items, item(domain.ChangeItemPort, change, domain.ChangeRiskHigh, key, "The logical port contract changed."))
	}
	return items
}

func equalPort(left, right revision.PortFact) bool {
	if left.Name != right.Name || left.Protocol != right.Protocol || left.FallbackRange != right.FallbackRange ||
		left.ConflictPolicy != right.ConflictPolicy || left.Exposure != right.Exposure {
		return false
	}
	if left.Preferred == nil || right.Preferred == nil {
		return left.Preferred == nil && right.Preferred == nil
	}
	return *left.Preferred == *right.Preferred
}

func compareGit(value revision.GitFact) []Item {
	items := make([]Item, 0, 2)
	if value.Status == revision.SourceUnsafe {
		items = append(items, item(domain.ChangeItemManifest, ChangeChanged, domain.ChangeRiskBlocked, "workspace.git", "The workspace Git identity is unsafe."))
	} else if value.Status != revision.SourceAvailable {
		items = append(items, item(domain.ChangeItemManifest, ChangeChanged, domain.ChangeRiskHigh, "workspace.git", "The workspace Git identity is unavailable."))
	}
	if value.Dirty {
		items = append(items, item(domain.ChangeItemManifest, ChangeChanged, domain.ChangeRiskHigh, "workspace.git.dirty", "The workspace contains uncommitted changes."))
	}
	return items
}

func compareServices(fromValues, toValues []revision.ServiceFact, manifestChanged bool) []Item {
	from, to := serviceMap(fromValues), serviceMap(toValues)
	keys := unionServiceKeys(from, to)
	items := make([]Item, 0)
	for _, key := range keys {
		left, leftOK := from[key]
		right, rightOK := to[key]
		switch {
		case !leftOK:
			items = append(items, item(domain.ChangeItemService, ChangeAdded, domain.ChangeRiskLow, key, "A managed service was added."))
		case !rightOK:
			risk := domain.ChangeRiskHigh
			if left.Required {
				risk = domain.ChangeRiskBlocked
			}
			items = append(items, item(domain.ChangeItemService, ChangeRemoved, risk, key, "A managed service was removed."))
		default:
			items = append(items, compareService(left, right, manifestChanged)...)
		}
		if rightOK && right.Required && right.Mode == domain.ProcessDaemon && right.HealthCoverage != domain.HealthCoverageBusiness && right.HealthCoverage != domain.HealthCoverageContainer {
			items = append(items, item(domain.ChangeItemHealth, ChangeChanged, domain.ChangeRiskBlocked, key+".health", "A required daemon lacks verifiable liveness coverage."))
		}
	}
	return items
}

func compareService(left, right revision.ServiceFact, manifestChanged bool) []Item {
	result := make([]Item, 0)
	key := right.ServiceID.String()
	if left.Driver != right.Driver || left.Mode != right.Mode || left.Required != right.Required {
		result = append(result, item(domain.ChangeItemService, ChangeChanged, domain.ChangeRiskHigh, key+".runtime", "The service runtime contract changed."))
	}
	if fmt.Sprint(left.Dependencies) != fmt.Sprint(right.Dependencies) {
		result = append(result, item(domain.ChangeItemDependency, ChangeChanged, domain.ChangeRiskHigh, key+".dependencies", "The service dependency graph changed."))
	}
	if manifestChanged && (left.CommandDigest != right.CommandDigest || left.DefinitionDigest != right.DefinitionDigest) {
		result = append(result, item(domain.ChangeItemService, ChangeChanged, domain.ChangeRiskMedium, key+".definition", "The service launch definition changed."))
	}
	if left.ComposeDigest != right.ComposeDigest {
		result = append(result, item(domain.ChangeItemCompose, ChangeChanged, domain.ChangeRiskHigh, key+".compose", "The Compose service or image identity changed."))
	} else if composeImageIdentityUnknown(left.Images, right.Images) {
		result = append(result, item(domain.ChangeItemCompose, ChangeChanged, domain.ChangeRiskHigh, key+".compose-images", "The Compose image identity is not comparable."))
	} else if fmt.Sprint(left.Images) != fmt.Sprint(right.Images) {
		result = append(result, item(domain.ChangeItemCompose, ChangeChanged, domain.ChangeRiskHigh, key+".compose-images", "The Compose image identity changed."))
	}
	if left.HealthCoverage != right.HealthCoverage {
		result = append(result, item(domain.ChangeItemHealth, ChangeChanged, domain.ChangeRiskHigh, key+".health-coverage", "The service health coverage changed."))
	}
	if left.RestartPolicy != right.RestartPolicy {
		result = append(result, item(domain.ChangeItemRestart, ChangeChanged, domain.ChangeRiskMedium, key+".restart", "The service restart policy changed."))
	}
	return result
}

func compareRunners(fromValues, toValues []revision.RunnerFact) []Item {
	from, to := runnerMap(fromValues), runnerMap(toValues)
	items := make([]Item, 0)
	for _, key := range unionRunnerKeys(from, to) {
		left, leftOK := from[key]
		right, rightOK := to[key]
		if !rightOK || right.Status != revision.SourceAvailable {
			items = append(items, item(domain.ChangeItemRunner, ChangeChanged, domain.ChangeRiskHigh, key, "The candidate Runner identity is unavailable."))
		} else if !leftOK || left.ExecutableDigest != right.ExecutableDigest || left.Version != right.Version {
			items = append(items, item(domain.ChangeItemRunner, ChangeChanged, domain.ChangeRiskHigh, key, "The resolved Runner identity changed."))
		}
	}
	return items
}

func compareFiles(fromValues, toValues []revision.FileFact) []Item {
	if len(fromValues) == 0 && len(toValues) > 0 {
		return []Item{item(domain.ChangeItemDependencyFile, ChangeChanged, domain.ChangeRiskHigh, "files.launch-identity", "Launch-time dependency file identities were not recorded.")}
	}
	from, to := fileMap(fromValues), fileMap(toValues)
	items := make([]Item, 0)
	for _, key := range unionFileKeys(from, to) {
		left, leftOK := from[key]
		right, rightOK := to[key]
		if leftOK && rightOK && left.Digest == right.Digest {
			continue
		}
		change := ChangeChanged
		if !leftOK {
			change = ChangeAdded
		} else if !rightOK {
			change = ChangeRemoved
		}
		risk := domain.ChangeRiskMedium
		if right.Kind == "registered" || left.Kind == "registered" {
			risk = domain.ChangeRiskHigh
		}
		items = append(items, item(domain.ChangeItemDependencyFile, change, risk, key, "An allowlisted dependency or registered file changed."))
	}
	return items
}

func compareSecrets(fromValues, toValues []revision.SecretFact) []Item {
	if len(fromValues) > 0 && len(toValues) == 0 {
		return []Item{item(domain.ChangeItemSecret, ChangeChanged, domain.ChangeRiskHigh, "secrets.candidate-identity", "Candidate Secret version metadata is unavailable.")}
	}
	from, to := secretMap(fromValues), secretMap(toValues)
	items := make([]Item, 0)
	for _, key := range unionStringKeys(from, to) {
		left, leftOK := from[key]
		right, rightOK := to[key]
		if leftOK && rightOK && left.Version == right.Version && left.Provider == right.Provider {
			continue
		}
		items = append(items, item(domain.ChangeItemSecret, ChangeChanged, domain.ChangeRiskMedium, key, "Secret metadata changed without exposing its value."))
	}
	return items
}

func composeImageIdentityUnknown(left, right []revision.ComposeImageFact) bool {
	if len(left) != len(right) || len(left) == 0 {
		return len(left) != len(right)
	}
	for index := range left {
		if left[index].Status != revision.SourceAvailable || right[index].Status != revision.SourceAvailable {
			return true
		}
	}
	return false
}

func aggregateRisk(items []Item) (domain.ChangeRisk, int) {
	risk, rank, blocked := domain.ChangeRiskInfo, 0, 0
	for _, value := range items {
		current := riskRank(value.Risk)
		if current > rank {
			risk, rank = value.Risk, current
		}
		if value.Risk == domain.ChangeRiskBlocked {
			blocked++
		}
	}
	return risk, blocked
}

func riskRank(value domain.ChangeRisk) int {
	switch value {
	case domain.ChangeRiskLow:
		return 1
	case domain.ChangeRiskMedium:
		return 2
	case domain.ChangeRiskHigh:
		return 3
	case domain.ChangeRiskBlocked:
		return 4
	default:
		return 0
	}
}

func item(kind domain.ChangeItemKind, change Change, risk domain.ChangeRisk, key, summary string) Item {
	return Item{Kind: kind, Change: change, Risk: risk, Key: key, Summary: summary}
}
