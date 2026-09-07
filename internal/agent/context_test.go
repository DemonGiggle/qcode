package agent

import (
	"context"
	"io"
	"strings"
	"testing"

	"qcode/internal/llm"
	"qcode/internal/prompt"
	"qcode/internal/trace"
)

type usageProvider struct{ calls int }

type compactingProvider struct {
	requests []llm.Request
}

func (*compactingProvider) Name() string { return "compacting" }
func (p *compactingProvider) Complete(_ context.Context, request llm.Request, _ llm.StreamCallback) (llm.Response, error) {
	p.requests = append(p.requests, request)
	if request.Messages[0].Content == prompt.ConversationCompact {
		return llm.Response{Message: llm.Message{Role: "assistant", Content: "User chose Go. Work remains: tests."}}, nil
	}
	return llm.Response{Message: llm.Message{Role: "assistant", Content: "done"}, Usage: &llm.Usage{InputTokens: 900}}, nil
}

func (*usageProvider) Name() string                                       { return "usage" }
func (*usageProvider) ContextWindow(context.Context, string) (int, error) { return 1000, nil }
func (p *usageProvider) Complete(context.Context, llm.Request, llm.StreamCallback) (llm.Response, error) {
	p.calls++
	return llm.Response{Message: llm.Message{Role: "assistant", Content: "hi"}, Usage: &llm.Usage{InputTokens: 700, OutputTokens: 50}}, nil
}

func TestContextTracksLatestRequestAndReset(t *testing.T) {
	p := &usageProvider{}
	a := New(p, "test", &skillToolset{}, trace.New(io.Discard, false), io.Discard, 3)
	a.RefreshContext(context.Background())
	if _, known, estimated := a.ContextRemaining(); !known || !estimated {
		t.Fatal("initial estimate missing")
	}
	for range 2 {
		if err := a.Run(context.Background(), "hello"); err != nil {
			t.Fatal(err)
		}
		if left, known, estimated := a.ContextRemaining(); left != 25 || !known || estimated {
			t.Fatalf("context=%d %v %v", left, known, estimated)
		}
	}
	a.ResetSession()
	if _, _, estimated := a.ContextRemaining(); !estimated || a.contextUsage != nil {
		t.Fatal("reset retained usage")
	}
	a.SetContextWindow(500)
	a.contextUsage = &llm.Usage{InputTokens: 700, OutputTokens: 50}
	a.contextMessages = len(a.messages)
	a.publishContext()
	if left, _, _ := a.ContextRemaining(); left != 0 {
		t.Fatalf("overflow=%d", left)
	}
	a.SetModel("other")
	if _, known, _ := a.ContextRemaining(); known {
		t.Fatal("model switch retained old capacity")
	}
}

func TestContextIncludesPendingToolResults(t *testing.T) {
	a := New(&usageProvider{}, "test", &skillToolset{}, trace.New(io.Discard, false), io.Discard, 3)
	a.SetContextWindow(1000)
	a.contextUsage = &llm.Usage{InputTokens: 500, OutputTokens: 50}
	a.contextMessages = len(a.messages)
	a.messages = append(a.messages, llm.Message{Role: "tool", Content: "new tool output"})
	a.publishContext()
	left, known, estimated := a.ContextRemaining()
	if left >= 45 || !known || !estimated {
		t.Fatalf("pending context=%d %v %v", left, known, estimated)
	}
}

func TestContextMissingUsageAndChangedSettings(t *testing.T) {
	a := New(&usageProvider{}, "test", &skillToolset{}, trace.New(io.Discard, false), io.Discard, 3)
	a.SetContextWindow(10000)
	a.contextUsage = &llm.Usage{InputTokens: 2900}
	a.contextMessages = len(a.messages)
	a.publishContext()
	if left, _, _ := a.ContextRemaining(); left != 71 {
		t.Fatalf("rounding=%d", left)
	}
	a.SetSkills(nil)
	if _, known, estimated := a.ContextRemaining(); !known || !estimated {
		t.Fatal("skill change retained measured usage")
	}
	a.contextUsage = &llm.Usage{InputTokens: 2000}
	a.ToggleTool("skill", false)
	if _, known, estimated := a.ContextRemaining(); !known || !estimated {
		t.Fatal("tool change retained measured usage")
	}
	a.contextUsage = nil
	a.publishContext()
	if left, known, estimated := a.ContextRemaining(); left < 0 || left > 100 || !known || !estimated {
		t.Fatal("invalid fallback estimate")
	}
}

func TestCompactPreservesSystemSummaryAndRecentToolHistory(t *testing.T) {
	p := &compactingProvider{}
	a := NewWithSystem(p, "test", &skillToolset{}, trace.New(io.Discard, false), io.Discard, 3, "system instructions")
	a.messages = append(a.messages,
		llm.Message{Role: "user", Content: "old decision"},
		llm.Message{Role: "assistant", Content: "old response"},
		llm.Message{Role: "user", Content: "new request"},
		llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "recent-call", Name: "skill"}}},
		llm.Message{Role: "tool", ToolCallID: "recent-call", Content: "recent result"},
	)
	result, err := a.Compact(context.Background())
	if err != nil || result != "Conversation compacted." {
		t.Fatalf("compact = %q, %v", result, err)
	}
	if len(p.requests) != 1 || p.requests[0].Messages[0].Content != prompt.ConversationCompact {
		t.Fatal("compaction request missing instructions")
	}
	if a.messages[0].Role != "system" || a.messages[0].Content != "system instructions" || a.messages[1].Role != "user" || !strings.Contains(a.messages[1].Content, "User chose Go") {
		t.Fatalf("messages = %+v", a.messages[:2])
	}
	if a.messages[len(a.messages)-1].Role != "tool" || a.messages[len(a.messages)-1].ToolCallID != "recent-call" {
		t.Fatalf("recent tool history was not preserved: %+v", a.messages)
	}
	if a.contextUsage != nil {
		t.Fatal("compaction retained stale context usage")
	}
}

func TestAutoCompactOnlyRunsForKnownCapacity(t *testing.T) {
	p := &compactingProvider{}
	a := New(p, "test", &skillToolset{}, trace.New(io.Discard, false), io.Discard, 1)
	a.messages = append(a.messages, llm.Message{Role: "user", Content: "old"}, llm.Message{Role: "assistant", Content: "old"})
	a.SetContextWindow(100)
	a.contextUsage = &llm.Usage{InputTokens: 90}
	a.contextMessages = len(a.messages)
	a.publishContext()
	if err := a.Run(context.Background(), "next"); err != nil {
		t.Fatal(err)
	}
	if len(p.requests) != 2 || p.requests[0].Messages[0].Content != prompt.ConversationCompact {
		t.Fatalf("requests = %+v", p.requests)
	}
	p = &compactingProvider{}
	a = New(p, "test", &skillToolset{}, trace.New(io.Discard, false), io.Discard, 1)
	a.messages = append(a.messages, llm.Message{Role: "user", Content: "old"})
	if err := a.Run(context.Background(), "next"); err != nil {
		t.Fatal(err)
	}
	if len(p.requests) != 1 {
		t.Fatalf("unknown capacity compacted: %d requests", len(p.requests))
	}
}

func TestManualCompactWorksWhenAutomaticCompactionIsDisabled(t *testing.T) {
	p := &compactingProvider{}
	a := New(p, "test", &skillToolset{}, trace.New(io.Discard, false), io.Discard, 1)
	a.SetAutoCompact(false, DefaultAutoCompactThreshold)
	a.messages = append(a.messages, llm.Message{Role: "user", Content: "old"}, llm.Message{Role: "assistant", Content: "old"})
	a.SetContextWindow(100)
	a.contextUsage = &llm.Usage{InputTokens: 90}
	a.contextMessages = len(a.messages)
	a.publishContext()
	if err := a.Run(context.Background(), "next"); err != nil {
		t.Fatal(err)
	}
	if len(p.requests) != 1 {
		t.Fatalf("disabled auto compaction made %d requests", len(p.requests))
	}
	if _, err := a.Compact(context.Background()); err != nil {
		t.Fatalf("manual compaction unavailable: %v", err)
	}
	if len(p.requests) != 2 || p.requests[1].Messages[0].Content != prompt.ConversationCompact {
		t.Fatal("manual compaction did not run")
	}
}
