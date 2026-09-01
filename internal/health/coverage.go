package health

import "stackpilot/internal/domain"

// CoverageInput contains only resolved and persisted facts needed by verification gates.
type CoverageInput struct {
	Driver        domain.DriverKind
	Mode          domain.ProcessMode
	Required      bool
	State         domain.ServiceState
	ReadinessKind Kind
	Liveness      *ResolvedSpec
	Latest        *Result
}

// CoverageSummary is the safe health contract and latest liveness projection.
type CoverageSummary struct {
	ReadinessKind         Kind
	LivenessKind          Kind
	Coverage              domain.HealthCoverage
	Latest                *Result
	SatisfiesVerification bool
}

// SummarizeCoverage derives a conservative verification gate without inferring liveness from readiness.
func SummarizeCoverage(input CoverageInput) CoverageSummary {
	result := CoverageSummary{ReadinessKind: input.ReadinessKind, Coverage: coverageLevel(input)}
	if input.Liveness != nil {
		result.LivenessKind = input.Liveness.Kind
	}
	if input.Latest != nil && input.Latest.Purpose == PurposeLiveness {
		copy := *input.Latest
		result.Latest = &copy
	}
	result.SatisfiesVerification = coverageSatisfied(input, result)
	return result
}

func coverageLevel(input CoverageInput) domain.HealthCoverage {
	if input.Mode == domain.ProcessOneshot || input.Liveness == nil {
		return domain.HealthCoverageUnavailable
	}
	if input.Driver == domain.DriverCompose {
		return domain.HealthCoverageContainer
	}
	if input.Liveness.Kind == KindProcess {
		return domain.HealthCoverageProcessOnly
	}
	return domain.HealthCoverageBusiness
}

func coverageSatisfied(input CoverageInput, summary CoverageSummary) bool {
	if !input.Required {
		return true
	}
	if input.Mode == domain.ProcessOneshot {
		return input.State == domain.ServiceCompleted
	}
	if summary.Coverage != domain.HealthCoverageBusiness && summary.Coverage != domain.HealthCoverageContainer {
		return false
	}
	return input.State == domain.ServiceReady && summary.Latest != nil && summary.Latest.Success
}
