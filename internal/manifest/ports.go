package manifest

import (
	"strconv"
	"strings"

	"stackpilot/internal/domain"
)

const (
	minimumPort       = 1024
	maximumPort       = 65535
	maximumCandidates = 2000
)

func validatePorts(ports map[string]Port) (map[string]PortRange, error) {
	result := make(map[string]PortRange)
	for name, port := range ports {
		path := "$.spec.ports." + name
		if _, err := domain.ParseServiceID(name); err != nil {
			return nil, newValidationError("$.spec.ports", name, ErrSemanticInvalid)
		}
		if port.Preferred != nil && (*port.Preferred < minimumPort || *port.Preferred > maximumPort) {
			return nil, newValidationError(path, "preferred", ErrPortRangeInvalid)
		}
		if port.FallbackRange == "" {
			continue
		}
		parsed, err := parsePortRange(port.FallbackRange)
		if err != nil {
			return nil, newValidationError(path, "fallbackRange", ErrPortRangeInvalid)
		}
		result[name] = parsed
	}
	return result, nil
}

func parsePortRange(value string) (PortRange, error) {
	parts := strings.Split(value, "-")
	if len(parts) != 2 {
		return PortRange{}, ErrPortRangeInvalid
	}
	start, startErr := strconv.Atoi(parts[0])
	end, endErr := strconv.Atoi(parts[1])
	if startErr != nil || endErr != nil || start < minimumPort || end > maximumPort || start > end {
		return PortRange{}, ErrPortRangeInvalid
	}
	if end-start+1 > maximumCandidates {
		return PortRange{}, ErrPortRangeInvalid
	}
	return PortRange{Start: start, End: end}, nil
}
