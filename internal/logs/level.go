package logs

import (
	"encoding/json"
	"regexp"
	"strings"
)

var levelPrefix = regexp.MustCompile(`(?i)^\s*(?:\[)?(trace|debug|info|warn|warning|error|fatal)(?:\])?(?:\s|:|-|$)`)

func detectLevel(message string) string {
	var object map[string]json.RawMessage
	if json.Unmarshal([]byte(message), &object) == nil {
		for key, raw := range object {
			if strings.EqualFold(key, "level") {
				var value string
				if json.Unmarshal(raw, &value) == nil {
					return normalizeLevel(value)
				}
			}
		}
	}
	matches := levelPrefix.FindStringSubmatch(message)
	if len(matches) == 2 {
		return normalizeLevel(matches[1])
	}
	return "unknown"
}

func normalizeLevel(value string) string {
	switch strings.ToLower(value) {
	case "trace", "debug", "info", "error", "fatal":
		return strings.ToLower(value)
	case "warn", "warning":
		return "warn"
	default:
		return "unknown"
	}
}
