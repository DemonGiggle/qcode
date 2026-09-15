package control

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestInteractionBrokerFirstResolutionWins(t *testing.T) {
	broker := NewInteractionBroker()
	type requestResult struct {
		resolution Resolution
		err        error
	}
	result := make(chan requestResult, 1)
	go func() {
		resolved, err := broker.Request(context.Background(), Interaction{AgentID: "main", Kind: InteractionPlanDecision})
		result <- requestResult{resolution: resolved, err: err}
	}()

	deadline := time.Now().Add(time.Second)
	var pending []Interaction
	for len(pending) == 0 && time.Now().Before(deadline) {
		pending = broker.Pending()
		time.Sleep(time.Millisecond)
	}
	if len(pending) != 1 {
		t.Fatalf("pending interactions = %+v", pending)
	}
	resolution := Resolution{InteractionID: pending[0].ID, Value: json.RawMessage(`"implement"`), ResolvedBy: "local-tui"}
	if err := broker.Resolve(resolution); err != nil {
		t.Fatal(err)
	}
	if err := broker.Resolve(resolution); !errors.Is(err, ErrInteractionResolved) {
		t.Fatalf("second resolution error = %v", err)
	}
	select {
	case got := <-result:
		if got.err != nil {
			t.Fatal(got.err)
		}
		if got.resolution.InteractionID != resolution.InteractionID || string(got.resolution.Value) != `"implement"` {
			t.Fatalf("resolution = %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("request did not receive resolution")
	}
}

func TestInteractionBrokerRemovesCancelledRequest(t *testing.T) {
	broker := NewInteractionBroker()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := broker.Request(ctx, Interaction{AgentID: "main", Kind: InteractionQuestions})
		done <- err
	}()
	for len(broker.Pending()) == 0 {
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("request error = %v", err)
	}
	if pending := broker.Pending(); len(pending) != 0 {
		t.Fatalf("pending after cancellation = %+v", pending)
	}
}
