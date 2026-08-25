package domain

import "testing"

func TestQueuedOperationCanFailDuringRestartRecovery(t *testing.T) {
	t.Parallel()
	if !OperationQueued.CanTransitionTo(OperationFailed) {
		t.Fatal("queued Operation must support recovery failure after a control-plane restart")
	}
}

func TestServiceStateTransitions(t *testing.T) {
	t.Parallel()
	allowed := map[ServiceState][]ServiceState{
		ServiceStarting:     {ServiceWaitingReady, ServiceFailed, ServiceStopping, ServiceUnknown},
		ServiceWaitingReady: {ServiceReady, ServiceCompleted, ServiceFailed, ServiceStopping, ServiceUnknown},
		ServiceReady:        {ServiceDegraded, ServiceStopping, ServiceFailed, ServiceUnknown},
		ServiceDegraded:     {ServiceReady, ServiceStopping, ServiceFailed, ServiceUnknown},
		ServiceStopping:     {ServiceStopped, ServiceFailed, ServiceUnknown},
		ServiceFailed:       {ServiceStopping, ServiceStopped},
	}
	for from, targets := range allowed {
		for _, target := range targets {
			if !from.CanTransitionTo(target) {
				t.Fatalf("expected %s -> %s to be allowed", from, target)
			}
		}
	}
	if ServiceReady.CanTransitionTo(ServiceStarting) || ServiceStopped.CanTransitionTo(ServiceReady) {
		t.Fatal("illegal service transition was allowed")
	}
}

func TestSystemStateTransitions(t *testing.T) {
	t.Parallel()
	if !SystemStarting.CanTransitionTo(SystemRunning) || !SystemRunning.CanTransitionTo(SystemStopping) ||
		!SystemStopping.CanTransitionTo(SystemStopped) {
		t.Fatal("expected system lifecycle transitions to be allowed")
	}
	if SystemStopped.CanTransitionTo(SystemStarting) || SystemRunning.CanTransitionTo(SystemStopped) {
		t.Fatal("illegal system transition was allowed")
	}
}
