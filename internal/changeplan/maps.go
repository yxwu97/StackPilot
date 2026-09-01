package changeplan

import (
	"sort"

	"stackpilot/internal/revision"
)

func serviceMap(values []revision.ServiceFact) map[string]revision.ServiceFact {
	result := make(map[string]revision.ServiceFact, len(values))
	for _, value := range values {
		result[value.ServiceID.String()] = value
	}
	return result
}

func runnerMap(values []revision.RunnerFact) map[string]revision.RunnerFact {
	result := make(map[string]revision.RunnerFact, len(values))
	for _, value := range values {
		result[value.ServiceID.String()] = value
	}
	return result
}

func fileMap(values []revision.FileFact) map[string]revision.FileFact {
	result := make(map[string]revision.FileFact, len(values))
	for _, value := range values {
		result[value.Path] = value
	}
	return result
}

func portMap(values []revision.PortFact) map[string]revision.PortFact {
	result := make(map[string]revision.PortFact, len(values))
	for _, value := range values {
		result[value.Name] = value
	}
	return result
}

func secretMap(values []revision.SecretFact) map[string]revision.SecretFact {
	result := make(map[string]revision.SecretFact, len(values))
	for _, value := range values {
		result[value.ServiceID.String()+"."+value.EnvironmentName] = value
	}
	return result
}

func unionServiceKeys(left, right map[string]revision.ServiceFact) []string {
	return unionKeys(left, right)
}

func unionRunnerKeys(left, right map[string]revision.RunnerFact) []string {
	return unionKeys(left, right)
}

func unionFileKeys(left, right map[string]revision.FileFact) []string {
	return unionKeys(left, right)
}

func unionPortKeys(left, right map[string]revision.PortFact) []string {
	return unionKeys(left, right)
}

func unionStringKeys(left, right map[string]revision.SecretFact) []string {
	return unionKeys(left, right)
}

func unionKeys[T any](left, right map[string]T) []string {
	set := make(map[string]struct{}, len(left)+len(right))
	for key := range left {
		set[key] = struct{}{}
	}
	for key := range right {
		set[key] = struct{}{}
	}
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
