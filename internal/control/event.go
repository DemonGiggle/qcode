// Package control owns qcode's application-facing control plane.
//
// Terminal, HTTP, and other transports should issue commands through Host and
// observe state through its event stream instead of consuming AgentManager's
// presentation channel directly.
package control

import (
	"context"
	"sync"
	"time"

	"qcode/internal/session"
)

const defaultEventHistory = 256

type EventType string

const (
	EventAgentChanged         EventType = "agent.changed"
	EventConsultationChanged  EventType = "consultation.changed"
	EventInteractionRequested EventType = "interaction.requested"
	EventInteractionResolved  EventType = "interaction.resolved"
)

// Event is the transport-neutral envelope used by local and future remote
// clients. Sequence numbers let a reconnecting client ask for missed events.
type Event struct {
	Sequence     uint64           `json:"sequence"`
	Type         EventType        `json:"type"`
	Time         time.Time        `json:"time"`
	Agent        *session.Summary `json:"agent,omitempty"`
	Duration     time.Duration    `json:"duration_ns,omitempty"`
	Consultation bool             `json:"consultation,omitempty"`
	Interaction  *Interaction     `json:"interaction,omitempty"`
	Resolution   *Resolution      `json:"resolution,omitempty"`
}

// Subscription is an independent event cursor. A slow subscriber can miss
// events without delaying the agent runtime; sequence gaps tell it to refresh
// from Host.Snapshot.
type Subscription struct {
	Events <-chan Event
	cancel func()
	closed chan struct{}
	once   sync.Once
}

func (s *Subscription) Close() {
	if s == nil {
		return
	}
	s.once.Do(s.cancel)
}

type eventBus struct {
	mu          sync.Mutex
	next        uint64
	history     []Event
	historySize int
	subscribers map[uint64]*eventSubscriber
	nextSubID   uint64
	closed      bool
}

type eventSubscriber struct {
	events chan Event
	done   chan struct{}
}

func newEventBus(historySize int) *eventBus {
	if historySize <= 0 {
		historySize = defaultEventHistory
	}
	return &eventBus{historySize: historySize, subscribers: make(map[uint64]*eventSubscriber)}
}

func (b *eventBus) publish(event Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.next++
	event.Sequence = b.next
	if event.Time.IsZero() {
		event.Time = time.Now().UTC()
	}
	b.history = append(b.history, event)
	if len(b.history) > b.historySize {
		b.history = append([]Event(nil), b.history[len(b.history)-b.historySize:]...)
	}
	for _, subscriber := range b.subscribers {
		select {
		case subscriber.events <- event:
		default:
		}
	}
}

func (b *eventBus) subscribe(ctx context.Context, after uint64) *Subscription {
	b.mu.Lock()
	if b.closed {
		closed := make(chan Event)
		close(closed)
		b.mu.Unlock()
		done := make(chan struct{})
		close(done)
		return &Subscription{Events: closed, closed: done, cancel: func() {}}
	}
	replay := make([]Event, 0, len(b.history))
	for _, event := range b.history {
		if event.Sequence > after {
			replay = append(replay, event)
		}
	}
	capacity := defaultEventHistory
	if len(replay)+32 > capacity {
		capacity = len(replay) + 32
	}
	subscriber := &eventSubscriber{events: make(chan Event, capacity), done: make(chan struct{})}
	for _, event := range replay {
		subscriber.events <- event
	}
	b.nextSubID++
	id := b.nextSubID
	b.subscribers[id] = subscriber
	b.mu.Unlock()

	subscription := &Subscription{Events: subscriber.events, closed: subscriber.done}
	subscription.cancel = func() {
		b.mu.Lock()
		if current, ok := b.subscribers[id]; ok {
			delete(b.subscribers, id)
			close(current.events)
			close(current.done)
		}
		b.mu.Unlock()
	}
	if ctx != nil {
		go func() {
			select {
			case <-ctx.Done():
				subscription.Close()
			case <-subscription.closed:
			}
		}()
	}
	return subscription
}

func (b *eventBus) sequence() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.next
}

func (b *eventBus) close() {
	b.mu.Lock()
	if !b.closed {
		b.closed = true
		for id, subscriber := range b.subscribers {
			close(subscriber.events)
			close(subscriber.done)
			delete(b.subscribers, id)
		}
	}
	b.mu.Unlock()
}
