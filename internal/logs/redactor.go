package logs

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// RedactionRule is one bounded user-configured regular expression replacement.
type RedactionRule struct {
	Pattern string
	Type    string
}

type compiledRule struct {
	pattern     *regexp.Regexp
	replacement string
}

// DefaultRedactor applies built-in credential rules followed by configured rules.
type DefaultRedactor struct {
	rules []compiledRule
}

type captureRedactor struct {
	base   Redactor
	values [][]byte
}

// NewDefaultRedactor compiles all rules before any log content is processed.
func NewDefaultRedactor(custom []RedactionRule) (*DefaultRedactor, error) {
	specifications := []RedactionRule{
		{Pattern: `(?i)(authorization\s*[:=]\s*)(?:bearer\s+)?[^\s,;]+`, Type: "authorization"},
		{Pattern: `(?i)(cookie\s*[:=]\s*)[^\r\n]+`, Type: "cookie"},
		{Pattern: `(?i)([?&](?:access_token|token|api_key|password|secret)=)[^&\s]+`, Type: "query"},
		{Pattern: `(?i)((?:password|pwd|user id|uid)\s*=\s*)[^;\r\n]+`, Type: "connection"},
		{Pattern: `(?i)(bearer\s+)[A-Za-z0-9._~+/-]+=*`, Type: "bearer"},
	}
	specifications = append(specifications, custom...)
	rules := make([]compiledRule, 0, len(specifications))
	for _, specification := range specifications {
		if specification.Type == "" || len(specification.Type) > 32 {
			return nil, fmt.Errorf("%w: redaction type", ErrInvalidConfig)
		}
		pattern, err := regexp.Compile(specification.Pattern)
		if err != nil {
			return nil, fmt.Errorf("%w: redaction rule: %v", ErrInvalidConfig, err)
		}
		rules = append(rules, compiledRule{pattern: pattern, replacement: `${1}[REDACTED:` + specification.Type + `]`})
	}
	return &DefaultRedactor{rules: rules}, nil
}

// Redact applies every compiled rule in deterministic order.
func (redactor *DefaultRedactor) Redact(message string) (string, error) {
	result := message
	for _, rule := range redactor.rules {
		result = rule.pattern.ReplaceAllString(result, rule.replacement)
	}
	return result, nil
}

func newCaptureRedactor(base Redactor, values [][]byte) (*captureRedactor, error) {
	if base == nil {
		return nil, fmt.Errorf("%w: base redactor", ErrInvalidConfig)
	}
	result := &captureRedactor{base: base, values: make([][]byte, 0, len(values))}
	for _, value := range values {
		if len(value) == 0 {
			result.clear()
			return nil, fmt.Errorf("%w: empty Secret redaction value", ErrInvalidConfig)
		}
		result.values = append(result.values, append([]byte(nil), value...))
	}
	sort.Slice(result.values, func(left, right int) bool { return len(result.values[left]) > len(result.values[right]) })
	return result, nil
}

func (redactor *captureRedactor) Redact(message string) (string, error) {
	result, err := redactor.base.Redact(message)
	if err != nil {
		return "", err
	}
	for _, value := range redactor.values {
		result = strings.ReplaceAll(result, string(value), "[REDACTED:secret]")
	}
	return result, nil
}

func (redactor *captureRedactor) clear() {
	for _, value := range redactor.values {
		for index := range value {
			value[index] = 0
		}
	}
	redactor.values = nil
}
