package main

import (
	"encoding/json"
	"net/url"
	"path/filepath"
	"runtime"
	"testing"

	"stackpilot/internal/domain"
)

func TestFinalizeRequiresAvailableProcessAndComposeSources(t *testing.T) {
	report := gateReport{Services: []serviceReport{
		{SystemID: "btc", ServiceID: "backend", Driver: domain.DriverProcess, MetricStatus: string(domain.MetricAvailable)},
		{SystemID: "aiws", ServiceID: "infrastructure", Driver: domain.DriverCompose, MetricStatus: string(domain.MetricAvailable)},
	}}
	finalize(&report)
	if report.GateStatus != "passed" || len(report.Blockers) != 0 {
		t.Fatalf("finalize() = status %q, blockers %v", report.GateStatus, report.Blockers)
	}

	report = gateReport{Services: []serviceReport{{
		SystemID: "btc", ServiceID: "backend", Driver: domain.DriverProcess,
		MetricStatus: string(domain.MetricUnsupported), ReasonCode: "SUPERVISOR_PROTOCOL_UNSUPPORTED",
	}}}
	finalize(&report)
	if report.GateStatus != "blocked" || len(report.Blockers) != 3 {
		t.Fatalf("finalize() = status %q, blockers %v", report.GateStatus, report.Blockers)
	}
}

func TestReadOnlyDSNEnforcesModeAndQueryOnly(t *testing.T) {
	path := filepath.Join(string(filepath.Separator), "tmp", "stackpilot.db")
	if runtime.GOOS == "windows" {
		path = `C:\StackPilot\stackpilot.db`
	}
	parsed, err := url.Parse(readOnlyDSN(path))
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	if parsed.Query().Get("mode") != "ro" {
		t.Fatalf("mode = %q, want ro", parsed.Query().Get("mode"))
	}
	values := parsed.Query()["_pragma"]
	if len(values) != 2 || values[0] != "query_only(ON)" || values[1] != "busy_timeout(5000)" {
		t.Fatalf("pragmas = %v", values)
	}
}

func TestEvidenceDTOOmitsRuntimeIdentity(t *testing.T) {
	encoded, err := json.Marshal(serviceReport{
		SystemID: "btc", ServiceID: "backend", Driver: domain.DriverProcess,
		RuntimeState: domain.ServiceReady, MetricStatus: string(domain.MetricUnavailable),
		ReasonCode: "SUPERVISOR_UNAVAILABLE",
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	for _, forbidden := range []string{"pid", "executablePath", "commandDigest", "platformToken", "composeIdentity"} {
		if _, exists := fields[forbidden]; exists {
			t.Fatalf("evidence unexpectedly contains %q", forbidden)
		}
	}
}
