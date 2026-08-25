package events

import (
	"sync"

	"stackpilot/internal/domain"
)

const defaultSubscriberBuffer = 32

// Broker provides best-effort notification of already-committed event IDs.
type Broker struct {
	mutex       sync.Mutex
	nextID      uint64
	bufferSize  int
	subscribers map[uint64]*Subscription
}

// Subscription is one bounded notification channel. Its closure signals overflow or cancellation.
type Subscription struct {
	broker *Broker
	id     uint64
	events chan domain.EventID
	once   sync.Once
}

// NewBroker constructs a broker. Non-positive sizes use the bounded default.
func NewBroker(bufferSize int) *Broker {
	if bufferSize <= 0 {
		bufferSize = defaultSubscriberBuffer
	}
	return &Broker{bufferSize: bufferSize, subscribers: make(map[uint64]*Subscription)}
}

// Subscribe registers before the caller reads its database high-water mark.
func (broker *Broker) Subscribe() *Subscription {
	broker.mutex.Lock()
	defer broker.mutex.Unlock()
	broker.nextID++
	subscription := &Subscription{broker: broker, id: broker.nextID, events: make(chan domain.EventID, broker.bufferSize)}
	broker.subscribers[subscription.id] = subscription
	return subscription
}

// Notify offers a committed ID to every subscriber and disconnects slow consumers.
func (broker *Broker) Notify(id domain.EventID) {
	if id <= 0 {
		return
	}
	broker.mutex.Lock()
	defer broker.mutex.Unlock()
	for key, subscription := range broker.subscribers {
		select {
		case subscription.events <- id:
		default:
			delete(broker.subscribers, key)
			close(subscription.events)
		}
	}
}

// Events returns the bounded committed-ID notification channel.
func (subscription *Subscription) Events() <-chan domain.EventID { return subscription.events }

// Close unregisters and closes the subscription once.
func (subscription *Subscription) Close() {
	subscription.once.Do(func() {
		subscription.broker.mutex.Lock()
		defer subscription.broker.mutex.Unlock()
		if _, exists := subscription.broker.subscribers[subscription.id]; exists {
			delete(subscription.broker.subscribers, subscription.id)
			close(subscription.events)
		}
	})
}

var _ Notifier = (*Broker)(nil)
