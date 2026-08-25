package manifest

func validateDependencyGraph(services map[string]Service) error {
	for serviceID, service := range services {
		for dependency := range service.DependsOn {
			if dependency == serviceID {
				return newValidationError("$.spec.services."+serviceID+".dependsOn", dependency, ErrDependencyCycle)
			}
			if _, ok := services[dependency]; !ok {
				return newValidationError("$.spec.services."+serviceID+".dependsOn", dependency, ErrReferenceNotFound)
			}
		}
	}
	states := make(map[string]uint8, len(services))
	for serviceID := range services {
		if states[serviceID] == 0 && dependencyCycleFrom(serviceID, services, states) {
			return newValidationError("$.spec.services", serviceID, ErrDependencyCycle)
		}
	}
	return nil
}

func dependencyCycleFrom(serviceID string, services map[string]Service, states map[string]uint8) bool {
	states[serviceID] = 1
	for dependency := range services[serviceID].DependsOn {
		if states[dependency] == 1 {
			return true
		}
		if states[dependency] == 0 && dependencyCycleFrom(dependency, services, states) {
			return true
		}
	}
	states[serviceID] = 2
	return false
}
