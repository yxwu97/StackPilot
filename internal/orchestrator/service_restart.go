package orchestrator

import (
	"context"
	"fmt"
	"time"

	"stackpilot/internal/domain"
	"stackpilot/internal/driver"
	"stackpilot/internal/health"
	"stackpilot/internal/incident"
	"stackpilot/internal/logs"
	"stackpilot/internal/manifest"
)

func (service *SingleService) serviceRestartClosure(ctx context.Context, input RestartServiceInput) ([]string, map[string]domain.ProcessMode, error) {
	system, found, err := service.config.Runtime.GetActive(ctx, input.WorkspaceID)
	if err != nil || !found {
		return nil, nil, fmt.Errorf("%w: no active system", ErrInvalidInput)
	}
	if err := service.requireCurrentManifest(ctx, input.WorkspaceID, system.ManifestDigest); err != nil {
		return nil, nil, err
	}
	spec, err := service.loadRuntimeSpec(ctx, system.ResolvedSpecDigest)
	if err != nil {
		return nil, nil, err
	}
	graph, err := resolvedSpecDAG(spec)
	if err != nil {
		return nil, nil, err
	}
	closure, err := graph.DownstreamClosure(input.ServiceID.String())
	modes := make(map[string]domain.ProcessMode, len(spec.Services))
	for serviceID, resolved := range spec.Services {
		modes[serviceID] = resolved.Process.Mode
		if resolved.Driver == domain.DriverCompose {
			modes[serviceID] = domain.ProcessDaemon
		}
	}
	return closure, modes, err
}

func serviceRestartStepKeys(closure []string, modes map[string]domain.ProcessMode) []string {
	result := []string{"validate-runtime"}
	for index := len(closure) - 1; index >= 0; index-- {
		result = append(result, "stop:"+closure[index])
	}
	for _, serviceID := range closure {
		wait := "wait-ready:"
		if modes[serviceID] == domain.ProcessOneshot {
			wait = "wait-complete:"
		}
		result = append(result, "start:"+serviceID, wait+serviceID)
	}
	return append(result, "aggregate-state")
}

func (service *SingleService) runServiceRestart(ctx context.Context, operation Operation, input RestartServiceInput) {
	if _, err := service.config.Operations.Start(ctx, operation.ID); err != nil {
		return
	}
	system, spec, graph, closure, runtimes, err := service.loadServiceRestart(ctx, operation, input)
	if err == nil {
		err = service.stopRestartClosure(ctx, operation, spec, closure, runtimes)
	}
	if err == nil {
		startCtx, cancel := startDeadline(ctx, spec.StartTimeout)
		err = service.startRestartClosure(startCtx, operation, system, spec, graph, closure, runtimes)
		cancel()
	}
	if err == nil {
		err = service.finishServiceRestart(ctx, operation)
		if err == nil {
			service.startSystemLiveness(system.ID, spec)
		}
	}
	if err != nil {
		service.finishServiceRestartFailure(ctx, operation, system, err)
	}
}

func (service *SingleService) loadServiceRestart(ctx context.Context, operation Operation, input RestartServiceInput) (*domain.SystemInstance, *ResolvedSystemSpec, *DAG, []string, map[string]domain.ServiceInstance, error) {
	var system *domain.SystemInstance
	var spec *ResolvedSystemSpec
	var graph *DAG
	var closure []string
	runtimes := make(map[string]domain.ServiceInstance)
	err := service.runStep(ctx, operation.ID, stepNumber(operation, "validate-runtime"), func() error {
		var found bool
		var err error
		system, found, err = service.config.Runtime.GetActive(ctx, input.WorkspaceID)
		if err != nil || !found {
			return fmt.Errorf("%w: no active system", ErrInvalidInput)
		}
		if err = service.requireCurrentManifest(ctx, input.WorkspaceID, system.ManifestDigest); err != nil {
			return err
		}
		if spec, err = service.loadRuntimeSpec(ctx, system.ResolvedSpecDigest); err != nil {
			return err
		}
		if graph, err = resolvedSpecDAG(spec); err != nil {
			return err
		}
		if closure, err = graph.DownstreamClosure(input.ServiceID.String()); err != nil {
			return err
		}
		values, err := service.config.Runtime.ListServices(ctx, system.ID)
		for _, runtime := range values {
			runtimes[runtime.ServiceID.String()] = runtime
		}
		return err
	})
	return system, spec, graph, closure, runtimes, err
}

func (service *SingleService) requireCurrentManifest(ctx context.Context, workspaceID domain.WorkspaceID, runningDigest string) error {
	record, _, err := service.config.Workspaces.ExecutionManifest(ctx, workspaceID)
	if err != nil {
		return err
	}
	if record.LastValidDigest != runningDigest {
		return ErrManifestChanged
	}
	return nil
}

func resolvedSpecDAG(spec *ResolvedSystemSpec) (*DAG, error) {
	services := make(map[string]manifest.Service, len(spec.Services))
	for serviceID, resolved := range spec.Services {
		dependencies := make(map[string]string, len(resolved.Dependencies))
		for dependency, condition := range resolved.Dependencies {
			dependencies[dependency] = string(condition)
		}
		services[serviceID] = manifest.Service{DependsOn: dependencies}
	}
	return NewDAG(services)
}

func (service *SingleService) stopRestartClosure(ctx context.Context, operation Operation, spec *ResolvedSystemSpec, closure []string, runtimes map[string]domain.ServiceInstance) error {
	for index := len(closure) - 1; index >= 0; index-- {
		serviceID := closure[index]
		runtime, exists := runtimes[serviceID]
		if !exists {
			return fmt.Errorf("runtime service %s is missing", serviceID)
		}
		step := stepNumber(operation, "stop:"+serviceID)
		if err := service.runStep(ctx, operation.ID, step, func() error {
			return service.stopResolvedRuntime(ctx, operation.ID, spec, serviceID, &runtime)
		}); err != nil {
			service.failStep(context.WithoutCancel(ctx), operation.ID, step, singleServiceStopErrorCode(err))
			return err
		}
		runtimes[serviceID] = runtime
	}
	return nil
}

func (service *SingleService) startRestartClosure(ctx context.Context, operation Operation, system *domain.SystemInstance, spec *ResolvedSystemSpec, graph *DAG, closure []string, runtimes map[string]domain.ServiceInstance) error {
	_ = graph
	for _, serviceID := range closure {
		runtime := runtimes[serviceID]
		updated, err := service.config.Runtime.TransitionService(ctx, operation.ID, runtime.ID, runtime.StateVersion, domain.ServiceStarting, "", nil, time.Now().UTC())
		if err != nil {
			return err
		}
		runtime = *updated
		if spec.Services[serviceID].Driver == domain.DriverCompose {
			err = service.restartComposeService(ctx, operation, system, spec, serviceID, &runtime)
			if err != nil {
				if runtime.State.CanTransitionTo(domain.ServiceFailed) {
					_, _ = service.config.Runtime.TransitionService(context.WithoutCancel(ctx), operation.ID, runtime.ID, runtime.StateVersion, domain.ServiceFailed, systemStartErrorCode(err), nil, time.Now().UTC())
				}
				return err
			}
			runtimes[serviceID] = runtime
			continue
		}
		identity, err := service.startRestartedProcess(ctx, operation, system, spec, serviceID, &runtime)
		if err == nil {
			err = service.readyRestartedProcess(ctx, operation, *system, spec, serviceID, identity, &runtime)
		}
		if err != nil {
			if runtime.State.CanTransitionTo(domain.ServiceFailed) {
				_, _ = service.config.Runtime.TransitionService(context.WithoutCancel(ctx), operation.ID, runtime.ID, runtime.StateVersion, domain.ServiceFailed, systemStartErrorCode(err), oneshotFailureExitCode(err), time.Now().UTC())
			}
			return err
		}
		runtimes[serviceID] = runtime
	}
	return nil
}

func (service *SingleService) startRestartedProcess(ctx context.Context, operation Operation, system *domain.SystemInstance, spec *ResolvedSystemSpec, serviceID string, runtime *domain.ServiceInstance) (driver.RuntimeIdentity, error) {
	var identity driver.RuntimeIdentity
	step := stepNumber(operation, "start:"+serviceID)
	err := service.runStep(ctx, operation.ID, step, func() error {
		launch, err := service.prepareProcessLaunch(ctx, operation.SystemID, runtime.ID, spec.Services[serviceID].Process)
		if err != nil {
			return err
		}
		defer launch.clear()
		identity, err = service.config.Driver.Start(ctx, driver.StartRequest{Spec: launch.spec})
		if err != nil {
			return err
		}
		updated, err := service.config.Runtime.AttachIdentity(ctx, operation.ID, runtime.ID, runtime.StateVersion, identity, time.Now().UTC())
		if err != nil {
			return err
		}
		*runtime = *updated
		return service.startRestartCapture(operation, system, spec, serviceID, *runtime, launch.redactionValues)
	})
	if err != nil {
		service.failStep(context.WithoutCancel(ctx), operation.ID, step, systemStartErrorCode(err))
	}
	return identity, err
}

func (service *SingleService) startRestartCapture(operation Operation, system *domain.SystemInstance, spec *ResolvedSystemSpec, serviceID string, runtime domain.ServiceInstance, secretValues [][]byte) error {
	resolved := spec.Services[serviceID]
	session, err := service.config.StartLogs(service.config.Context, logs.CaptureRequest{
		Scope:  logs.Scope{SystemID: operation.SystemID, InstanceID: system.ID, ServiceID: runtime.ServiceID, ServiceInstanceID: runtime.ID, OperationID: operation.ID},
		Spools: map[logs.Stream]string{logs.StreamStdout: resolved.Process.StdoutPath, logs.StreamStderr: resolved.Process.StderrPath}, SecretValues: secretValues,
	})
	if err != nil {
		return err
	}
	service.mutex.Lock()
	service.captures[runtime.ID] = session
	service.mutex.Unlock()
	return nil
}

func (service *SingleService) readyRestartedProcess(ctx context.Context, operation Operation, system domain.SystemInstance, spec *ResolvedSystemSpec, serviceID string, identity driver.RuntimeIdentity, runtime *domain.ServiceInstance) error {
	resolved := spec.Services[serviceID]
	waitKey := "wait-ready:" + serviceID
	if resolved.Process.Mode == domain.ProcessOneshot {
		waitKey = "wait-complete:" + serviceID
	}
	step := stepNumber(operation, waitKey)
	err := service.runStep(ctx, operation.ID, step, func() error {
		if resolved.Process.Mode == domain.ProcessOneshot {
			updated, waitErr := service.awaitOneshot(ctx, operation.ID, *runtime, identity)
			*runtime = updated
			return waitErr
		}
		readiness := resolved.Readiness
		readiness.Identity = identity
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

func (service *SingleService) finishServiceRestart(ctx context.Context, operation Operation) error {
	if err := service.runStep(ctx, operation.ID, stepNumber(operation, "aggregate-state"), func() error { return nil }); err != nil {
		return err
	}
	_, err := service.config.Operations.Succeed(ctx, operation.ID)
	return err
}

func (service *SingleService) finishServiceRestartFailure(ctx context.Context, operation Operation, system *domain.SystemInstance, failure error) {
	finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), workerFinalizationTimeout)
	defer cancel()
	code := systemStartErrorCode(failure)
	service.finishActiveSteps(finalCtx, operation.ID, code)
	if system != nil {
		_, _ = service.config.Runtime.TransitionSystem(finalCtx, operation.ID, system.ID, domain.SystemFailed, time.Now().UTC())
	}
	if _, err := service.config.Operations.Fail(finalCtx, operation.ID, code); err != nil {
		service.logWorkerError(operation.ID, code, err)
	}
}
