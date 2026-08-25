package incident

import "strings"

// DiagnosticRule maps one bounded context to a deterministic result.
type DiagnosticRule interface {
	ID() string
	Match(Context) (RuleResult, bool)
}

// RuleEngine evaluates stable rules in specificity order and removes duplicate advice.
type RuleEngine struct{ rules []DiagnosticRule }

// NewRuleEngine constructs the Phase 2E deterministic rule set.
func NewRuleEngine() *RuleEngine {
	return &RuleEngine{rules: []DiagnosticRule{
		codeRule{id: "port-conflict", codes: []string{"PORT_CONFLICT", "EADDRINUSE"}, title: "Port is already in use", cause: "The planned loopback port is owned by another listener.", action: "review-port-owner"},
		codeRule{id: "identity-mismatch", codes: []string{"PROCESS_IDENTITY_MISMATCH"}, title: "Process identity changed", cause: "The persisted runtime identity no longer matches the observed process.", action: "review-process-identity"},
		codeRule{id: "readiness-refused", codes: []string{"TCP_REFUSED"}, title: "Readiness connection was refused", cause: "The service did not accept connections on its planned readiness endpoint.", action: "rerun-readiness"},
		codeRule{id: "readiness-timeout", codes: []string{"HEALTH_READINESS_TIMEOUT"}, title: "Readiness timed out", cause: "The service did not satisfy its readiness threshold before the configured deadline.", action: "inspect-readiness-evidence"},
		codeRule{id: "http-status", codes: []string{"HTTP_STATUS_MISMATCH"}, title: "Health endpoint returned an unexpected status", cause: "The local health endpoint responded outside the accepted success range.", action: "inspect-health-endpoint"},
		codeRule{id: "process-exit", codes: []string{"PROCESS_EXITED", "SUPERVISOR_EXITED", "CONTAINER_UNHEALTHY"}, title: "Managed process exited", cause: "The managed daemon exited while the system expected it to remain running.", action: "inspect-exit-evidence"},
		knownLogRule{},
	}}
}

// Analyze returns all matching diagnoses in stable priority order.
func (engine *RuleEngine) Analyze(context Context) []RuleResult {
	results := make([]RuleResult, 0)
	seen := make(map[string]bool)
	for _, rule := range engine.rules {
		result, matched := rule.Match(context)
		if !matched || seen[result.RuleID] {
			continue
		}
		seen[result.RuleID] = true
		results = append(results, result)
	}
	return results
}

type codeRule struct {
	id, title, cause, action string
	codes                    []string
}

func (rule codeRule) ID() string { return rule.id }

func (rule codeRule) Match(context Context) (RuleResult, bool) {
	for _, code := range rule.codes {
		if strings.EqualFold(context.TriggerCode, code) || contextLogsContain(context, code) {
			return newRuleResult(rule.id, rule.title, rule.cause, rule.action, context.Evidence), true
		}
	}
	return RuleResult{}, false
}

type knownLogRule struct{}

func (knownLogRule) ID() string { return "known-startup-log" }

func (knownLogRule) Match(context Context) (RuleResult, bool) {
	patterns := []string{"address already in use", "eaddrinuse", "bindexception", "modulenotfounderror", "could not find or load main class"}
	for _, pattern := range patterns {
		if contextLogsContain(context, pattern) {
			return newRuleResult("known-startup-log", "Known startup error", "A recognized Java, Node, or Python startup failure appears in the redacted log evidence.", "inspect-startup-configuration", context.Evidence), true
		}
	}
	return RuleResult{}, false
}

func contextLogsContain(context Context, value string) bool {
	value = strings.ToLower(value)
	for _, line := range context.Logs {
		if strings.Contains(strings.ToLower(line.Message), value) {
			return true
		}
	}
	return false
}

func newRuleResult(id, title, cause, action string, evidence []EvidenceRef) RuleResult {
	return RuleResult{
		RuleID: id, Title: title, Cause: cause, Confidence: 100,
		Evidence:    append([]EvidenceRef(nil), evidence...),
		Suggestions: []Suggestion{{Action: action, Description: title, Automatic: false}},
	}
}
