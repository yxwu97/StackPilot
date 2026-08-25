package health

import (
	"context"
	"fmt"

	"stackpilot/internal/domain"
)

// LivenessRequest binds a recurring check to an already-ready daemon instance.
type LivenessRequest struct {
	ServiceInstanceID domain.ServiceInstanceID
	InitialState      domain.ServiceState
	Spec              ResolvedSpec
}

// LivenessTransition describes one threshold-driven state change.
type LivenessTransition struct {
	From   domain.ServiceState
	To     domain.ServiceState
	Result Result
}

// LivenessHandler persists a state transition and its event atomically.
type LivenessHandler interface {
	HandleLiveness(context.Context, domain.ServiceInstanceID, LivenessTransition) error
}

// MonitorLiveness checks until cancellation and emits only threshold state changes.
func (engine *Engine) MonitorLiveness(ctx context.Context, request LivenessRequest, handler LivenessHandler) error {
	if err := validateLivenessRequest(request, handler); err != nil {
		return err
	}
	state := request.InitialState
	successes, failures := 0, 0
	for {
		result := engine.checkOnce(ctx, request.Spec)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var err error
		result, err = engine.record(ctx, request.ServiceInstanceID, result)
		if err != nil {
			return err
		}
		target := nextLivenessState(state, result.Success, &successes, &failures, request.Spec)
		if target != state {
			transition := LivenessTransition{From: state, To: target, Result: result}
			if err := handler.HandleLiveness(ctx, request.ServiceInstanceID, transition); err != nil {
				return fmt.Errorf("persist liveness transition: %w", err)
			}
			state = target
			successes, failures = 0, 0
		}
		if err := waitContext(ctx, engine.delay(request.Spec.Interval, successes+failures)); err != nil {
			return ctx.Err()
		}
	}
}

func validateLivenessRequest(request LivenessRequest, handler LivenessHandler) error {
	if handler == nil {
		return fmt.Errorf("%w: liveness handler", ErrInvalidSpec)
	}
	if request.InitialState != domain.ServiceReady && request.InitialState != domain.ServiceDegraded {
		return fmt.Errorf("%w: liveness initial state", ErrInvalidSpec)
	}
	if _, err := domain.ParseServiceInstanceID(request.ServiceInstanceID.String()); err != nil {
		return fmt.Errorf("%w: service instance", ErrInvalidSpec)
	}
	return validateRecurringSpec(request.Spec)
}

func validateRecurringSpec(spec ResolvedSpec) error {
	if spec.CheckTimeout <= 0 || spec.Interval <= 0 || spec.CheckTimeout > spec.Interval ||
		spec.SuccessThreshold < 1 || spec.SuccessThreshold > 100 ||
		spec.FailureThreshold < 1 || spec.FailureThreshold > 100 {
		return fmt.Errorf("%w: timing or threshold", ErrInvalidSpec)
	}
	request := Request{ServiceInstanceID: domain.ServiceInstanceID("svi_01ARZ3NDEKTSV4RRFFQ69G5FAV"), Spec: spec}
	request.Spec.ReadinessTimeout = request.Spec.Interval
	return validateRequest(request)
}

func nextLivenessState(state domain.ServiceState, success bool, successes, failures *int, spec ResolvedSpec) domain.ServiceState {
	if success {
		(*successes)++
		*failures = 0
		if state == domain.ServiceDegraded && *successes >= spec.SuccessThreshold {
			return domain.ServiceReady
		}
		return state
	}
	(*failures)++
	*successes = 0
	if state == domain.ServiceReady && *failures >= spec.FailureThreshold {
		return domain.ServiceDegraded
	}
	return state
}
