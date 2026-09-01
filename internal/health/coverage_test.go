package health

import (
	"testing"
	"time"

	"stackpilot/internal/domain"
)

func TestSummarizeCoverageKeepsProcessOnlyBlocked(t *testing.T) {
	liveness := &ResolvedSpec{Kind: KindProcess}
	latest := &Result{Purpose: PurposeLiveness, Kind: KindProcess, Success: true, CheckedAt: time.Now().UTC()}
	summary := SummarizeCoverage(CoverageInput{
		Driver: domain.DriverProcess, Mode: domain.ProcessDaemon, Required: true, State: domain.ServiceReady,
		ReadinessKind: KindProcess, Liveness: liveness, Latest: latest,
	})
	if summary.Coverage != domain.HealthCoverageProcessOnly || summary.SatisfiesVerification {
		t.Fatalf("process-only summary = %#v", summary)
	}
}

func TestSummarizeCoverageRequiresLatestLivenessSuccess(t *testing.T) {
	liveness := &ResolvedSpec{Kind: KindHTTP}
	readinessResult := &Result{Purpose: PurposeReadiness, Kind: KindHTTP, Success: true}
	summary := SummarizeCoverage(CoverageInput{
		Driver: domain.DriverProcess, Mode: domain.ProcessDaemon, Required: true, State: domain.ServiceReady,
		ReadinessKind: KindHTTP, Liveness: liveness, Latest: readinessResult,
	})
	if summary.Coverage != domain.HealthCoverageBusiness || summary.Latest != nil || summary.SatisfiesVerification {
		t.Fatalf("readiness-only summary = %#v", summary)
	}
	livenessResult := *readinessResult
	livenessResult.Purpose = PurposeLiveness
	summary = SummarizeCoverage(CoverageInput{
		Driver: domain.DriverProcess, Mode: domain.ProcessDaemon, Required: true, State: domain.ServiceReady,
		ReadinessKind: KindHTTP, Liveness: liveness, Latest: &livenessResult,
	})
	if !summary.SatisfiesVerification {
		t.Fatalf("business liveness summary = %#v", summary)
	}
}

func TestSummarizeCoverageRequiresCompletedOneshot(t *testing.T) {
	summary := SummarizeCoverage(CoverageInput{Mode: domain.ProcessOneshot, Required: true, State: domain.ServiceCompleted})
	if !summary.SatisfiesVerification {
		t.Fatalf("completed oneshot summary = %#v", summary)
	}
}
