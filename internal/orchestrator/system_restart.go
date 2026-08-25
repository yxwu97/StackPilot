package orchestrator

import (
	"context"
	"errors"
	"time"

	"stackpilot/internal/domain"
)

func (service *SingleService) restartStopKeys(ctx context.Context, workspaceID domain.WorkspaceID) ([]string, error) {
	keys := []string{"validate-runtime"}
	system, found, err := service.config.Runtime.GetActive(ctx, workspaceID)
	if err != nil || !found {
		return append(keys, "stop-aggregate-state"), err
	}
	spec, err := service.loadRuntimeSpec(ctx, system.ResolvedSpecDigest)
	if err != nil {
		return nil, err
	}
	for index := len(spec.Topology) - 1; index >= 0; index-- {
		for _, serviceID := range spec.Topology[index] {
			keys = append(keys, "stop:"+serviceID)
		}
	}
	return append(keys, "stop-aggregate-state"), nil
}

func (service *SingleService) runSystemRestart(ctx context.Context, operation Operation, input RestartSystemInput) {
	if _, err := service.config.Operations.Start(ctx, operation.ID); err != nil {
		return
	}
	stoppingSystem, err := service.executeRestartStop(ctx, operation)
	if err != nil {
		service.finishSystemStopFailure(ctx, operation, stoppingSystem, err)
		return
	}
	execution, err := service.prepareSystemStart(ctx, operation, input)
	if execution != nil && execution.plan != nil {
		defer execution.plan.Close()
	}
	if err == nil {
		err = service.executeSystemStart(ctx, operation, execution)
	}
	if err == nil {
		_, err = service.config.Operations.Succeed(ctx, operation.ID)
	}
	if err != nil {
		service.finishSystemStart(ctx, operation, execution, err)
	}
}

func (service *SingleService) executeRestartStop(ctx context.Context, operation Operation) (*domain.SystemInstance, error) {
	system, spec, runtimes, err := service.loadSystemStop(ctx, operation)
	if errors.Is(err, errNoActiveRuntime) {
		return nil, service.skipEmptyRestartStop(ctx, operation)
	}
	if err != nil {
		return system, err
	}
	if _, err := service.config.Runtime.TransitionSystem(ctx, operation.ID, system.ID, domain.SystemStopping, time.Now().UTC()); err != nil {
		return system, err
	}
	if err := service.stopSystemLayers(ctx, operation, spec, runtimes); err != nil {
		return system, err
	}
	err = service.runStep(ctx, operation.ID, stepNumber(operation, "stop-aggregate-state"), func() error {
		_, err := service.config.Runtime.TransitionSystem(ctx, operation.ID, system.ID, domain.SystemStopped, time.Now().UTC())
		return err
	})
	return system, err
}

func (service *SingleService) skipEmptyRestartStop(ctx context.Context, operation Operation) error {
	validateStep := stepNumber(operation, "validate-runtime")
	current, err := service.config.Operations.Get(ctx, operation.ID)
	if err != nil {
		return err
	}
	if current.Steps[validateStep-1].State == domain.OperationStepRunning {
		if _, err := service.config.Operations.TransitionStep(ctx, operation.ID, validateStep, domain.OperationStepSucceeded, "", ""); err != nil {
			return err
		}
	}
	_, err = service.config.Operations.TransitionStep(ctx, operation.ID, stepNumber(operation, "stop-aggregate-state"), domain.OperationStepSkipped, "", "")
	return err
}
