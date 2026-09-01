package domain

// MetricSource identifies the trusted runtime owner that produced a sample.
type MetricSource string

const (
	MetricSourceProcessJob MetricSource = "process-job"
	MetricSourceCompose    MetricSource = "compose"
)

func (value MetricSource) Valid() bool {
	return value == MetricSourceProcessJob || value == MetricSourceCompose
}

func (value MetricSource) Validate() error {
	return validateEnum("metricSource", string(value), value.Valid())
}

// MetricStatus distinguishes real measurements from explicit missing data.
type MetricStatus string

const (
	MetricAvailable   MetricStatus = "available"
	MetricUnavailable MetricStatus = "unavailable"
	MetricUnsupported MetricStatus = "unsupported"
)

func (value MetricStatus) Valid() bool {
	return value == MetricAvailable || value == MetricUnavailable || value == MetricUnsupported
}

func (value MetricStatus) Validate() error {
	return validateEnum("metricStatus", string(value), value.Valid())
}

// RevisionKind separates launch-time runtime facts from current workspace facts.
type RevisionKind string

const (
	RevisionRunning   RevisionKind = "running"
	RevisionWorkspace RevisionKind = "workspace"
)

func (value RevisionKind) Valid() bool {
	return value == RevisionRunning || value == RevisionWorkspace
}

func (value RevisionKind) Validate() error {
	return validateEnum("revisionKind", string(value), value.Valid())
}

// HealthCoverage is the strongest verified health signal available for a service.
type HealthCoverage string

const (
	HealthCoverageBusiness    HealthCoverage = "business"
	HealthCoverageContainer   HealthCoverage = "container"
	HealthCoverageProcessOnly HealthCoverage = "process-only"
	HealthCoverageUnavailable HealthCoverage = "unavailable"
)

func (value HealthCoverage) Valid() bool {
	switch value {
	case HealthCoverageBusiness, HealthCoverageContainer, HealthCoverageProcessOnly, HealthCoverageUnavailable:
		return true
	default:
		return false
	}
}

func (value HealthCoverage) Validate() error {
	return validateEnum("healthCoverage", string(value), value.Valid())
}

// ChangePlanState is the immutable usability outcome of a completed plan.
type ChangePlanState string

const (
	ChangePlanReady   ChangePlanState = "ready"
	ChangePlanBlocked ChangePlanState = "blocked"
)

func (value ChangePlanState) Valid() bool {
	return value == ChangePlanReady || value == ChangePlanBlocked
}

func (value ChangePlanState) Validate() error {
	return validateEnum("changePlanState", string(value), value.Valid())
}

// ChangeRisk is the deterministic risk assigned by the versioned rule set.
type ChangeRisk string

const (
	ChangeRiskInfo    ChangeRisk = "info"
	ChangeRiskLow     ChangeRisk = "low"
	ChangeRiskMedium  ChangeRisk = "medium"
	ChangeRiskHigh    ChangeRisk = "high"
	ChangeRiskBlocked ChangeRisk = "blocked"
)

func (value ChangeRisk) Valid() bool {
	switch value {
	case ChangeRiskInfo, ChangeRiskLow, ChangeRiskMedium, ChangeRiskHigh, ChangeRiskBlocked:
		return true
	default:
		return false
	}
}

func (value ChangeRisk) Validate() error {
	return validateEnum("changeRisk", string(value), value.Valid())
}

// ChangeItemKind identifies the bounded fact family changed between revisions.
type ChangeItemKind string

const (
	ChangeItemManifest       ChangeItemKind = "manifest"
	ChangeItemService        ChangeItemKind = "service"
	ChangeItemDependency     ChangeItemKind = "dependency"
	ChangeItemRunner         ChangeItemKind = "runner"
	ChangeItemPort           ChangeItemKind = "port"
	ChangeItemHealth         ChangeItemKind = "health"
	ChangeItemRestart        ChangeItemKind = "restart"
	ChangeItemDependencyFile ChangeItemKind = "dependency-file"
	ChangeItemCompose        ChangeItemKind = "compose"
	ChangeItemSecret         ChangeItemKind = "secret"
)

func (value ChangeItemKind) Valid() bool {
	switch value {
	case ChangeItemManifest, ChangeItemService, ChangeItemDependency, ChangeItemRunner, ChangeItemPort,
		ChangeItemHealth, ChangeItemRestart, ChangeItemDependencyFile, ChangeItemCompose, ChangeItemSecret:
		return true
	default:
		return false
	}
}

func (value ChangeItemKind) Validate() error {
	return validateEnum("changeItemKind", string(value), value.Valid())
}

// VerificationResult records whether the post-start stability contract passed.
type VerificationResult string

const (
	VerificationPassed VerificationResult = "passed"
	VerificationFailed VerificationResult = "failed"
)

func (value VerificationResult) Valid() bool {
	return value == VerificationPassed || value == VerificationFailed
}

func (value VerificationResult) Validate() error {
	return validateEnum("verificationResult", string(value), value.Valid())
}
