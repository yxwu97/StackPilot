package manifest

import (
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const defaultHealthInterval = "2s"

// EffectiveHealthTimeout returns the semantic timeout without mutating normalized manifests.
func EffectiveHealthTimeout(health HealthCheck, policies Policies) string {
	if health.Timeout != "" {
		return health.Timeout
	}
	return policies.StartTimeout
}

// EffectiveHealthInterval returns the semantic interval without mutating normalized manifests.
func EffectiveHealthInterval(health HealthCheck) string {
	if health.Interval != "" {
		return health.Interval
	}
	return defaultHealthInterval
}

func validateHealthCheck(health *HealthCheck, ports map[string]Port, policies Policies, path string) error {
	if health == nil {
		return newValidationError(path, "", ErrSemanticInvalid)
	}
	if err := validateHealthDurations(*health, policies, path); err != nil {
		return err
	}
	switch health.Type {
	case "process":
		if health.URL != "" || health.Host != "" || health.Port != nil {
			return newValidationError(path, "type", ErrSemanticInvalid)
		}
	case "http":
		return validateHTTPHealth(*health, ports, path)
	case "tcp":
		return validateTCPHealth(*health, ports, path)
	case "compose":
		if health.URL != "" || health.Host != "" || health.Port != nil {
			return newValidationError(path, "type", ErrSemanticInvalid)
		}
	default:
		return newValidationError(path, "type", ErrSemanticInvalid)
	}
	return nil
}

func validateHealthDurations(health HealthCheck, policies Policies, path string) error {
	timeout, timeoutSet, err := optionalPositiveDuration(EffectiveHealthTimeout(health, policies))
	if err != nil {
		return newValidationError(path, "timeout", ErrDurationInvalid)
	}
	interval, intervalSet, err := optionalPositiveDuration(EffectiveHealthInterval(health))
	if err != nil {
		return newValidationError(path, "interval", ErrDurationInvalid)
	}
	if timeoutSet && intervalSet {
		failureThreshold := time.Duration(*health.FailureThreshold)
		if interval > timeout || interval > timeout/failureThreshold {
			return newValidationError(path, "interval", ErrDurationInvalid)
		}
	}
	return nil
}

func validateHTTPHealth(health HealthCheck, ports map[string]Port, path string) error {
	if health.Host != "" || health.Port != nil {
		return newValidationError(path, "type", ErrSemanticInvalid)
	}
	rendered := renderTemplatesForValidation(health.URL)
	parsed, err := url.Parse(rendered)
	if err != nil || parsed.User != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
		return newValidationError(path, "url", ErrHealthTargetUnsafe)
	}
	if !loopbackHost(parsed.Hostname()) {
		return newValidationError(path, "url", ErrHealthTargetUnsafe)
	}
	if parsed.Port() != "" {
		port, err := strconv.Atoi(parsed.Port())
		if err != nil || port < minimumPort || port > maximumPort {
			return newValidationError(path, "url", ErrHealthTargetUnsafe)
		}
	}
	return validateTemplateValue(health.URL, ports, false, path+".url")
}

func validateTCPHealth(health HealthCheck, ports map[string]Port, path string) error {
	if health.URL != "" || !loopbackHost(health.Host) {
		return newValidationError(path, "host", ErrHealthTargetUnsafe)
	}
	switch value := health.Port.(type) {
	case int:
		if value < minimumPort || value > maximumPort {
			return newValidationError(path, "port", ErrPortRangeInvalid)
		}
	case string:
		if !isExactPortTemplate(value) {
			return newValidationError(path, "port", ErrTemplateInvalid)
		}
		if err := validateTemplateValue(value, ports, false, path+".port"); err != nil {
			return err
		}
	default:
		return newValidationError(path, "port", ErrSemanticInvalid)
	}
	return nil
}

func optionalPositiveDuration(value string) (time.Duration, bool, error) {
	if value == "" {
		return 0, false, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return 0, false, ErrDurationInvalid
	}
	return parsed, true, nil
}

func validatePolicyDurations(policies Policies) error {
	start, err := time.ParseDuration(policies.StartTimeout)
	if err != nil || start < time.Second || start > 30*time.Minute {
		return newValidationError("$.spec.policies", "startTimeout", ErrDurationInvalid)
	}
	stop, err := time.ParseDuration(policies.StopTimeout)
	if err != nil || stop < time.Second || stop > 10*time.Minute {
		return newValidationError("$.spec.policies", "stopTimeout", ErrDurationInvalid)
	}
	return nil
}

func validateServiceDurations(service Service, policies Policies, path string) error {
	graceful, err := time.ParseDuration(service.Stop.GracefulTimeout)
	if err != nil || graceful <= 0 {
		return newValidationError(path+".stop", "gracefulTimeout", ErrDurationInvalid)
	}
	stopTimeout, err := time.ParseDuration(policies.StopTimeout)
	if err != nil || graceful > stopTimeout {
		return newValidationError(path+".stop", "gracefulTimeout", ErrDurationInvalid)
	}
	return nil
}

func validateRestart(service Service, path string) error {
	initial, err := time.ParseDuration(service.Restart.InitialBackoff)
	if err != nil || initial < 100*time.Millisecond || initial > time.Minute {
		return newValidationError(path+".restart", "initialBackoff", ErrDurationInvalid)
	}
	maximum, err := time.ParseDuration(service.Restart.MaxBackoff)
	if err != nil || maximum < initial || maximum > 10*time.Minute {
		return newValidationError(path+".restart", "maxBackoff", ErrDurationInvalid)
	}
	stable, err := time.ParseDuration(service.Restart.StableWindow)
	if err != nil || stable < maximum || stable > 24*time.Hour {
		return newValidationError(path+".restart", "stableWindow", ErrDurationInvalid)
	}
	if service.Restart.MaxAttempts == nil || *service.Restart.MaxAttempts < 1 || *service.Restart.MaxAttempts > 100 {
		return newValidationError(path+".restart", "maxAttempts", ErrSemanticInvalid)
	}
	if service.Mode != "daemon" && service.Restart.Policy != "never" {
		return newValidationError(path+".restart", "policy", ErrSemanticInvalid)
	}
	return nil
}

func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func renderTemplatesForValidation(value string) string {
	for {
		start := strings.Index(value, "${")
		if start < 0 {
			return value
		}
		endOffset := strings.IndexByte(value[start+2:], '}')
		if endOffset < 0 {
			return value
		}
		end := start + 2 + endOffset
		replacement := "value"
		if strings.HasPrefix(value[start+2:end], "ports.") {
			replacement = "1024"
		}
		value = value[:start] + replacement + value[end+1:]
	}
}

func isExactPortTemplate(value string) bool {
	if !strings.HasPrefix(value, "${ports.") || !strings.HasSuffix(value, "}") {
		return false
	}
	name := strings.TrimSuffix(strings.TrimPrefix(value, "${ports."), "}")
	return name != "" && !strings.ContainsAny(name, "${}")
}
