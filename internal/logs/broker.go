package logs

import (
	"sync"

	"stackpilot/internal/domain"
)

const (
	defaultLiveBuffer = 256
	defaultRingSize   = 1024
)

// Broker retains a bounded live window and disconnects slow log-stream consumers.
type Broker struct {
	mutex       sync.Mutex
	nextID      uint64
	bufferSize  int
	ringSize    int
	rings       map[domain.ServiceInstanceID][]Entry
	subscribers map[uint64]*Subscription
}

// Subscription contains the atomic pre-subscription backlog and future entries.
type Subscription struct {
	broker  *Broker
	id      uint64
	scope   domain.ServiceInstanceID
	backlog []Entry
	entries chan Entry
	once    sync.Once
}

// NewBroker constructs a broker with bounded subscriber and retained-window sizes.
func NewBroker(bufferSize, ringSize int) *Broker {
	if bufferSize <= 0 {
		bufferSize = defaultLiveBuffer
	}
	if ringSize <= 0 {
		ringSize = defaultRingSize
	}
	return &Broker{
		bufferSize: bufferSize, ringSize: ringSize, rings: make(map[domain.ServiceInstanceID][]Entry),
		subscribers: make(map[uint64]*Subscription),
	}
}

// Subscribe atomically registers and snapshots retained entries newer than the cursor.
func (broker *Broker) Subscribe(scope domain.ServiceInstanceID, after int64) *Subscription {
	broker.mutex.Lock()
	defer broker.mutex.Unlock()
	broker.nextID++
	subscription := &Subscription{
		broker: broker, id: broker.nextID, scope: scope, entries: make(chan Entry, broker.bufferSize),
		backlog: entriesAfter(broker.rings[scope], after),
	}
	broker.subscribers[subscription.id] = subscription
	return subscription
}

// Snapshot returns a copy of the current bounded live window for history merging.
func (broker *Broker) Snapshot(scope domain.ServiceInstanceID) []Entry {
	broker.mutex.Lock()
	defer broker.mutex.Unlock()
	return append([]Entry(nil), broker.rings[scope]...)
}

// Publish retains and offers a redacted persisted entry without blocking the log writer.
func (broker *Broker) Publish(scope domain.ServiceInstanceID, entry Entry) {
	broker.mutex.Lock()
	defer broker.mutex.Unlock()
	broker.rings[scope] = appendRing(broker.rings[scope], entry, broker.ringSize)
	for id, subscription := range broker.subscribers {
		if subscription.scope != scope {
			continue
		}
		select {
		case subscription.entries <- entry:
		default:
			delete(broker.subscribers, id)
			close(subscription.entries)
		}
	}
}

// Backlog returns a copy of entries retained at subscription time.
func (subscription *Subscription) Backlog() []Entry {
	return append([]Entry(nil), subscription.backlog...)
}

// Entries returns the bounded future-entry channel.
func (subscription *Subscription) Entries() <-chan Entry { return subscription.entries }

// Close unregisters one subscription idempotently.
func (subscription *Subscription) Close() {
	subscription.once.Do(func() {
		subscription.broker.mutex.Lock()
		defer subscription.broker.mutex.Unlock()
		if _, exists := subscription.broker.subscribers[subscription.id]; exists {
			delete(subscription.broker.subscribers, subscription.id)
			close(subscription.entries)
		}
	})
}

func appendRing(entries []Entry, entry Entry, limit int) []Entry {
	entries = append(entries, entry)
	if len(entries) > limit {
		copy(entries, entries[len(entries)-limit:])
		entries = entries[:limit]
	}
	return entries
}

func entriesAfter(entries []Entry, after int64) []Entry {
	result := make([]Entry, 0, len(entries))
	for _, entry := range entries {
		if entry.Sequence > after {
			result = append(result, entry)
		}
	}
	return result
}

var _ Publisher = (*Broker)(nil)
