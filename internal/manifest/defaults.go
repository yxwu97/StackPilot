package manifest

func applyDefaults(manifest *Manifest) {
	manifest.Spec.Capabilities = nonNilMap(manifest.Spec.Capabilities)
	manifest.Spec.Ports = nonNilMap(manifest.Spec.Ports)
	setBoolDefault(&manifest.Spec.Policies.FailFast, true)
	setBoolDefault(&manifest.Spec.Policies.CleanupOnFailure, false)
	setBoolDefault(&manifest.Spec.Policies.KeepReadyServices, true)
	setBoolDefault(&manifest.Spec.Policies.StickyPorts, true)
	setStringDefault(&manifest.Spec.Policies.StartTimeout, "10m")
	setStringDefault(&manifest.Spec.Policies.StopTimeout, "2m")
	for name, port := range manifest.Spec.Ports {
		setStringDefault(&port.ConflictPolicy, "auto")
		setStringDefault(&port.Exposure, "loopback")
		manifest.Spec.Ports[name] = port
	}
	for name, service := range manifest.Spec.Services {
		applyServiceDefaults(&service)
		manifest.Spec.Services[name] = service
	}
}

func applyServiceDefaults(service *Service) {
	setBoolDefault(&service.Required, true)
	setStringDefault(&service.Mode, "daemon")
	service.Environment = nonNilMap(service.Environment)
	service.DependsOn = nonNilMap(service.DependsOn)
	setStringDefault(&service.Stop.GracefulTimeout, "15s")
	setStringDefault(&service.Restart.Policy, "never")
	setStringDefault(&service.Restart.InitialBackoff, "1s")
	setStringDefault(&service.Restart.MaxBackoff, "1m")
	setIntDefault(&service.Restart.MaxAttempts, 3)
	setStringDefault(&service.Restart.StableWindow, "5m")
	applyHealthDefaults(service.Readiness)
	applyHealthDefaults(service.Liveness)
	if service.Compose != nil {
		service.Compose.Ports = nonNilMap(service.Compose.Ports)
		service.Compose.Environment = nonNilMap(service.Compose.Environment)
	}
}

func applyHealthDefaults(health *HealthCheck) {
	if health == nil {
		return
	}
	setIntDefault(&health.SuccessThreshold, 1)
	setIntDefault(&health.FailureThreshold, 1)
}

func setBoolDefault(target **bool, value bool) {
	if *target == nil {
		copy := value
		*target = &copy
	}
}

func setIntDefault(target **int, value int) {
	if *target == nil {
		copy := value
		*target = &copy
	}
}

func setStringDefault(target *string, value string) {
	if *target == "" {
		*target = value
	}
}

func nonNilMap[K comparable, V any](value map[K]V) map[K]V {
	if value == nil {
		return make(map[K]V)
	}
	return value
}

func cloneManifest(source Manifest) Manifest {
	result := source
	result.Spec.Capabilities = cloneMap(source.Spec.Capabilities)
	result.Spec.Ports = make(map[string]Port, len(source.Spec.Ports))
	for name, port := range source.Spec.Ports {
		port.Preferred = clonePointer(port.Preferred)
		result.Spec.Ports[name] = port
	}
	result.Spec.Services = make(map[string]Service, len(source.Spec.Services))
	for name, service := range source.Spec.Services {
		service.Arguments = append([]string(nil), service.Arguments...)
		service.Environment = cloneMap(service.Environment)
		service.DependsOn = cloneMap(service.DependsOn)
		service.Required = clonePointer(service.Required)
		service.Readiness = cloneHealth(service.Readiness)
		service.Liveness = cloneHealth(service.Liveness)
		service.Compose = cloneCompose(service.Compose)
		service.Restart.MaxAttempts = clonePointer(service.Restart.MaxAttempts)
		result.Spec.Services[name] = service
	}
	result.Spec.Policies.FailFast = clonePointer(source.Spec.Policies.FailFast)
	result.Spec.Policies.CleanupOnFailure = clonePointer(source.Spec.Policies.CleanupOnFailure)
	result.Spec.Policies.KeepReadyServices = clonePointer(source.Spec.Policies.KeepReadyServices)
	result.Spec.Policies.StickyPorts = clonePointer(source.Spec.Policies.StickyPorts)
	return result
}

func cloneCompose(source *ComposeService) *ComposeService {
	if source == nil {
		return nil
	}
	result := *source
	result.Services = append([]string(nil), source.Services...)
	result.Readiness = cloneMap(source.Readiness)
	result.Ports = cloneMap(source.Ports)
	result.Environment = make(map[string]map[string]string, len(source.Environment))
	for service, environment := range source.Environment {
		result.Environment[service] = cloneMap(environment)
	}
	return &result
}

func cloneHealth(source *HealthCheck) *HealthCheck {
	if source == nil {
		return nil
	}
	result := *source
	result.SuccessThreshold = clonePointer(source.SuccessThreshold)
	result.FailureThreshold = clonePointer(source.FailureThreshold)
	return &result
}

func clonePointer[T any](source *T) *T {
	if source == nil {
		return nil
	}
	copy := *source
	return &copy
}

func cloneMap[K comparable, V any](source map[K]V) map[K]V {
	if source == nil {
		return nil
	}
	result := make(map[K]V, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
