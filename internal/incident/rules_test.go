package incident

import (
	"strings"
	"testing"
	"time"

	"stackpilot/internal/domain"
)

func TestBuildContextRedactsBoundsAndFoldsLogs(t *testing.T) {
	now := time.Date(2026, 8, 18, 8, 0, 0, 0, time.UTC)
	line := LogLine{ServiceInstanceID: domain.ServiceInstanceID("svi_01ARZ3NDEKTSV4RRFFQ69G5FAV"), Sequence: 1, Timestamp: now, Stream: "stderr", Message: "token=secret"}
	context, err := BuildContext(Context{Kind: KindKnownLogError}, now, []LogLine{line, line}, replacingRedactor{})
	if err != nil || len(context.Logs) != 1 || context.Logs[0].RepeatCount != 2 || strings.Contains(context.Logs[0].Message, "secret") {
		t.Fatalf("BuildContext() = (%#v, %v)", context, err)
	}
}

func TestRuleEngineProducesTraceableNonAutomaticResults(t *testing.T) {
	evidence := []EvidenceRef{{Type: "event", EventID: 42}}
	results := NewRuleEngine().Analyze(Context{TriggerCode: "PORT_CONFLICT", Evidence: evidence, Logs: []LogLine{{Message: "Node EADDRINUSE"}}})
	if len(results) < 2 || results[0].RuleID != "port-conflict" || results[0].Suggestions[0].Automatic || results[0].Evidence[0].EventID != 42 {
		t.Fatalf("Analyze() = %#v", results)
	}
}

type replacingRedactor struct{}

func (replacingRedactor) Redact(value string) (string, error) {
	return strings.ReplaceAll(value, "secret", "[REDACTED]"), nil
}
