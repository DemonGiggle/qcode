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

type interTurnCompactingProvider struct {
	requests   []llm.Request
	modelCalls int
}

func (*interTurnCompactingProvider) Name() string { return "inter-turn-compacting" }
func (p *interTurnCompactingProvider) Complete(_ context.Context, request llm.Request, _ llm.StreamCallback) (llm.Response, error) {
	p.requests = append(p.requests, request)
	if request.Messages[0].Content == prompt.ConversationCompact {
		return llm.Response{Message: llm.Message{Role: "assistant", Content: "Summary of prior work."}}, nil
	}
	p.modelCalls++
	if p.modelCalls == 1 {
		return llm.Response{
			Message: llm.Message{
				Role:      "assistant",
				Thinking:  strings.Repeat("reasoning ", 200),
				ToolCalls: []llm.ToolCall{{ID: "call_skill", Name: "skill"}},
			},
			Usage: &llm.Usage{InputTokens: 200, OutputTokens: 600},
		}, nil
	}
	return llm.Response{Message: llm.Message{Role: "assistant", Content: "done"}, Usage: &llm.Usage{InputTokens: 800}}, nil
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
	if len(p.requests) != 3 || p.requests[0].Messages[0].Content != prompt.ConversationCompact {
		t.Fatalf("requests = %+v", p.requests)
	}
	if p.requests[2].Messages[0].Content != prompt.ConversationCompact {
		t.Fatal("final response crossing the threshold was not compacted")
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

func TestAutoCompactRunsBetweenModelTurns(t *testing.T) {
	p := &interTurnCompactingProvider{}
	a := NewWithSystem(p, "test", &skillToolset{}, trace.New(io.Discard, false), io.Discard, 2, "system")
	a.SetContextWindow(1000)
	if err := a.Run(context.Background(), "next"); err != nil {
		t.Fatal(err)
	}
	if len(p.requests) != 4 {
		t.Fatalf("requests = %d, want initial completion, in-run compaction, next completion, and final compaction", len(p.requests))
	}
	if p.requests[1].Messages[0].Content != prompt.ConversationCompact {
		t.Fatalf("second provider request was not compaction: %q", p.requests[1].Messages[0].Content)
	}
	if p.requests[2].Messages[0].Content == prompt.ConversationCompact {
		t.Fatal("next model turn was not sent after compaction")
	}
	if p.requests[3].Messages[0].Content != prompt.ConversationCompact {
		t.Fatal("final response crossing the threshold was not compacted")
	}
	hasSummary, hasToolResult := false, false
	for _, message := range p.requests[2].Messages {
		hasSummary = hasSummary || strings.Contains(message.Content, "Summary of prior work.")
		hasToolResult = hasToolResult || message.Role == "tool" && strings.Contains(message.Content, "skill instructions")
	}
	if !hasSummary || !hasToolResult {
		t.Fatalf("next model turn lost compacted context: summary=%v tool_result=%v", hasSummary, hasToolResult)
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

type optimizingCompactProvider struct {
	requests []llm.Request
	streamed int
	sawNil   bool
}

func (*optimizingCompactProvider) Name() string { return "optimizing-compact" }

func (p *optimizingCompactProvider) Complete(_ context.Context, request llm.Request, onText llm.StreamCallback) (llm.Response, error) {
	p.requests = append(p.requests, request)
	if onText == nil {
		p.sawNil = true
		return llm.Response{Message: llm.Message{Role: "assistant", Content: "summary ok"}}, nil
	}
	onText(llm.StreamEvent{Kind: llm.StreamOutput, Text: "partial"})
	p.streamed++
	return llm.Response{Message: llm.Message{Role: "assistant", Content: "summary ok"}}, nil
}

func TestCompactSendsReducedPayload(t *testing.T) {
	p := &optimizingCompactProvider{}
	a := NewWithSystem(p, "test", &skillToolset{}, trace.New(io.Discard, false), io.Discard, 3, "system instructions")
	largeToolOutput := strings.Repeat("T", 10000)
	a.messages = append(a.messages,
		llm.Message{Role: "user", Content: "old question"},
		llm.Message{Role: "assistant", Content: "old answer", Thinking: "secret thinking", ReasoningDetails: []byte(`{"reasoning":"verbose"}`), Images: []llm.Image{{MediaType: "image/png", Data: []byte{1, 2, 3}}}},
		llm.Message{Role: "tool", ToolCallID: "call-large", Content: largeToolOutput},
		llm.Message{Role: "user", Content: "middle question"},
		llm.Message{Role: "assistant", Content: "middle answer"},
		llm.Message{Role: "user", Content: "TAIL-USER-1"},
		llm.Message{Role: "user", Content: "TAIL-USER-2"},
		llm.Message{Role: "user", Content: "TAIL-USER-3"},
		llm.Message{Role: "user", Content: "TAIL-USER-4"},
		llm.Message{Role: "user", Content: "TAIL-USER-5"},
		llm.Message{Role: "user", Content: "TAIL-USER-6"},
	)
	result, err := a.Compact(context.Background())
	if err != nil || result != "Conversation compacted." {
		t.Fatalf("compact = %q, %v", result, err)
	}
	if len(p.requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(p.requests))
	}
	req := p.requests[0]
	if req.Thinking != "" {
		t.Fatalf("compaction thinking = %q, want empty", req.Thinking)
	}
	if len(req.Tools) != 0 || req.Temperature != nil {
		t.Fatalf("compaction must not send tools or temperature: tools=%d temp=%v", len(req.Tools), req.Temperature)
	}
	if p.sawNil || p.streamed == 0 {
		t.Fatalf("compaction did not stream progress: sawNil=%v streamed=%d", p.sawNil, p.streamed)
	}
	if req.Messages[0].Content != prompt.ConversationCompact {
		t.Fatal("compaction request missing instructions")
	}
	for _, msg := range req.Messages[1:] {
		if len(msg.Images) != 0 {
			t.Fatalf("compaction must strip image payloads: %+v", msg)
		}
		if msg.Origin != "" || msg.Untrusted {
			t.Fatalf("compaction retained provenance metadata: %+v", msg)
		}
		if strings.Contains(msg.Content, "TAIL-USER-") {
			t.Fatalf("compaction re-summarized retained tail: %q", msg.Content)
		}
	}
	// Reasoning replay fields are required by Responses/Chat adapters to
	// accept assistant history; stripping them caused 400
	// "input_text is not valid on assistant messages".
	preservedThinking := false
	for _, msg := range req.Messages[1:] {
		if msg.Role == "assistant" && strings.HasPrefix(msg.Content, "old answer") {
			if msg.Thinking != "secret thinking" || len(msg.ReasoningDetails) == 0 {
				t.Fatalf("compaction dropped required replay fields: %+v", msg)
			}
			if !strings.Contains(msg.Content, "[1 image(s) omitted for compaction]") {
				t.Fatalf("image placeholder missing: %+v", msg)
			}
			if len(msg.Images) != 0 {
				t.Fatalf("image payload not stripped: %+v", msg)
			}
			preservedThinking = true
		}
	}
	if !preservedThinking {
		t.Fatal("old assistant turn missing from compaction request")
	}
	foundTruncated := false
	for _, msg := range req.Messages[1:] {
		if msg.Role == "tool" && strings.Contains(msg.Content, "[truncated") {
			foundTruncated = true
			if len(msg.Content) >= len(largeToolOutput) {
				t.Fatal("tool output was not reduced")
			}
			if msg.ToolCallID == "" {
				t.Fatal("truncation dropped tool pairing")
			}
		}
	}
	if !foundTruncated {
		t.Fatal("large tool output was not truncated with a marker")
	}
	// Retained tail must survive verbatim alongside the new summary.
	tailKept := false
	for _, msg := range a.messages {
		if strings.Contains(msg.Content, "TAIL-USER-6") {
			tailKept = true
		}
	}
	if !tailKept {
		t.Fatal("retained tail was lost")
	}
}
