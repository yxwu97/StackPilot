package logs

import (
	"testing"
	"time"

	"stackpilot/internal/domain"
)

func TestLogBrokerSnapshotsBacklogAndFiltersScopes(t *testing.T) {
	broker := NewBroker(2, 3)
	scope := testScope().ServiceInstanceID
	other := domain.ServiceInstanceID("svi_01ARZ3NDEKTSV4RRFFQ69G5FAW")
	for sequence := int64(1); sequence <= 4; sequence++ {
		broker.Publish(scope, Entry{Sequence: sequence, Message: "redacted"})
	}
	broker.Publish(other, Entry{Sequence: 9})
	subscription := broker.Subscribe(scope, 2)
	defer subscription.Close()
	backlog := subscription.Backlog()
	if len(backlog) != 2 || backlog[0].Sequence != 3 || backlog[1].Sequence != 4 {
		t.Fatalf("Backlog() = %#v", backlog)
	}
	broker.Publish(other, Entry{Sequence: 10})
	broker.Publish(scope, Entry{Sequence: 5})
	if entry := <-subscription.Entries(); entry.Sequence != 5 {
		t.Fatalf("live entry = %#v", entry)
	}
}

func TestLogBrokerDisconnectsSlowConsumerWithoutBlockingPublisher(t *testing.T) {
	broker := NewBroker(1, 2)
	scope := testScope().ServiceInstanceID
	subscription := broker.Subscribe(scope, 0)
	defer subscription.Close()
	broker.Publish(scope, Entry{Sequence: 1})
	done := make(chan struct{})
	go func() {
		broker.Publish(scope, Entry{Sequence: 2})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Publish blocked on a slow consumer")
	}
	if entry := <-subscription.Entries(); entry.Sequence != 1 {
		t.Fatalf("first entry = %#v", entry)
	}
	if _, open := <-subscription.Entries(); open {
		t.Fatal("slow subscription remained open")
	}
}
