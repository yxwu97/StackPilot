package manifest

import "strings"

func validatePortOwners(ports map[string]Port, services map[string]Service) error {
	owners := make(map[string][]string, len(ports))
	for serviceID, service := range services {
		if service.Compose != nil {
			for logicalName := range service.Compose.Ports {
				owners[logicalName] = append(owners[logicalName], serviceID)
			}
		}
		if service.Readiness == nil {
			continue
		}
		for logicalName := range ports {
			token := "${ports." + logicalName + "}"
			if readinessOwnsPort(*service.Readiness, token) {
				owners[logicalName] = append(owners[logicalName], serviceID)
			}
		}
	}
	for logicalName := range ports {
		if len(owners[logicalName]) != 1 {
			return newValidationError("$.spec.ports."+logicalName, "owner", ErrSemanticInvalid)
		}
	}
	return nil
}

func readinessOwnsPort(readiness HealthCheck, token string) bool {
	if readiness.Type == "http" {
		return strings.Contains(readiness.URL, token)
	}
	if readiness.Type == "tcp" {
		value, ok := readiness.Port.(string)
		return ok && value == token
	}
	return false
}
