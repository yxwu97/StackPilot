package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"stackpilot/internal/domain"
)

func (service *SingleService) stopSteps(ctx context.Context, workspaceID domain.WorkspaceID) ([]string, bool, error) {
	system, found, err := service.config.Runtime.GetActive(ctx, workspaceID)
	if err != nil || !found {
		return []string{"validate-runtime", "stop:service", "aggregate-state"}, false, err
	}
	services, err := service.config.Runtime.ListServices(ctx, system.ID)
	if err != nil {
		return []string{"validate-runtime", "stop:service", "aggregate-state"}, false, err
	}
	if len(services) == 1 && services[0].Driver != domain.DriverCompose {
		return []string{"validate-runtime", "stop:service", "aggregate-state"}, false, err
	}
	if service.config.ResolvedSpecs == nil {
		return nil, true, fmt.Errorf("%w: resolved spec store", ErrInvalidInput)
	}
	spec, err := service.loadRuntimeSpec(ctx, system.ResolvedSpecDigest)
	if err != nil {
		return nil, true, err
	}
	return systemStopStepKeys(spec.Topology), true, nil
}

func systemStopStepKeys(topology [][]string) []string {
	result := []string{"validate-runtime"}
	for index := len(topology) - 1; index >= 0; index-- {
		for _, serviceID := range topology[index] {
			result = append(result, "stop:"+serviceID)
		}
	}
	return append(result, "aggregate-state")
}

func (service *SingleService) loadRuntimeSpec(ctx context.Context, digest string) (*ResolvedSystemSpec, error) {
	encoded, err := service.config.ResolvedSpecs.LoadResolvedSpec(ctx, digest)
	if err != nil {
		return nil, err
	}
	var spec ResolvedSystemSpec
	if err := json.Unmarshal(encoded, &spec); err != nil {
		return nil, fmt.Errorf("decode resolved system spec: %w", err)
	}
	if spec.SchemaVersion != resolvedSystemSpecSchema || len(spec.Topology) == 0 {
		return nil, fmt.Errorf("resolved system spec contract is invalid")
	}
	spec.Digest, spec.CanonicalJSON = digest, append([]byte(nil), encoded...)
	return &spec, nil
}

func (service *SingleService) runSystemStop(ctx context.Context, operation Operation) {
	if _, err := service.config.Operations.Start(ctx, operation.ID); err != nil {
		return
	}
	system, spec, runtimes, err := service.loadSystemStop(ctx, operation)
	if errors.Is(err, errNoActiveRuntime) {
		service.finishNoActiveSystemStop(ctx, operation)
		return
	}
	if err == nil {
		_, err = service.config.Runtime.TransitionSystem(ctx, operation.ID, system.ID, domain.SystemStopping, time.Now().UTC())
	}
	if err == nil {
		err = service.stopSystemLayers(ctx, operation, spec, runtimes)
	}
	if err == nil {
		err = service.completeSystemStop(ctx, operation, system.ID)
	}
	if err != nil {
		service.finishSystemStopFailure(ctx, operation, system, err)
	}
}

func (service *SingleService) loadSystemStop(ctx context.Context, operation Operation) (*domain.SystemInstance, *ResolvedSystemSpec, map[string]domain.ServiceInstance, error) {
	var system *domain.SystemInstance
	var spec *ResolvedSystemSpec
	runtimes := make(map[string]domain.ServiceInstance)
	err := service.runStep(ctx, operation.ID, stepNumber(operation, "validate-runtime"), func() error {
		var found bool
		var err error
		system, found, err = service.config.Runtime.GetActive(ctx, operation.WorkspaceID)
		if err != nil || !found {
			if err == nil {
				err = errNoActiveRuntime
			}
			return err
		}
		spec, err = service.loadRuntimeSpec(ctx, system.ResolvedSpecDigest)
		if err != nil {
			return err
		}
		values, err := service.config.Runtime.ListServices(ctx, system.ID)
		for _, runtime := range values {
			runtimes[runtime.ServiceID.String()] = runtime
		}
		return err
	})
	return system, spec, runtimes, err
}

func (service *SingleService) stopSystemLayers(ctx context.Context, operation Operation, spec *ResolvedSystemSpec, runtimes map[string]domain.ServiceInstance) error {
	collector := &FailureCollector{}
	var mutex sync.Mutex
	for index := len(spec.Topology) - 1; index >= 0; index-- {
		service.stopSystemLayer(ctx, operation, spec, runtimes, spec.Topology[index], &mutex, collector)
	}
	if report, failed := collector.Report(); failed {
		return systemStartFailure{report: report}
	}
	return nil
}

func (service *SingleService) stopSystemLayer(ctx context.Context, operation Operation, spec *ResolvedSystemSpec, runtimes map[string]domain.ServiceInstance, layer []string, mutex *sync.Mutex, collector *FailureCollector) {
	semaphore := make(chan struct{}, maxParallelServices)
	var wait sync.WaitGroup
	for _, serviceID := range layer {
		serviceID := serviceID
		wait.Add(1)
		go func() {
			defer wait.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()
			mutex.Lock()
			runtime, exists := runtimes[serviceID]
			mutex.Unlock()
			if !exists {
				collector.Add(ServiceFailure{ServiceID: serviceID, ErrorCode: "PROCESS_STOP_FAILED", Cause: fmt.Errorf("runtime service missing")})
				return
			}
			if err := service.stopSystemService(ctx, operation, spec, serviceID, &runtime); err != nil {
				code := singleServiceStopErrorCode(err)
				runtime = service.markSystemStopServiceFailed(ctx, operation.ID, runtime, code)
				mutex.Lock()
				runtimes[serviceID] = runtime
				mutex.Unlock()
				collector.Add(ServiceFailure{ServiceID: serviceID, ErrorCode: code, Cause: err})
				return
			}
			mutex.Lock()
			runtimes[serviceID] = runtime
			mutex.Unlock()
		}()
	}
	wait.Wait()
}

func (service *SingleService) markSystemStopServiceFailed(ctx context.Context, operationID domain.OperationID, runtime domain.ServiceInstance, code string) domain.ServiceInstance {
	if !runtime.State.CanTransitionTo(domain.ServiceFailed) {
		return runtime
	}
	updated, err := service.config.Runtime.TransitionService(context.WithoutCancel(ctx), operationID, runtime.ID, runtime.StateVersion, domain.ServiceFailed, code, nil, time.Now().UTC())
	if err != nil {
		return runtime
	}
	return *updated
}

func (service *SingleService) stopSystemService(ctx context.Context, operation Operation, spec *ResolvedSystemSpec, serviceID string, runtime *domain.ServiceInstance) error {
	step := stepNumber(operation, "stop:"+serviceID)
	err := service.runStep(ctx, operation.ID, step, func() error {
		if runtime.State == domain.ServiceStopped {
			return nil
		}
		if err := service.stopResolvedRuntime(ctx, operation.ID, spec, serviceID, runtime); err != nil {
			return err
		}
		service.releaseServiceLeases(ctx, spec, serviceID)
		return nil
	})
	if err != nil {
		service.failStep(context.WithoutCancel(ctx), operation.ID, step, singleServiceStopErrorCode(err))
	}
	return err
}

func (service *SingleService) completeSystemStop(ctx context.Context, operation Operation, systemID domain.SystemInstanceID) error {
	step := stepNumber(operation, "aggregate-state")
	if err := service.runStep(ctx, operation.ID, step, func() error {
		_, err := service.config.Runtime.TransitionSystem(ctx, operation.ID, systemID, domain.SystemStopped, time.Now().UTC())
		return err
	}); err != nil {
		return err
	}
	_, err := service.config.Operations.Succeed(ctx, operation.ID)
	return err
}

func (service *SingleService) finishSystemStopFailure(ctx context.Context, operation Operation, system *domain.SystemInstance, failure error) {
	finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), workerFinalizationTimeout)
	defer cancel()
	code := singleServiceStopErrorCode(failure)
	var systemFailure systemStartFailure
	if errors.As(failure, &systemFailure) && systemFailure.report.Primary.ErrorCode != "" {
		code = systemFailure.report.Primary.ErrorCode
	}
	service.finishActiveSteps(finalCtx, operation.ID, code)
	if system != nil {
		_, _ = service.config.Runtime.TransitionSystem(finalCtx, operation.ID, system.ID, domain.SystemFailed, time.Now().UTC())
	}
	if _, err := service.config.Operations.Fail(finalCtx, operation.ID, code); err != nil {
		service.logWorkerError(operation.ID, code, err)
	}
}

func (service *SingleService) finishNoActiveSystemStop(ctx context.Context, operation Operation) {
	current, err := service.config.Operations.Get(ctx, operation.ID)
	if err != nil {
		return
	}
	for _, step := range current.Steps {
		target := domain.OperationStepSkipped
		if step.Key == "validate-runtime" {
			target = domain.OperationStepSucceeded
		}
		if step.State == domain.OperationStepRunning || step.State == domain.OperationStepPending {
			_, _ = service.config.Operations.TransitionStep(ctx, operation.ID, step.Number, target, "", "")
		}
	}
	_, _ = service.config.Operations.Succeed(ctx, operation.ID)
}
