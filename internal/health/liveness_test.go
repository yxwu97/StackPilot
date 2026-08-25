package health

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"stackpilot/internal/domain"
)

func TestMonitorLivenessTransitionsOnlyAtConsecutiveThresholds(t *testing.T) {
	checker := &sequenceChecker{results: []Result{
		{ErrorCode: CodeTCPRefused},
		{Success: true},
		{ErrorCode: CodeTCPRefused},
		{ErrorCode: CodeTCPRefused},
		{Success: true},
		{Success: true},
	}}
	recorder := &memoryRecorder{}
	engine, _ := NewEngine(checker, recorder, func(time.Duration, int) time.Duration { return time.Millisecond })
	ctx, cancel := context.WithCancel(context.Background())
	handler := &recordingLivenessHandler{cancel: cancel}
	err := engine.MonitorLiveness(ctx, livenessRequest(), handler)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("MonitorLiveness() error = %v", err)
	}
	if len(handler.transitions) != 2 || handler.transitions[0].To != domain.ServiceDegraded || handler.transitions[1].To != domain.ServiceReady {
		t.Fatalf("transitions = %#v", handler.transitions)
	}
	if len(recorder.results) != 6 {
		t.Fatalf("recorded results = %d, want 6", len(recorder.results))
	}
}

func TestMonitorLivenessRejectsInvalidInitialState(t *testing.T) {
	engine, _ := NewEngine(&sequenceChecker{}, &memoryRecorder{}, noJitter)
	request := livenessRequest()
	request.InitialState = domain.ServiceStarting
	if err := engine.MonitorLiveness(context.Background(), request, &recordingLivenessHandler{}); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("MonitorLiveness() error = %v", err)
	}
}

func livenessRequest() LivenessRequest {
	return LivenessRequest{
		ServiceInstanceID: domain.ServiceInstanceID("svi_01ARZ3NDEKTSV4RRFFQ69G5FAV"),
		InitialState:      domain.ServiceReady,
		Spec: ResolvedSpec{
			Kind: KindTCP, Host: "127.0.0.1", Port: 32102,
			CheckTimeout: time.Millisecond, Interval: 2 * time.Millisecond,
			SuccessThreshold: 2, FailureThreshold: 2,
		},
	}
}

type recordingLivenessHandler struct {
	mutex       sync.Mutex
	transitions []LivenessTransition
	cancel      context.CancelFunc
}

func (handler *recordingLivenessHandler) HandleLiveness(_ context.Context, _ domain.ServiceInstanceID, transition LivenessTransition) error {
	handler.mutex.Lock()
	defer handler.mutex.Unlock()
	handler.transitions = append(handler.transitions, transition)
	if len(handler.transitions) == 2 && handler.cancel != nil {
		handler.cancel()
	}
	return nil
}
