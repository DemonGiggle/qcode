package control

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"qcode/internal/agent"
	"qcode/internal/llm"
	"qcode/internal/session"
	"qcode/internal/trace"
)

type hostProvider struct{}

func (*hostProvider) Name() string { return "host-test" }
func (*hostProvider) Complete(context.Context, llm.Request, llm.StreamCallback) (llm.Response, error) {
	return llm.Response{Message: llm.Message{Role: "assistant", Content: "done"}}, nil
}

type hostBlockingProvider struct {
	started chan<- struct{}
	release <-chan struct{}
}

func (p *hostBlockingProvider) Name() string { return "host-blocking-test" }
func (p *hostBlockingProvider) Complete(ctx context.Context, _ llm.Request, _ llm.StreamCallback) (llm.Response, error) {
	p.started <- struct{}{}
	select {
	case <-p.release:
		return llm.Response{Message: llm.Message{Role: "assistant", Content: "done"}}, nil
	case <-ctx.Done():
		return llm.Response{}, ctx.Err()
	}
}

type hostTools struct{}

func (*hostTools) Schemas() []llm.Tool        { return nil }
func (*hostTools) EnabledSchemas() []llm.Tool { return nil }
func (*hostTools) ExecuteDetailed(context.Context, llm.ToolCall) (llm.ToolResult, error) {
	return llm.ToolResult{}, nil
}

func TestHostSharesRuntimeWithIndependentSubscribers(t *testing.T) {
	host := NewHost(context.Background(), 2)
	defer host.Shutdown()
	host.SetFactory(func(id, name, model string, main bool) (*agent.Agent, error) {
		tools := host.WrapToolset(id, &hostTools{}, main)
		return agent.New(&hostProvider{}, model, tools, trace.New(io.Discard, false), io.Discard, 2), nil
	})
	if _, err := host.CreateMain("test-model"); err != nil {
		t.Fatal(err)
	}

	first := host.Subscribe(context.Background(), 0)
	defer first.Close()
	second := host.Subscribe(context.Background(), 0)
	defer second.Close()
	if _, err := host.Submit("main", "work"); err != nil {
		t.Fatal(err)
	}

	for name, subscription := range map[string]*Subscription{"first": first, "second": second} {
		deadline := time.After(time.Second)
		completed := false
		for !completed {
			select {
			case event := <-subscription.Events:
				completed = event.Agent != nil && event.Agent.ID == "main" && event.Agent.Status == agent.StatusCompleted
			case <-deadline:
				t.Fatalf("%s subscriber did not observe completion", name)
			}
		}
	}

	snapshot := host.Snapshot()
	if snapshot.Sequence == 0 || len(snapshot.Agents) != 1 || snapshot.Agents[0].Status != agent.StatusCompleted {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	records := host.WorkRecords()
	if len(records) != 1 || records[0].AgentID != "main" || records[0].Prompt != "work" || records[0].Response != "done" {
		t.Fatalf("work records = %+v", records)
	}
}

func TestHostExposesQueuedPromptsToPresentation(t *testing.T) {
	host := NewHost(context.Background(), 1)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	defer func() {
		close(release)
		host.Shutdown()
	}()
	host.SetFactory(func(id, name, model string, main bool) (*agent.Agent, error) {
		tools := host.WrapToolset(id, &hostTools{}, main)
		provider := &hostBlockingProvider{started: started, release: release}
		return agent.New(provider, model, tools, trace.New(io.Discard, false), io.Discard, 2), nil
	})
	if _, err := host.CreateMain("test-model"); err != nil {
		t.Fatal(err)
	}
	if _, err := host.Submit("main", "active prompt"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("active prompt did not start")
	}
	if _, err := host.Submit("main", "queued prompt"); err != nil {
		t.Fatal(err)
	}
	queued := host.QueuedPrompts("main")
	if len(queued) != 1 || queued[0].Prompt != "queued prompt" {
		t.Fatalf("queued prompts = %+v", queued)
	}
}

func TestHostPublishesInteractionLifecycle(t *testing.T) {
	host := NewHost(context.Background(), 1)
	defer host.Shutdown()
	subscription := host.Subscribe(context.Background(), 0)
	defer subscription.Close()
	result := make(chan error, 1)
	go func() {
		_, err := host.Interactions().Request(context.Background(), Interaction{
			AgentID: "main", Kind: InteractionDirectoryApproval,
		})
		result <- err
	}()

	requested := <-subscription.Events
	if requested.Type != EventInteractionRequested || requested.Interaction == nil {
		t.Fatalf("requested event = %+v", requested)
	}
	resolution := Resolution{InteractionID: requested.Interaction.ID, ResolvedBy: "local-tui"}
	if err := host.Interactions().Resolve(resolution); err != nil {
		t.Fatal(err)
	}
	resolved := <-subscription.Events
	if resolved.Type != EventInteractionResolved || resolved.Resolution == nil || resolved.Resolution.InteractionID != resolution.InteractionID {
		t.Fatalf("resolved event = %+v", resolved)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}

func TestHostSteeringWithdrawsOnlyOwningTaskAndRejectsLateAnswers(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	host := NewHost(context.Background(), 2)
	defer host.Shutdown()
	host.SetFactory(func(id, name, model string, main bool) (*agent.Agent, error) {
		return agent.New(&hostBlockingProvider{started: started, release: release}, model, &hostTools{}, trace.New(io.Discard, false), io.Discard, 2), nil
	})
	_, _ = host.CreateMain("test")
	sub, err := host.Submit("main", "task")
	if err != nil {
		t.Fatal(err)
	}
	<-started
	request, err := host.BeginInteraction(session.Interaction{AgentID: "main", Kind: session.InteractionQuestions})
	if err != nil {
		t.Fatal(err)
	}
	if request.InteractionInfo().TaskID != sub.RequestID {
		t.Fatal("interaction lacks owning task")
	}
	other, _ := host.Interactions().Begin(session.Interaction{AgentID: "main", TaskID: "another-task", Kind: session.InteractionQuestions})
	steer, err := host.SubmitPrompt(session.PromptSubmission{AgentID: "main", ObservedTaskID: sub.RequestID, Prompt: "new direction", Intent: session.IntentSteer})
	if err != nil {
		t.Fatal(err)
	}
	result, err := request.Wait(context.Background())
	if !errors.Is(err, ErrInteractionWithdrawn) || !result.Withdrawn {
		t.Fatalf("withdrawal=%+v %v", result, err)
	}
	if err := host.ResolveInteraction(session.Resolution{InteractionID: request.InteractionInfo().ID}); !errors.Is(err, ErrInteractionResolved) {
		t.Fatal("late answer accepted")
	}
	if _, err := host.BeginInteraction(session.Interaction{AgentID: "main", Kind: session.InteractionDirectoryApproval}); err == nil {
		t.Fatal("obsolete interaction opened after steering")
	}
	snapshot := host.Snapshot()
	if len(snapshot.PendingInputs["main"]) != 1 || snapshot.PendingInputs["main"][0].RequestID != steer.RequestID || len(snapshot.SteeringEvents) != 1 {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	if len(snapshot.Interactions) != 1 || snapshot.Interactions[0].ID != other.Interaction.ID {
		t.Fatal("withdrawal crossed task boundary")
	}
	_ = host.Cancel("main")
}
