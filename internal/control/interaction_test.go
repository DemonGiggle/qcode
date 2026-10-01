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

func TestWithdrawalResolutionRaceAndTaskIsolation(t *testing.T) {
	for i := 0; i < 100; i++ {
		broker := NewInteractionBroker()
		request, err := broker.Begin(Interaction{AgentID: "main", TaskID: "task-1", Kind: InteractionQuestions})
		if err != nil {
			t.Fatal(err)
		}
		other, _ := broker.Begin(Interaction{AgentID: "agent-1", TaskID: "task-1", Kind: InteractionDirectoryApproval})
		next, _ := broker.Begin(Interaction{AgentID: "main", TaskID: "task-2", Kind: InteractionDirectoryApproval})
		gate := make(chan struct{})
		resolved := make(chan error, 1)
		withdrawn := make(chan struct{})
		go func() {
			<-gate
			resolved <- broker.Resolve(Resolution{InteractionID: request.Interaction.ID, Value: json.RawMessage(`"answer"`)})
		}()
		go func() { <-gate; broker.WithdrawTask("main", "task-1"); close(withdrawn) }()
		close(gate)
		resolveErr := <-resolved
		<-withdrawn
		result, waitErr := request.Wait(context.Background())
		if result.Withdrawn {
			if !errors.Is(waitErr, ErrInteractionWithdrawn) || !errors.Is(resolveErr, ErrInteractionResolved) {
				t.Fatalf("withdrawal lost arbitration: %v %v", waitErr, resolveErr)
			}
		} else if waitErr != nil || resolveErr != nil {
			t.Fatalf("resolution lost arbitration: %v %v", waitErr, resolveErr)
		}
		if err := broker.Resolve(Resolution{InteractionID: request.Interaction.ID}); !errors.Is(err, ErrInteractionResolved) {
			t.Fatal("late answer accepted")
		}
		if len(broker.Pending()) != 2 {
			t.Fatal("withdrawal crossed task or agent boundary")
		}
		_ = broker.Resolve(Resolution{InteractionID: other.Interaction.ID})
		_ = broker.Resolve(Resolution{InteractionID: next.Interaction.ID})
	}
}
