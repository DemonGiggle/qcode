package control

import (
	"context"
	"io"
	"testing"
	"time"

	"qcode/internal/agent"
	"qcode/internal/llm"
	"qcode/internal/trace"
)

type hostProvider struct{}

func (*hostProvider) Name() string { return "host-test" }
func (*hostProvider) Complete(context.Context, llm.Request, llm.StreamCallback) (llm.Response, error) {
	return llm.Response{Message: llm.Message{Role: "assistant", Content: "done"}}, nil
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
