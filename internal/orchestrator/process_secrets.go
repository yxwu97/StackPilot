package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"time"
	"unicode/utf8"

	"stackpilot/internal/domain"
	"stackpilot/internal/driver"
	"stackpilot/internal/security"
)

const maximumProcessSecretSize = 16 * 1024

type preparedProcessLaunch struct {
	spec            driver.ResolvedServiceSpec
	redactionValues [][]byte
	versions        []security.ServiceSecretVersion
}

func (service *SingleService) prepareProcessLaunch(ctx context.Context, systemID domain.SystemID, serviceInstanceID domain.ServiceInstanceID, spec driver.ResolvedServiceSpec) (*preparedProcessLaunch, error) {
	launch := &preparedProcessLaunch{spec: cloneProcessSpec(spec)}
	if len(spec.SecretReferences) == 0 {
		return launch, nil
	}
	if service.config.Secrets == nil || service.config.SecretVersions == nil {
		return nil, fmt.Errorf("Secret launch capability is unavailable")
	}
	if err := service.resolveProcessSecrets(ctx, systemID, serviceInstanceID, launch); err != nil {
		launch.clear()
		return nil, err
	}
	if err := service.config.SecretVersions.RecordServiceSecretVersions(ctx, serviceInstanceID, launch.versions); err != nil {
		launch.clear()
		return nil, err
	}
	return launch, nil
}

func (service *SingleService) resumeProcessSecretValues(ctx context.Context, system domain.SystemInstance, runtime domain.ServiceInstance) ([][]byte, error) {
	if service.config.ResolvedSpecs == nil {
		return nil, nil
	}
	spec, err := service.loadRuntimeSpec(ctx, system.ResolvedSpecDigest)
	if err != nil {
		return nil, err
	}
	resolved, exists := spec.Services[runtime.ServiceID.String()]
	if !exists || len(resolved.Process.SecretReferences) == 0 {
		return nil, nil
	}
	if service.config.Secrets == nil || service.config.SecretVersions == nil {
		return nil, fmt.Errorf("Secret launch capability is unavailable")
	}
	launch := &preparedProcessLaunch{spec: cloneProcessSpec(resolved.Process)}
	defer launch.clear()
	if err := service.resolveProcessSecrets(ctx, system.SystemID, runtime.ID, launch); err != nil {
		return nil, err
	}
	if err := service.verifyRecordedSecretVersions(ctx, runtime.ID, launch.versions); err != nil {
		return nil, err
	}
	values := launch.redactionValues
	launch.redactionValues = nil
	return values, nil
}

func (service *SingleService) verifyRecordedSecretVersions(ctx context.Context, serviceID domain.ServiceInstanceID, resolved []security.ServiceSecretVersion) error {
	recorded, err := service.config.SecretVersions.ListServiceSecretVersions(ctx, serviceID)
	if err != nil {
		return err
	}
	byEnvironment := make(map[string]security.ServiceSecretVersion, len(recorded))
	for _, value := range recorded {
		byEnvironment[value.EnvironmentName] = value
	}
	for _, value := range resolved {
		stored, exists := byEnvironment[value.EnvironmentName]
		if !exists || stored.Key != value.Key || stored.Provider != value.Provider || stored.Version != value.Version {
			return security.ErrSecretVersionConflict
		}
	}
	return nil
}

func (service *SingleService) resolveProcessSecrets(ctx context.Context, systemID domain.SystemID, serviceInstanceID domain.ServiceInstanceID, launch *preparedProcessLaunch) error {
	for _, environmentName := range sortedSecretEnvironmentNames(launch.spec.SecretReferences) {
		name := launch.spec.SecretReferences[environmentName]
		key := security.SecretKey{SystemID: systemID, Name: name}
		resolved, err := service.config.Secrets.Resolve(ctx, key)
		if err != nil {
			return err
		}
		if err := addResolvedProcessSecret(launch, serviceInstanceID, environmentName, key, resolved); err != nil {
			resolved.Clear()
			return err
		}
		resolved.Clear()
	}
	return nil
}

func addResolvedProcessSecret(launch *preparedProcessLaunch, serviceInstanceID domain.ServiceInstanceID, environmentName string, key security.SecretKey, resolved security.ResolvedSecret) error {
	if resolved.Metadata.Key != key || security.ValidateSecretMetadata(resolved.Metadata) != nil || !validProcessSecretValue(resolved.Value) {
		return security.ErrSecretInvalid
	}
	launch.spec.Environment[environmentName] = string(resolved.Value)
	launch.redactionValues = append(launch.redactionValues, append([]byte(nil), resolved.Value...))
	launch.versions = append(launch.versions, security.ServiceSecretVersion{
		ServiceInstanceID: serviceInstanceID, EnvironmentName: environmentName,
		Key: resolved.Metadata.Key, Provider: resolved.Metadata.Provider,
		Version: resolved.Metadata.Version, ResolvedAt: time.Now().UTC(),
	})
	return nil
}

func validProcessSecretValue(value []byte) bool {
	return len(value) > 0 && len(value) <= maximumProcessSecretSize && utf8.Valid(value) &&
		bytes.IndexByte(value, 0) < 0 && bytes.IndexAny(value, "\r\n") < 0
}

func cloneProcessSpec(spec driver.ResolvedServiceSpec) driver.ResolvedServiceSpec {
	result := spec
	result.Arguments = append([]string(nil), spec.Arguments...)
	result.Environment = cloneStringMap(spec.Environment)
	result.SecretReferences = cloneStringMap(spec.SecretReferences)
	return result
}

func cloneStringMap(source map[string]string) map[string]string {
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func sortedSecretEnvironmentNames(references map[string]string) []string {
	result := make([]string, 0, len(references))
	for name := range references {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}

func (launch *preparedProcessLaunch) clear() {
	for name := range launch.spec.SecretReferences {
		launch.spec.Environment[name] = ""
		delete(launch.spec.Environment, name)
	}
	clearSecretValues(launch.redactionValues)
	launch.redactionValues = nil
	launch.versions = nil
}

func clearSecretValues(values [][]byte) {
	for _, value := range values {
		for index := range value {
			value[index] = 0
		}
	}
}

func secretErrorCode(err error) string {
	switch {
	case errors.Is(err, security.ErrSecretNotFound):
		return "SECRET_NOT_FOUND"
	case errors.Is(err, security.ErrSecretVersionConflict):
		return "SECRET_VERSION_CONFLICT"
	case errors.Is(err, security.ErrSecretInvalid):
		return "SECRET_INVALID"
	default:
		return ""
	}
}
