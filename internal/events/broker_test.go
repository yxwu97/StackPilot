package events

import (
	"testing"
	"time"

	"stackpilot/internal/domain"
)

func TestBrokerDisconnectsSlowSubscriberWithoutBlockingPublisher(t *testing.T) {
	broker := NewBroker(1)
	slow := broker.Subscribe()
	fast := broker.Subscribe()
	defer slow.Close()
	defer fast.Close()
	broker.Notify(domain.EventID(1))
	if received := <-fast.Events(); received != 1 {
		t.Fatalf("first fast notification = %d", received)
	}
	done := make(chan struct{})
	go func() {
		broker.Notify(domain.EventID(2))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Notify blocked on a full subscriber")
	}
	if first := <-slow.Events(); first != 1 {
		t.Fatalf("slow first notification = %d", first)
	}
	if _, open := <-slow.Events(); open {
		t.Fatal("slow subscriber remained open after overflow")
	}
	if received := <-fast.Events(); received != 2 {
		t.Fatalf("second fast notification = %d", received)
	}
}

func TestBrokerSubscriptionCloseIsIdempotent(t *testing.T) {
	broker := NewBroker(2)
	subscription := broker.Subscribe()
	subscription.Close()
	subscription.Close()
	if _, open := <-subscription.Events(); open {
		t.Fatal("closed subscription channel remained open")
	}
	broker.Notify(domain.EventID(1))
}
