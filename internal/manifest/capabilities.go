package manifest

func (validator *Validator) validateServiceCapabilities(service Service, path string) error {
	if service.Runner == "go" && !validator.enabled["go"] {
		return &FeatureError{Path: path + ".runner", Feature: "go"}
	}
	if service.Driver == "compose" && !validator.enabled["compose"] {
		return &FeatureError{Path: path + ".driver", Feature: "compose"}
	}
	if service.Compose != nil && EffectiveComposeBuildPolicy(*service.Compose) == "always" && !validator.enabled["compose-build"] {
		return &FeatureError{Path: path + ".compose.buildPolicy", Feature: "compose-build"}
	}
	if service.Liveness != nil && !validator.enabled["liveness"] {
		return &FeatureError{Path: path + ".liveness", Feature: "liveness"}
	}
	if service.Restart.Policy != "never" && !validator.enabled["auto-restart"] {
		return &FeatureError{Path: path + ".restart.policy", Feature: "auto-restart"}
	}
	if service.Readiness != nil && service.Readiness.Type == "compose" && !validator.enabled["compose"] {
		return &FeatureError{Path: path + ".readiness.type", Feature: "compose"}
	}
	return nil
}
