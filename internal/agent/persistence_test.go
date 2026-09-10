package agent

import (
	"context"
	"encoding/json"
	"io"
	"reflect"
	"testing"

	"qcode/internal/llm"
	"qcode/internal/trace"
)

type resumeProvider struct{ request llm.Request }

func (*resumeProvider) Name() string { return "resume-test" }
func (p *resumeProvider) Complete(_ context.Context, r llm.Request, _ llm.StreamCallback) (llm.Response, error) {
	p.request = r
	return llm.Response{Message: llm.Message{Role: "assistant", Content: "continued"}, Usage: &llm.Usage{InputTokens: 10, OutputTokens: 2}}, nil
}

func TestAgentRestorePreservesImagesContextAndContinuation(t *testing.T) {
	provider := &resumeProvider{}
	a := New(provider, "saved-model", &managerToolset{}, trace.New(io.Discard, false), io.Discard, 9)
	a.messages = append(a.messages, llm.Message{Role: "user", Content: "inspect", Images: []llm.Image{{MediaType: "image/png", Data: []byte{1, 2, 3}}}}, llm.Message{Role: "assistant", Content: "answer", Thinking: "reasoning"})
	a.contextWindow = 10000
	a.contextUsage = &llm.Usage{InputTokens: 300, OutputTokens: 50}
	a.contextMessages = len(a.messages)
	a.sessionUsage = llm.SessionUsage{InputTokens: 500, OutputTokens: 80, TotalTokens: 580, Missing: 1}
	a.SetMaxSteps(12)
	a.publishContext()
	data := *a.checkpoint.Load()
	b := New(provider, "saved-model", &managerToolset{}, trace.New(io.Discard, false), io.Discard, 1)
	if err := b.RestoreState(data); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a.messages, b.messages) || a.SessionUsage() != b.SessionUsage() {
		t.Fatal("conversation or usage changed")
	}
	if b.MaxSteps() != 12 {
		t.Fatalf("max steps = %d, want 12", b.MaxSteps())
	}
	ar, ak, ae := a.ContextRemaining()
	br, bk, be := b.ContextRemaining()
	if ar != br || ak != bk || ae != be {
		t.Fatal("context display changed")
	}
	if err := b.Run(context.Background(), "continue"); err != nil {
		t.Fatal(err)
	}
	if provider.request.Model != "saved-model" || len(provider.request.Messages) != 4 || len(provider.request.Messages[1].Images) != 1 {
		t.Fatalf("wrong continuation: %+v", provider.request)
	}
	if b.SessionUsage().TotalTokens != 592 {
		t.Fatal(b.SessionUsage())
	}
}

func TestManagerRestoresAllTabsAndInterruptsUnfinishedWork(t *testing.T) {
	m := newTestManager(t, 4)
	sub, err := m.Create("other-model")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Start(sub.ID, "block"); err != nil {
		t.Fatal(err)
	}
	saved, next := m.SaveAgents()
	restored := NewAgentManager(context.Background(), 4)
	defer restored.Shutdown()
	restored.SetFactory(func(id, name, model string, main bool) (*Agent, error) {
		return New(&managerProvider{}, model, restored.WrapToolset(id, &managerToolset{}, main), trace.New(io.Discard, false), io.Discard, 4), nil
	})
	if err := restored.RestoreAgents(saved, next); err != nil {
		t.Fatal(err)
	}
	summary, _ := restored.Summary(sub.ID)
	if summary.Status != StatusCancelled || summary.Model != "other-model" || summary.Error == "" {
		t.Fatal(summary)
	}
	created, err := restored.Create("third-model")
	if err != nil || created.ID == sub.ID {
		t.Fatalf("ID collision: %v %v", created, err)
	}
}

func TestInterruptedCallsAreNotExecutedAgain(t *testing.T) {
	a := New(&resumeProvider{}, "model", &managerToolset{}, trace.New(io.Discard, false), io.Discard, 4)
	a.messages = append(a.messages, llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "done", Name: "read"}, {ID: "unknown", Name: "shell"}}}, llm.Message{Role: "tool", ToolCallID: "done", Content: "existing result"})
	a.repairInterruptedCalls()
	if len(a.messages) != 4 || a.messages[2].Content != "existing result" || a.messages[3].ToolCallID != "unknown" {
		t.Fatal(a.messages)
	}
	before, _ := json.Marshal(a.messages)
	a.repairInterruptedCalls()
	after, _ := json.Marshal(a.messages)
	if string(before) != string(after) {
		t.Fatal("repair is not idempotent")
	}
}

func TestCheckpointPreservesMalformedToolArgumentsAndPendingImages(t *testing.T) {
	a := New(&resumeProvider{}, "model", &managerToolset{}, trace.New(io.Discard, false), io.Discard, 4)
	a.messages = append(a.messages, llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "broken", Name: "read", Arguments: json.RawMessage(`{"unfinished":`)}}})
	a.pendingImages = []llm.Image{{MediaType: "image/png", Data: []byte{1, 2, 3}}}
	a.publishContext()
	b := New(&resumeProvider{}, "model", &managerToolset{}, trace.New(io.Discard, false), io.Discard, 4)
	if err := b.RestoreState(*a.checkpoint.Load()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a.messages, b.messages) || !reflect.DeepEqual(a.pendingImages, b.pendingImages) {
		t.Fatal("lost malformed call or loaded images")
	}
	b.repairInterruptedCalls()
	if len(b.messages[len(b.messages)-1].Images) != 1 || b.pendingImages != nil {
		t.Fatal("pending images unavailable to next request")
	}
}
