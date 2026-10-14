package control

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"qcode/internal/agent"
	"qcode/internal/session"
	"qcode/internal/trace"
)

func TestCloseWithdrawsOnlyTargetInteractions(t *testing.T) {
	h := NewHost(context.Background(), 3)
	defer h.Shutdown()
	h.SetFactory(func(id, name, model string, main bool) (*agent.Agent, error) {
		return agent.New(&hostProvider{}, model, &hostTools{}, trace.New(io.Discard, false), io.Discard, 2), nil
	})
	if _, err := h.CreateMain("model"); err != nil {
		t.Fatal(err)
	}
	target, _ := h.Create("model")
	other, _ := h.Create("model")
	requests := make([]session.InteractionWaiter, 0)
	for _, kind := range []session.InteractionKind{session.InteractionQuestions, session.InteractionDirectoryApproval, session.InteractionPlanDecision} {
		request, err := h.BeginInteraction(session.Interaction{AgentID: target.ID, Kind: kind})
		if err != nil {
			t.Fatal(err)
		}
		requests = append(requests, request)
	}
	untouched, err := h.BeginInteraction(session.Interaction{AgentID: other.ID, Kind: session.InteractionQuestions})
	if err != nil {
		t.Fatal(err)
	}
	sub := h.Subscribe(context.Background(), h.Snapshot().Sequence)
	defer sub.Close()
	if err := h.Close(target.ID); err != nil {
		t.Fatal(err)
	}
	for _, request := range requests {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, err := request.Wait(ctx)
		cancel()
		if !errors.Is(err, ErrInteractionWithdrawn) {
			t.Fatalf("withdrawal: %v", err)
		}
		if err := h.ResolveInteraction(session.Resolution{InteractionID: request.InteractionInfo().ID}); err == nil {
			t.Fatal("late answer accepted")
		}
	}
	if _, err := h.BeginInteraction(session.Interaction{AgentID: target.ID, Kind: session.InteractionQuestions}); !errors.Is(err, agent.ErrClosedAgent) {
		t.Fatalf("late interaction: %v", err)
	}
	pending := h.PendingInteractions()
	if len(pending) != 1 || pending[0].ID != untouched.InteractionInfo().ID {
		t.Fatalf("other agent interactions changed: %+v", pending)
	}
	deadline := time.After(time.Second)
	for {
		select {
		case event := <-sub.Events:
			if event.Type == EventAgentClosed && event.Agent.ID == target.ID {
				return
			}
		case <-deadline:
			t.Fatal("explicit closed lifecycle event missing")
		}
	}
}

func TestHostCloseWithSaturatedLocalNotificationsAndBarrier(t *testing.T) {
	h := NewHost(context.Background(), 2)
	defer h.Shutdown()
	h.SetFactory(func(id, name, model string, main bool) (*agent.Agent, error) {
		return agent.New(&hostProvider{}, model, &hostTools{}, trace.New(io.Discard, false), io.Discard, 2), nil
	})
	if _, err := h.CreateMain("model"); err != nil {
		t.Fatal(err)
	}
	target, _ := h.Create("model")
	// Broadcast removal must survive a saturated local presentation buffer.
	for i := 0; i < cap(h.legacyEvents); i++ {
		h.legacyEvents <- session.Event{Consultation: true}
	}
	sub := h.Subscribe(context.Background(), 0)
	defer sub.Close()
	if err := h.Close(target.ID); err != nil {
		t.Fatal(err)
	}
	drained := make(chan struct{})
	go func() {
		for event := range h.Events() {
			if event.Barrier != nil {
				close(event.Barrier)
			}
		}
		close(drained)
	}()
	h.FlushEvents()
	if ids := h.ClosedIDs(); len(ids) != 1 || ids[0] != target.ID {
		t.Fatalf("closed IDs: %v", ids)
	}
	deadline := time.After(time.Second)
	for {
		select {
		case event := <-sub.Events:
			if event.Type == EventAgentClosed && event.Agent.ID == target.ID {
				h.Shutdown()
				<-drained
				return
			}
		case <-deadline:
			t.Fatal("saturated close did not reach broadcast")
		}
	}
}
