package agent

import (
	"context"
	"io"
	"testing"
	"time"

	"qcode/internal/llm"
	"qcode/internal/session"
	"qcode/internal/trace"
)

type progressProvider struct {
	observe func()
	calls   int
}

func (*progressProvider) Name() string { return "progress-test" }
func (p *progressProvider) Complete(_ context.Context, _ llm.Request, callback llm.StreamCallback) (llm.Response, error) {
	p.calls++
	p.observe()
	if p.calls == 1 {
		callback(llm.StreamEvent{Kind: llm.StreamThinking, Text: "thinking"})
		p.observe()
		callback(llm.StreamEvent{Kind: llm.StreamThinking, Text: "more thinking"})
		p.observe()
		callback(llm.StreamEvent{Kind: llm.StreamOutput, Text: "reading"})
		p.observe()
		return llm.Response{Message: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "shell-1", Name: "shell", Arguments: []byte(`{"command":"echo hello"}`)}}}}, nil
	}
	return llm.Response{Message: llm.Message{Role: "assistant", Content: "done"}}, nil
}

type progressToolset struct {
	managerToolset
	observe func()
	started chan struct{}
	release chan struct{}
}

func (p *progressToolset) ExecuteDetailed(ctx context.Context, _ llm.ToolCall) (llm.ToolResult, error) {
	p.observe()
	close(p.started)
	select {
	case <-p.release:
		return llm.ToolResult{Output: "file content"}, nil
	case <-ctx.Done():
		return llm.ToolResult{}, ctx.Err()
	}
}

func TestProgressTracksModelStreamingAndClearsDuringTools(t *testing.T) {
	manager := NewAgentManager(context.Background(), 1)
	t.Cleanup(manager.Shutdown)
	observations := make(chan session.Progress, 8)
	observe := func() {
		summary, _ := manager.Summary("main")
		if summary.Progress == nil {
			observations <- session.Progress{}
		} else {
			// Callers own their snapshots and cannot change live model progress.
			summary.Progress.Summary = "changed by caller"
			live, _ := manager.Summary("main")
			observations <- *live.Progress
		}
	}
	toolset := &progressToolset{observe: observe, started: make(chan struct{}), release: make(chan struct{})}
	provider := &progressProvider{observe: observe}
	manager.SetFactory(func(_, _, model string, _ bool) (*Agent, error) {
		return New(provider, model, toolset, trace.New(io.Discard, false), io.Discard, 4), nil
	})
	if _, err := manager.CreateMain("test-model"); err != nil {
		t.Fatal(err)
	}
	if err := manager.Start("main", "run command"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-toolset.started:
	case <-time.After(2 * time.Second):
		t.Fatal("tool did not start")
	}
	var previous session.Progress
	for i, want := range []string{
		"Waiting for model response",
		"Receiving model thinking",
		"Receiving model thinking",
		"Receiving model response",
	} {
		got := <-observations
		if got.Summary != want || got.StartedAt.IsZero() {
			t.Fatalf("progress %d = %+v, want %q", i, got, want)
		}
		if i == 2 && !got.StartedAt.Equal(previous.StartedAt) {
			t.Fatal("each thinking chunk restarted the operation timer")
		}
		previous = got
	}
	if got := <-observations; got.Summary != "" || !got.StartedAt.IsZero() {
		t.Fatalf("tool execution retained model progress: %+v", got)
	}
	live, _ := manager.Summary("main")
	if live.Status != StatusRunning || live.Progress != nil {
		t.Fatalf("running shell summary = %+v", live)
	}
	saved, _ := manager.SaveAgents()
	if len(saved) != 1 || saved[0].Summary.Progress != nil {
		t.Fatal("transient progress was saved in the session")
	}
	close(toolset.release)
	completed := waitManagerStatus(t, manager, "main", StatusCompleted)
	if completed.Progress != nil {
		t.Fatalf("completed progress = %+v", completed.Progress)
	}
	if got := <-observations; got.Summary != "Waiting for model response" {
		t.Fatalf("next model turn retained tool progress: %+v", got)
	}
}

func TestProgressClearsOnCancellationAndAgentReuse(t *testing.T) {
	manager := newTestManager(t, 1)
	if err := manager.Start("main", "block"); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(2 * time.Second)
	for {
		summary, _ := manager.Summary("main")
		if summary.Progress != nil && summary.Progress.Summary == "Waiting for model response" {
			break
		}
		select {
		case <-manager.Events():
		case <-deadline:
			t.Fatal("silent model request did not publish its progress")
		}
	}
	if err := manager.Cancel("main"); err != nil {
		t.Fatal(err)
	}
	if cancelled := waitManagerStatus(t, manager, "main", StatusCancelled); cancelled.Progress != nil {
		t.Fatalf("cancelled progress = %+v", cancelled.Progress)
	}
	result, err := manager.SubmitAndWait(context.Background(), "main", "reused")
	if err != nil || result.Response != "handled reused" {
		t.Fatalf("reused result = %+v, %v", result, err)
	}
	if completed := waitManagerStatus(t, manager, "main", StatusCompleted); completed.Progress != nil {
		t.Fatalf("reused progress = %+v", completed.Progress)
	}
}
