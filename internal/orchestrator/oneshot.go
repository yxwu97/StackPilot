package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"time"

	"stackpilot/internal/domain"
	"stackpilot/internal/driver"
)

const oneshotInspectInterval = 25 * time.Millisecond

type oneshotExitFailure struct{ exitCode uint32 }

func (failure oneshotExitFailure) Error() string {
	return fmt.Sprintf("oneshot process exited with code %d", failure.exitCode)
}

type oneshotTimeoutFailure struct{ exitCode uint32 }

func (failure oneshotTimeoutFailure) Error() string {
	return "oneshot process exceeded the start timeout"
}
func (failure oneshotTimeoutFailure) Unwrap() error { return context.DeadlineExceeded }

func (service *SingleService) awaitOneshot(ctx context.Context, operationID domain.OperationID, runtime domain.ServiceInstance, identity driver.RuntimeIdentity) (domain.ServiceInstance, error) {
	ticker := time.NewTicker(oneshotInspectInterval)
	defer ticker.Stop()
	for {
		observation, err := service.config.Driver.Inspect(ctx, identity)
		if err != nil {
			return runtime, err
		}
		if observation.State == "exited" {
			return service.completeOneshot(ctx, operationID, runtime, observation.ExitCode)
		}
		if observation.State != "running" || observation.ExitCode != nil {
			return runtime, fmt.Errorf("unexpected oneshot observation state %q", observation.State)
		}
		select {
		case <-ctx.Done():
			if errors.Is(context.Cause(ctx), context.DeadlineExceeded) {
				return service.timeoutOneshot(ctx, operationID, runtime)
			}
			return runtime, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (service *SingleService) completeOneshot(ctx context.Context, operationID domain.OperationID, runtime domain.ServiceInstance, exitCode *uint32) (domain.ServiceInstance, error) {
	if exitCode == nil {
		return runtime, fmt.Errorf("oneshot exited without an exit code")
	}
	if err := service.reapOneshot(ctx, runtime); err != nil {
		return runtime, err
	}
	target, code := domain.ServiceCompleted, ""
	if *exitCode != 0 {
		target, code = domain.ServiceFailed, "PROCESS_EXITED"
	}
	updated, err := service.transitionSettledOneshot(ctx, operationID, runtime, target, code, exitCode)
	if err != nil || target == domain.ServiceCompleted {
		return updated, err
	}
	return updated, oneshotExitFailure{exitCode: *exitCode}
}

func (service *SingleService) timeoutOneshot(ctx context.Context, operationID domain.OperationID, runtime domain.ServiceInstance) (domain.ServiceInstance, error) {
	const forcedExitCode uint32 = 137
	if err := service.reapOneshot(ctx, runtime); err != nil {
		return runtime, err
	}
	updated, err := service.transitionSettledOneshot(ctx, operationID, runtime, domain.ServiceFailed, "HEALTH_READINESS_TIMEOUT", uint32Pointer(forcedExitCode))
	if err != nil {
		return runtime, err
	}
	return updated, oneshotTimeoutFailure{exitCode: forcedExitCode}
}

func (service *SingleService) reapOneshot(ctx context.Context, runtime domain.ServiceInstance) error {
	if runtime.Identity == nil {
		return driver.ErrRuntimeNotFound
	}
	finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), workerFinalizationTimeout)
	defer cancel()
	err := service.config.Driver.Stop(finalCtx, driver.StopRequest{Identity: *runtime.Identity, GracefulTimeout: 0})
	if err != nil && !errors.Is(err, driver.ErrRuntimeNotFound) {
		return err
	}
	service.closeCapture(runtime.ID)
	return nil
}

func (service *SingleService) transitionSettledOneshot(ctx context.Context, operationID domain.OperationID, runtime domain.ServiceInstance, target domain.ServiceState, code string, exitCode *uint32) (domain.ServiceInstance, error) {
	finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), workerFinalizationTimeout)
	defer cancel()
	updated, err := service.config.Runtime.TransitionService(finalCtx, operationID, runtime.ID, runtime.StateVersion, target, code, exitCode, time.Now().UTC())
	if err != nil {
		return runtime, err
	}
	return *updated, nil
}

func uint32Pointer(value uint32) *uint32 { return &value }

func oneshotFailureExitCode(err error) *uint32 {
	var exited oneshotExitFailure
	if errors.As(err, &exited) {
		return uint32Pointer(exited.exitCode)
	}
	var timeout oneshotTimeoutFailure
	if errors.As(err, &timeout) {
		return uint32Pointer(timeout.exitCode)
	}
	return nil
}

func startDeadline(ctx context.Context, value string) (context.Context, context.CancelFunc) {
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, duration)
}
