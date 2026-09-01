package main

import "testing"

func TestClassifyRequiresBusinessOrContainerLiveness(t *testing.T) {
	ok := true
	service := serviceReport{Driver: "process", Mode: "daemon", Required: true, RuntimeState: "ready", LatestLivenessKind: "http", LatestLivenessOK: &ok}
	classify(&service)
	if service.Coverage != "business" || !service.SatisfiesVerification {
		t.Fatalf("service = %#v", service)
	}
	service.LatestLivenessKind = "process"
	classify(&service)
	if service.Coverage != "process-only" || service.SatisfiesVerification {
		t.Fatalf("process-only service = %#v", service)
	}
}

func TestFinalizeRequiresFiveSystemsAndAllRequiredServices(t *testing.T) {
	result := report{Services: []serviceReport{{SystemID: "a", ServiceID: "api", Required: true, Mode: "daemon", SatisfiesVerification: true}}}
	finalize(&result)
	if result.GateStatus != "blocked" || len(result.Blockers) != 1 || result.Blockers[0] != "ACTIVE_SYSTEM_COUNT" {
		t.Fatalf("result = %#v", result)
	}
}
