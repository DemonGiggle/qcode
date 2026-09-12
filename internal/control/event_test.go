package control

import (
	"context"
	"testing"
	"time"

	"qcode/internal/session"
)

func TestEventBusBroadcastAndReplay(t *testing.T) {
	bus := newEventBus(4)
	first := bus.subscribe(context.Background(), 0)
	defer first.Close()
	second := bus.subscribe(context.Background(), 0)
	defer second.Close()

	bus.publish(Event{Type: EventAgentChanged, Agent: &session.Summary{ID: "main"}})
	for name, subscription := range map[string]*Subscription{"first": first, "second": second} {
		select {
		case event := <-subscription.Events:
			if event.Sequence != 1 || event.Agent == nil || event.Agent.ID != "main" {
				t.Fatalf("%s event = %+v", name, event)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s did not receive event", name)
		}
	}

	bus.publish(Event{Type: EventAgentChanged, Agent: &session.Summary{ID: "agent-1"}})
	replay := bus.subscribe(context.Background(), 1)
	defer replay.Close()
	select {
	case event := <-replay.Events:
		if event.Sequence != 2 || event.Agent == nil || event.Agent.ID != "agent-1" {
			t.Fatalf("replayed event = %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("subscriber did not receive replay")
	}
}

func TestEventBusSlowSubscriberDoesNotBlockPublisher(t *testing.T) {
	bus := newEventBus(2)
	subscription := bus.subscribe(context.Background(), 0)
	defer subscription.Close()
	done := make(chan struct{})
	go func() {
		for i := 0; i < defaultEventHistory*3; i++ {
			bus.publish(Event{Type: EventAgentChanged})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("slow subscriber blocked event publisher")
	}
}
