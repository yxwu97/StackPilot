package domain

import "testing"

func TestObservabilityEnumsRejectUnknownValues(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		validate func() error
	}{
		{name: "metric source", validate: func() error { return MetricSource("root-pid").Validate() }},
		{name: "metric status", validate: func() error { return MetricStatus("zero").Validate() }},
		{name: "revision kind", validate: func() error { return RevisionKind("current").Validate() }},
		{name: "health coverage", validate: func() error { return HealthCoverage("ready").Validate() }},
		{name: "plan state", validate: func() error { return ChangePlanState("stale").Validate() }},
		{name: "change risk", validate: func() error { return ChangeRisk("unknown").Validate() }},
		{name: "change item", validate: func() error { return ChangeItemKind("command").Validate() }},
		{name: "verification", validate: func() error { return VerificationResult("rolled-back").Validate() }},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if err := test.validate(); err == nil {
				t.Fatal("unknown enum value unexpectedly validated")
			}
		})
	}
}

func TestObservabilityEnumsAcceptClosedContracts(t *testing.T) {
	t.Parallel()
	for _, value := range []interface{ Valid() bool }{
		MetricSourceProcessJob, MetricSourceCompose,
		MetricAvailable, MetricUnavailable, MetricUnsupported,
		RevisionRunning, RevisionWorkspace,
		HealthCoverageBusiness, HealthCoverageContainer, HealthCoverageProcessOnly, HealthCoverageUnavailable,
		ChangePlanReady, ChangePlanBlocked,
		ChangeRiskInfo, ChangeRiskLow, ChangeRiskMedium, ChangeRiskHigh, ChangeRiskBlocked,
		ChangeItemManifest, ChangeItemService, ChangeItemDependency, ChangeItemRunner, ChangeItemPort,
		ChangeItemHealth, ChangeItemRestart, ChangeItemDependencyFile, ChangeItemCompose, ChangeItemSecret,
		VerificationPassed, VerificationFailed,
	} {
		if !value.Valid() {
			t.Fatalf("valid enum %#v was rejected", value)
		}
	}
}
