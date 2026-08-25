package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"time"

	"stackpilot/internal/domain"
	"stackpilot/internal/driver/compose"
	"stackpilot/internal/health"
	"stackpilot/internal/incident"
	"stackpilot/internal/logs"
)

type joinedCapture struct{ sessions []CaptureSession }

func (capture *joinedCapture) Close() error {
	var result error
	for index := len(capture.sessions) - 1; index >= 0; index-- {
		result = errors.Join(result, capture.sessions[index].Close())
	}
	return result
}

func (service *SingleService) startComposeService(ctx context.Context, operation Operation, execution *systemStartExecution, serviceID string, runtime domain.ServiceInstance, resolved ResolvedService) error {
	if service.config.Compose == nil || resolved.Compose == nil {
		return ErrInvalidInput
	}
	identity, err := service.prepareComposeStart(ctx, operation, execution, serviceID, *resolved.Compose)
	if err != nil {
		return err
	}
	if err = service.buildComposeStart(ctx, operation, serviceID, identity); err != nil {
		return err
	}
	encoded, err := service.upComposeStart(ctx, operation, execution, serviceID, runtime, identity)
	if err != nil {
		return err
	}
	return service.readyComposeService(ctx, operation, execution, serviceID, encoded)
}

func (service *SingleService) prepareComposeStart(ctx context.Context, operation Operation, execution *systemStartExecution, serviceID string, resolved ResolvedComposeService) (compose.ProjectIdentity, error) {
	var identity compose.ProjectIdentity
	step := stepNumber(operation, "compose-preflight:"+serviceID)
	err := service.runStep(ctx, operation.ID, step, func() error {
		var prepareErr error
		identity, prepareErr = service.config.Compose.Prepare(ctx, composeRequest(execution.spec, resolved))
		return prepareErr
	})
	if err != nil {
		service.failStep(context.WithoutCancel(ctx), operation.ID, step, systemStartErrorCode(err))
	}
	return identity, err
}

func (service *SingleService) buildComposeStart(ctx context.Context, operation Operation, serviceID string, identity compose.ProjectIdentity) error {
	step := stepNumber(operation, "compose-build:"+serviceID)
	if identity.BuildPolicy != "always" {
		_, err := service.config.Operations.TransitionStep(ctx, operation.ID, step, domain.OperationStepSkipped, "", "")
		return err
	}
	err := service.runStep(ctx, operation.ID, step, func() error { return service.config.Compose.Build(ctx, identity) })
	if err != nil {
		service.failStep(context.WithoutCancel(ctx), operation.ID, step, systemStartErrorCode(err))
	}
	return err
}

func (service *SingleService) upComposeStart(ctx context.Context, operation Operation, execution *systemStartExecution, serviceID string, runtime domain.ServiceInstance, identity compose.ProjectIdentity) (string, error) {
	encoded := ""
	step := stepNumber(operation, "compose-up:"+serviceID)
	err := service.runStep(ctx, operation.ID, step, func() error {
		if err := releaseServiceProbes(execution, serviceID); err != nil {
			return err
		}
		if err := service.config.Compose.Up(ctx, identity); err != nil {
			return err
		}
		var err error
		encoded, err = compose.EncodeProjectIdentity(identity)
		if err != nil {
			return err
		}
		updated, err := service.config.Runtime.AttachComposeIdentity(ctx, operation.ID, runtime.ID, runtime.StateVersion, encoded, time.Now().UTC())
		if err != nil {
			return err
		}
		execution.updateRuntime(serviceID, *updated)
		execution.markCreated(serviceID)
		return service.startComposeCapture(operation, execution, serviceID, identity)
	})
	if err != nil {
		service.failStep(context.WithoutCancel(ctx), operation.ID, step, systemStartErrorCode(err))
	}
	return encoded, err
}

func releaseServiceProbes(execution *systemStartExecution, serviceID string) error {
	for _, logicalName := range ownedLogicalPorts(execution.spec, serviceID) {
		if err := execution.plan.ReleaseProbe(logicalName); err != nil {
			return err
		}
	}
	return nil
}

func (service *SingleService) readyComposeService(ctx context.Context, operation Operation, execution *systemStartExecution, serviceID, identity string) error {
	step := stepNumber(operation, "wait-ready:"+serviceID)
	err := service.runStep(ctx, operation.ID, step, func() error {
		resolved, runtime := execution.spec.Services[serviceID], execution.runtime(serviceID)
		readiness := resolved.Readiness
		readiness.Kind, readiness.ComposeIdentity = health.KindCompose, identity
		outcome, err := service.config.Readiness.Await(ctx, health.Request{ServiceInstanceID: runtime.ID, Spec: readiness})
		if err != nil {
			return err
		}
		if !outcome.Ready {
			service.reportServiceIncident(ctx, execution.system, runtime, incident.KindReadinessTimeout, incident.SeverityCritical, string(outcome.ErrorCode), outcome.LastResult)
			return readinessFailure{code: string(outcome.ErrorCode)}
		}
		updated, err := service.config.Runtime.TransitionService(ctx, operation.ID, runtime.ID, runtime.StateVersion, domain.ServiceReady, "", nil, time.Now().UTC())
		if err != nil {
			return err
		}
		execution.updateRuntime(serviceID, *updated)
		return service.bindServiceLeases(ctx, execution, serviceID)
	})
	if err != nil {
		service.failStep(context.WithoutCancel(ctx), operation.ID, step, systemStartErrorCode(err))
	}
	return err
}

func (service *SingleService) startComposeCapture(operation Operation, execution *systemStartExecution, serviceID string, identity compose.ProjectIdentity) error {
	runtime, resolved := execution.runtime(serviceID), execution.spec.Services[serviceID]
	since, err := service.composeLogSince(service.config.Context, runtime.ID)
	if err != nil {
		return err
	}
	follower, err := service.config.Compose.FollowLogs(service.config.Context, compose.LogFollowRequest{Identity: identity, StdoutPath: resolved.Compose.StdoutPath, StderrPath: resolved.Compose.StderrPath, Since: since})
	if err != nil {
		return err
	}
	manager, err := service.config.StartLogs(service.config.Context, logs.CaptureRequest{Scope: logs.Scope{SystemID: operation.SystemID, InstanceID: execution.system.ID, ServiceID: runtime.ServiceID, ServiceInstanceID: runtime.ID, OperationID: operation.ID}, Spools: map[logs.Stream]string{logs.StreamStdout: resolved.Compose.StdoutPath, logs.StreamStderr: resolved.Compose.StderrPath}})
	if err != nil {
		_ = follower.Close()
		return err
	}
	service.mutex.Lock()
	service.captures[runtime.ID] = &joinedCapture{sessions: []CaptureSession{follower, manager}}
	service.mutex.Unlock()
	return nil
}

func (service *SingleService) composeLogSince(ctx context.Context, id domain.ServiceInstanceID) (time.Time, error) {
	if service.config.LogCheckpoints == nil {
		return time.Time{}, nil
	}
	last, found, err := service.config.LogCheckpoints.LastTimestamp(ctx, id)
	if err != nil || !found {
		return time.Time{}, err
	}
	return last.Add(time.Nanosecond), nil
}

func (service *SingleService) restartComposeService(ctx context.Context, operation Operation, system *domain.SystemInstance, spec *ResolvedSystemSpec, serviceID string, runtime *domain.ServiceInstance) error {
	resolved := spec.Services[serviceID]
	if service.config.Compose == nil || resolved.Compose == nil {
		return ErrInvalidInput
	}
	var encoded string
	step := stepNumber(operation, "start:"+serviceID)
	err := service.runStep(ctx, operation.ID, step, func() error {
		identity, err := service.config.Compose.StartWithoutBuild(ctx, composeRequest(spec, *resolved.Compose))
		if err != nil {
			return err
		}
		encoded, err = compose.EncodeProjectIdentity(identity)
		if err != nil {
			return err
		}
		updated, err := service.config.Runtime.AttachComposeIdentity(ctx, operation.ID, runtime.ID, runtime.StateVersion, encoded, time.Now().UTC())
		if err != nil {
			return err
		}
		*runtime = *updated
		return service.startRestartComposeCapture(operation, system, resolved, *runtime, identity)
	})
	if err != nil {
		service.failStep(context.WithoutCancel(ctx), operation.ID, step, systemStartErrorCode(err))
		return err
	}
	return service.readyRestartCompose(ctx, operation, *system, resolved, encoded, runtime)
}

func (service *SingleService) startRestartComposeCapture(operation Operation, system *domain.SystemInstance, resolved ResolvedService, runtime domain.ServiceInstance, identity compose.ProjectIdentity) error {
	follower, err := service.config.Compose.FollowLogs(service.config.Context, compose.LogFollowRequest{Identity: identity, StdoutPath: resolved.Compose.StdoutPath, StderrPath: resolved.Compose.StderrPath})
	if err != nil {
		return err
	}
	manager, err := service.config.StartLogs(service.config.Context, logs.CaptureRequest{Scope: logs.Scope{SystemID: operation.SystemID, InstanceID: system.ID, ServiceID: runtime.ServiceID, ServiceInstanceID: runtime.ID, OperationID: operation.ID}, Spools: map[logs.Stream]string{logs.StreamStdout: resolved.Compose.StdoutPath, logs.StreamStderr: resolved.Compose.StderrPath}})
	if err != nil {
		_ = follower.Close()
		return err
	}
	service.mutex.Lock()
	service.captures[runtime.ID] = &joinedCapture{sessions: []CaptureSession{follower, manager}}
	service.mutex.Unlock()
	return nil
}

func (service *SingleService) readyRestartCompose(ctx context.Context, operation Operation, system domain.SystemInstance, resolved ResolvedService, identity string, runtime *domain.ServiceInstance) error {
	step := stepNumber(operation, "wait-ready:"+resolved.ServiceID.String())
	err := service.runStep(ctx, operation.ID, step, func() error {
		readiness := resolved.Readiness
		readiness.Kind, readiness.ComposeIdentity = health.KindCompose, identity
		outcome, err := service.config.Readiness.Await(ctx, health.Request{ServiceInstanceID: runtime.ID, Spec: readiness})
		if err != nil {
			return err
		}
		if !outcome.Ready {
			service.reportServiceIncident(ctx, system, *runtime, incident.KindReadinessTimeout, incident.SeverityCritical, string(outcome.ErrorCode), outcome.LastResult)
			return readinessFailure{code: string(outcome.ErrorCode)}
		}
		updated, err := service.config.Runtime.TransitionService(ctx, operation.ID, runtime.ID, runtime.StateVersion, domain.ServiceReady, "", nil, time.Now().UTC())
		if err == nil {
			*runtime = *updated
		}
		return err
	})
	if err != nil {
		service.failStep(context.WithoutCancel(ctx), operation.ID, step, systemStartErrorCode(err))
	}
	return err
}

func composeRequest(spec *ResolvedSystemSpec, resolved ResolvedComposeService) compose.LifecycleRequest {
	return compose.LifecycleRequest{WorkspaceRoot: resolved.WorkspaceRoot, DataDir: resolved.DataDir, ComposeFile: resolved.ComposeFile, OverrideFile: resolved.OverrideFile, SystemID: spec.SystemID, WorkspaceID: spec.WorkspaceID, InstanceID: spec.InstanceID, Services: append([]string(nil), resolved.Services...), BuildPolicy: resolved.BuildPolicy, Readiness: cloneStringRequirements(resolved.Readiness), StartTimeout: resolved.StartTimeout, StopTimeout: resolved.StopTimeout}
}

func cloneStringRequirements(source map[string]string) map[string]string {
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func (service *SingleService) stopCompose(ctx context.Context, runtime domain.ServiceInstance) error {
	if runtime.ComposeIdentity == "" || service.config.Compose == nil {
		return fmt.Errorf("Compose runtime identity is unavailable")
	}
	identity, err := compose.DecodeProjectIdentity(runtime.ComposeIdentity)
	if err != nil {
		return err
	}
	err = service.config.Compose.Stop(ctx, identity)
	if errors.Is(err, compose.ErrProjectNotFound) {
		return nil
	}
	return err
}

func (service *SingleService) stopResolvedRuntime(ctx context.Context, operationID domain.OperationID, spec *ResolvedSystemSpec, serviceID string, runtime *domain.ServiceInstance) error {
	if runtime.Driver != domain.DriverCompose {
		return service.stopOne(ctx, operationID, runtime)
	}
	found, err := service.prepareComposeStop(ctx, operationID, spec, serviceID, runtime)
	if err != nil {
		return err
	}
	if !found {
		if err := service.beginServiceStop(ctx, operationID, runtime); err != nil {
			return err
		}
		return service.completeServiceStop(ctx, operationID, runtime)
	}
	return service.stopOne(ctx, operationID, runtime)
}

func (service *SingleService) prepareComposeStop(ctx context.Context, operationID domain.OperationID, spec *ResolvedSystemSpec, serviceID string, runtime *domain.ServiceInstance) (bool, error) {
	resolved, exists := spec.Services[serviceID]
	if service.config.Compose == nil || !exists || resolved.Compose == nil {
		return false, fmt.Errorf("%w: resolved Compose service is unavailable", compose.ErrDiscoveryFailed)
	}
	request := composeRequest(spec, *resolved.Compose)
	if _, err := service.config.Compose.Preflight(ctx, compose.PreflightRequest{WorkspaceRoot: request.WorkspaceRoot, ComposeFile: request.ComposeFile, Services: request.Services, BuildPolicy: request.BuildPolicy, Readiness: request.Readiness}); err != nil {
		return false, err
	}
	if runtime.ComposeIdentity != "" {
		return true, nil
	}
	identity, _, err := service.config.Compose.Discover(ctx, request)
	if errors.Is(err, compose.ErrProjectNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	encoded, err := compose.EncodeProjectIdentity(identity)
	if err != nil {
		return false, err
	}
	updated, err := service.config.Runtime.AttachComposeStopIdentity(ctx, operationID, runtime.ID, runtime.StateVersion, encoded, time.Now().UTC())
	if err == nil {
		*runtime = *updated
	}
	return err == nil, err
}

func (service *SingleService) reconcileComposeRuntime(ctx context.Context, system domain.SystemInstance, runtime domain.ServiceInstance, spec *ResolvedSystemSpec) (domain.ServiceInstance, error) {
	if service.config.Compose == nil || spec == nil {
		return runtime, fmt.Errorf("Compose reconciliation dependencies are unavailable")
	}
	resolved, exists := spec.Services[runtime.ServiceID.String()]
	if !exists || resolved.Compose == nil {
		return runtime, fmt.Errorf("Compose resolved service is unavailable")
	}
	identity, observation, err := service.recoverCompose(ctx, runtime, spec, *resolved.Compose)
	if err != nil {
		return service.reconcileComposeError(ctx, runtime, err)
	}
	if runtime.ComposeIdentity == "" {
		if !runtime.State.CanTransitionTo(domain.ServiceWaitingReady) {
			if observation.State != "running" {
				return service.reconcileComposeExited(ctx, system, runtime, resolved)
			}
			if runtime.State == domain.ServiceStopping {
				return service.reconcileTransition(ctx, runtime, domain.ServiceFailed, reconcileRestartedCode, nil)
			}
			return runtime, nil
		}
		encoded, encodeErr := compose.EncodeProjectIdentity(identity)
		if encodeErr != nil {
			return runtime, encodeErr
		}
		updated, attachErr := service.config.Runtime.AttachComposeIdentity(ctx, "", runtime.ID, runtime.StateVersion, encoded, time.Now().UTC())
		if attachErr != nil {
			return runtime, attachErr
		}
		runtime = *updated
	}
	if observation.State != "running" {
		return service.reconcileComposeExited(ctx, system, runtime, resolved)
	}
	if err := service.resumeComposeCapture(system, runtime, resolved, identity); err != nil {
		return runtime, err
	}
	if runtime.State == domain.ServiceWaitingReady {
		return service.reconcileComposeReadiness(ctx, system, runtime, resolved)
	}
	if runtime.State == domain.ServiceStopping {
		return service.reconcileTransition(ctx, runtime, domain.ServiceFailed, reconcileRestartedCode, nil)
	}
	return runtime, nil
}

func (service *SingleService) recoverCompose(ctx context.Context, runtime domain.ServiceInstance, spec *ResolvedSystemSpec, resolved ResolvedComposeService) (compose.ProjectIdentity, compose.ProjectObservation, error) {
	if runtime.ComposeIdentity != "" {
		return service.config.Compose.Recover(ctx, runtime.ComposeIdentity)
	}
	return service.config.Compose.Discover(ctx, composeRequest(spec, resolved))
}

func (service *SingleService) reconcileComposeReadiness(ctx context.Context, system domain.SystemInstance, runtime domain.ServiceInstance, resolved ResolvedService) (domain.ServiceInstance, error) {
	readiness := resolved.Readiness
	readiness.Kind, readiness.ComposeIdentity = health.KindCompose, runtime.ComposeIdentity
	outcome, err := service.config.Readiness.Await(ctx, health.Request{ServiceInstanceID: runtime.ID, Spec: readiness})
	if err != nil {
		return runtime, err
	}
	if !outcome.Ready {
		updated, transitionErr := service.reconcileTransition(ctx, runtime, domain.ServiceFailed, string(outcome.ErrorCode), nil)
		if transitionErr == nil {
			service.reportServiceIncident(ctx, system, updated, incident.KindReadinessTimeout, incident.SeverityCritical, string(outcome.ErrorCode), outcome.LastResult)
		}
		return updated, transitionErr
	}
	return service.reconcileTransition(ctx, runtime, domain.ServiceReady, "", nil)
}

func (service *SingleService) reconcileComposeExited(ctx context.Context, system domain.SystemInstance, runtime domain.ServiceInstance, resolved ResolvedService) (domain.ServiceInstance, error) {
	if runtime.State == domain.ServiceFailed {
		return runtime, nil
	}
	service.closeCapture(runtime.ID)
	target, code := domain.ServiceFailed, "CONTAINER_UNHEALTHY"
	if runtime.State == domain.ServiceStopping {
		target, code = domain.ServiceStopped, ""
	}
	updated, err := service.reconcileTransition(ctx, runtime, target, code, nil)
	if err == nil {
		service.releaseReconciledLeases(ctx, system, runtime.ServiceID.String())
		if target == domain.ServiceFailed {
			service.reportServiceIncident(ctx, system, updated, incident.KindProcessExit, incident.SeverityCritical, code, health.Result{ErrorCode: health.CodeContainerUnhealthy, CheckedAt: time.Now().UTC()})
			err = service.scheduleAutomaticRestart(ctx, system, updated, resolved.Restart, "compose-exit")
		}
	}
	return updated, err
}

func (service *SingleService) reconcileComposeError(ctx context.Context, runtime domain.ServiceInstance, cause error) (domain.ServiceInstance, error) {
	if errors.Is(cause, compose.ErrProjectNotFound) {
		return service.reconcileMissingIdentity(ctx, runtime)
	}
	if runtime.State == domain.ServiceUnknown || runtime.State == domain.ServiceFailed {
		return runtime, nil
	}
	code := "COMPOSE_DISCOVERY_FAILED"
	if errors.Is(cause, compose.ErrProjectIdentityMismatch) {
		code = "COMPOSE_PROJECT_IDENTITY_MISMATCH"
	}
	return service.reconcileTransition(ctx, runtime, domain.ServiceUnknown, code, nil)
}

func (service *SingleService) resumeComposeCapture(system domain.SystemInstance, runtime domain.ServiceInstance, resolved ResolvedService, identity compose.ProjectIdentity) error {
	service.mutex.Lock()
	_, exists := service.captures[runtime.ID]
	service.mutex.Unlock()
	if exists {
		return nil
	}
	since, err := service.composeLogSince(service.config.Context, runtime.ID)
	if err != nil {
		return err
	}
	follower, err := service.config.Compose.FollowLogs(service.config.Context, compose.LogFollowRequest{Identity: identity, StdoutPath: resolved.Compose.StdoutPath, StderrPath: resolved.Compose.StderrPath, Since: since})
	if err != nil {
		return err
	}
	request := logs.CaptureRequest{Scope: logs.Scope{SystemID: system.SystemID, InstanceID: system.ID, ServiceID: runtime.ServiceID, ServiceInstanceID: runtime.ID}, Spools: map[logs.Stream]string{logs.StreamStdout: resolved.Compose.StdoutPath, logs.StreamStderr: resolved.Compose.StderrPath}}
	manager, err := service.config.ResumeLogs(service.config.Context, request)
	if err != nil {
		_ = follower.Close()
		return err
	}
	service.mutex.Lock()
	service.captures[runtime.ID] = &joinedCapture{sessions: []CaptureSession{follower, manager}}
	service.mutex.Unlock()
	return nil
}
