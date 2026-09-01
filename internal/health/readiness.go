package health

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"time"

	"stackpilot/internal/domain"
)

// JitterFunc returns the delay before the next sequential check.
type JitterFunc func(time.Duration, int) time.Duration

// Engine runs readiness checks sequentially and persists every completed result.
type Engine struct {
	checker  Checker
	recorder Recorder
	jitter   JitterFunc
}

// NewEngine constructs a readiness engine with bounded random jitter by default.
func NewEngine(checker Checker, recorder Recorder, jitter JitterFunc) (*Engine, error) {
	if checker == nil {
		return nil, fmt.Errorf("%w: checker", ErrInvalidSpec)
	}
	if recorder == nil {
		return nil, ErrRecorderRequired
	}
	if jitter == nil {
		jitter = randomJitter
	}
	return &Engine{checker: checker, recorder: recorder, jitter: jitter}, nil
}

// Await checks immediately, then sequentially waits until ready, terminal failure, cancellation, or timeout.
func (engine *Engine) Await(ctx context.Context, request Request) (Outcome, error) {
	if err := validateRequest(request); err != nil {
		return Outcome{}, err
	}
	readinessContext, cancel := context.WithTimeout(ctx, request.Spec.ReadinessTimeout)
	defer cancel()
	outcome := Outcome{}
	consecutive := 0
	for {
		result := engine.checkOnce(readinessContext, request.Spec)
		result.Purpose = PurposeReadiness
		var err error
		result, err = engine.record(readinessContext, request.ServiceInstanceID, result)
		if err != nil {
			return outcome, err
		}
		outcome.Attempts++
		outcome.LastResult = result
		if result.Success {
			consecutive++
			if consecutive >= request.Spec.SuccessThreshold {
				outcome.Ready = true
				return outcome, nil
			}
		} else {
			consecutive = 0
		}
		if terminalProcessFailure(result.ErrorCode) {
			outcome.ErrorCode = result.ErrorCode
			return outcome, nil
		}
		if err := readinessContext.Err(); err != nil {
			return timeoutOrCancellation(ctx, outcome)
		}
		if err := waitContext(readinessContext, engine.delay(request.Spec.Interval, outcome.Attempts)); err != nil {
			return timeoutOrCancellation(ctx, outcome)
		}
	}
}

func (engine *Engine) checkOnce(ctx context.Context, spec ResolvedSpec) Result {
	checkContext, cancel := context.WithTimeout(ctx, spec.CheckTimeout)
	defer cancel()
	return engine.checker.Check(checkContext, spec)
}

func (engine *Engine) record(ctx context.Context, id domain.ServiceInstanceID, result Result) (Result, error) {
	if ctx.Err() != nil && result.ErrorCode == "" {
		return result, nil
	}
	recordContext := ctx
	cancel := func() {}
	if ctx.Err() != nil {
		recordContext, cancel = context.WithTimeout(context.WithoutCancel(ctx), 250*time.Millisecond)
	}
	defer cancel()
	var err error
	if recorder, ok := engine.recorder.(IDRecorder); ok {
		result.ID, err = recorder.RecordWithID(recordContext, id, result)
	} else {
		err = engine.recorder.Record(recordContext, id, result)
	}
	if err != nil {
		return result, fmt.Errorf("persist health result: %w", err)
	}
	return result, nil
}

func (engine *Engine) delay(interval time.Duration, attempt int) time.Duration {
	delay := engine.jitter(interval, attempt)
	minimum, maximum := interval/2, interval+interval/2
	if delay < minimum {
		return minimum
	}
	if delay > maximum {
		return maximum
	}
	return delay
}

func validateRequest(request Request) error {
	if _, err := domain.ParseServiceInstanceID(request.ServiceInstanceID.String()); err != nil {
		return fmt.Errorf("%w: service instance", ErrInvalidSpec)
	}
	spec := request.Spec
	if spec.CheckTimeout <= 0 || spec.Interval <= 0 || spec.ReadinessTimeout <= 0 ||
		spec.CheckTimeout > spec.Interval || spec.Interval > spec.ReadinessTimeout ||
		spec.SuccessThreshold < 1 || spec.SuccessThreshold > 100 ||
		spec.FailureThreshold < 1 || spec.FailureThreshold > 100 {
		return fmt.Errorf("%w: timing or threshold", ErrInvalidSpec)
	}
	switch spec.Kind {
	case KindProcess:
		if spec.Identity.PID <= 0 || spec.Identity.StartedAt.IsZero() || spec.Identity.ExecutablePath == "" || spec.Identity.CommandDigest == "" || spec.Identity.PlatformToken == "" {
			return fmt.Errorf("%w: process identity", ErrInvalidSpec)
		}
	case KindTCP:
		if !isLoopbackName(spec.Host) || spec.Port < 1024 || spec.Port > 65535 {
			return fmt.Errorf("%w: TCP target", ErrInvalidSpec)
		}
	case KindHTTP:
		if _, err := safeURL(spec.URL); err != nil {
			return fmt.Errorf("%w: HTTP target", ErrInvalidSpec)
		}
	case KindCompose:
		if spec.ComposeIdentity == "" || len(spec.ComposeIdentity) > 64*1024 {
			return fmt.Errorf("%w: Compose identity", ErrInvalidSpec)
		}
	default:
		return fmt.Errorf("%w: kind", ErrInvalidSpec)
	}
	return nil
}

func timeoutOrCancellation(parent context.Context, outcome Outcome) (Outcome, error) {
	if err := parent.Err(); err != nil {
		return outcome, err
	}
	outcome.ErrorCode = CodeReadinessTimeout
	return outcome, nil
}

func terminalProcessFailure(code ErrorCode) bool {
	return code == CodeProcessExited || code == CodeProcessIdentityMismatch
}

func waitContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func randomJitter(interval time.Duration, _ int) time.Duration {
	var payload [2]byte
	if _, err := rand.Read(payload[:]); err != nil {
		return interval
	}
	fraction := int64(binary.LittleEndian.Uint16(payload[:])) - 32768
	return interval + time.Duration(int64(interval)*fraction/(32768*10))
}
