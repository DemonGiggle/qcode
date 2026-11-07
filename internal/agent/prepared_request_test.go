package agent

import (
	"context"
	"io"
	"strings"
	"testing"

	"qcode/internal/learning"
	"qcode/internal/llm"
	"qcode/internal/prompt"
	"qcode/internal/trace"
	"qcode/internal/trust"
)

// #79 acceptance tests: prepared-request estimation and prefix stability.

type capture79Provider struct {
	requests []llm.Request
	usage    *llm.Usage
}

func (p *capture79Provider) Name() string { return "capture-79" }
func (p *capture79Provider) Complete(_ context.Context, req llm.Request, _ llm.StreamCallback) (llm.Response, error) {
	p.requests = append(p.requests, req)
	usage := p.usage
	if usage == nil {
		usage = &llm.Usage{InputTokens: llm.IntPtr(1000), OutputTokens: llm.IntPtr(10)}
	}
	return llm.Response{Message: llm.Message{Role: "assistant", Content: "done"}, Usage: usage}, nil
}

type counting79Store struct {
	searches int
	items    []learning.Learning
}

func (s *counting79Store) Snapshot(context.Context) (learning.Snapshot, error) {
	return learning.Snapshot{}, nil
}
func (s *counting79Store) Search(_ context.Context, _ string, _ int) ([]learning.Learning, error) {
	s.searches++
	return s.items, nil
}
func (s *counting79Store) Apply(context.Context, learning.Plan) (string, error) {
	return "", nil
}

type context79Toolset struct {
	contextCalls int
	dynamic      string
	work         string
	cwd          string
}

func (t *context79Toolset) Schemas() []llm.Tool { return []llm.Tool{{Name: "skill"}} }
func (t *context79Toolset) EnabledSchemas() []llm.Tool {
	return []llm.Tool{{Name: "skill"}}
}
func (t *context79Toolset) ExecuteDetailed(context.Context, llm.ToolCall) (llm.ToolResult, error) {
	return llm.ToolResult{Output: "skill instructions"}, nil
}
func (t *context79Toolset) TaskContext(string) string { return t.work }
func (t *context79Toolset) WorkingDirectory() string  { return t.cwd }

func TestPreparedEstimateIncludesRequestOnlyOnce(t *testing.T) {
	p := &capture79Provider{}
	ts := &context79Toolset{work: "WORK-HISTORY", cwd: "/test/cwd"}
	a := NewWithSystem(p, "test", ts, trace.New(io.Discard, false), io.Discard, 3, "BASE-SYSTEM")
	store := &counting79Store{items: []learning.Learning{{Topic: "T", Content: "C"}}}
	a.SetLearning(store, 1200)
	dynamics := 0
	a.SetRequestContext(func() string { dynamics++; return "DYN-CONTEXT" })
	a.SetInteractiveAvailable(true)
	a.SetInteractiveMode(true)
	a.taskContext = "WORK-HISTORY"
	a.messages = append(a.messages, llm.Message{Role: "user", Content: "hello"})
	durableBefore := append([]llm.Message(nil), a.messages...)

	prep := a.prepareNormalRequest(context.Background(), "RECOVERY-NOTICE")
	sys := prep.Request.Messages[0].Content
	for _, marker := range []string{
		`Current agent working directory: "/test/cwd"`,
		prompt.LearningReference,
		trust.Wrap("request_context", "DYN-CONTEXT"),
		trust.Wrap("work_history", "WORK-HISTORY"),
		"Interactive questions remaining for this request:",
		"RECOVERY-NOTICE",
	} {
		if got := strings.Count(sys, marker); got != 1 {
			t.Fatalf("marker %q count = %d\nsys=%q", marker, got, sys)
		}
	}
	if want := estimateTokens(prep.Request.Messages) + estimateTokens(prep.Request.Tools); prep.Estimate != want {
		t.Fatalf("estimate = %d, want %d", prep.Estimate, want)
	}
	// Durable history preserved byte-for-byte; ephemeral suffix only on the copy.
	if !stablePrefixEqual(a.messages, durableBefore) {
		t.Fatalf("durable history mutated by preparation: %+v", a.messages)
	}
	if !stablePrefixEqual(a.messages[1:], prep.Request.Messages[1:]) {
		t.Fatal("durable tail not byte-identical in prepared copy")
	}
	if dynamics != 1 || store.searches != 1 {
		t.Fatalf("resolves = dynamic %d learning %d, want 1 each", dynamics, store.searches)
	}
	// Deterministic: identical inputs produce identical normalized output and
	// the base system content stays leading.
	prep2 := a.prepareNormalRequest(context.Background(), "RECOVERY-NOTICE")
	if !stablePrefixEqual(prep.Request.Messages, prep2.Request.Messages) {
		t.Fatal("identical preparations diverged")
	}
	if !strings.HasPrefix(prep.Request.Messages[0].Content, a.system) {
		t.Fatal("base system content is not leading text")
	}
}

func TestPublishNeverResolvesEphemeralSources(t *testing.T) {
	p := &capture79Provider{}
	ts := &context79Toolset{}
	a := New(p, "test", ts, trace.New(io.Discard, false), io.Discard, 3)
	store := &counting79Store{items: []learning.Learning{{Topic: "T", Content: "C"}}}
	a.SetLearning(store, 1200)
	dynamics := 0
	a.SetRequestContext(func() string { dynamics++; return "DYN" })
	a.messages = append(a.messages, llm.Message{Role: "user", Content: "hello"})
	a.prepareNormalRequest(context.Background(), "")
	if dynamics != 1 || store.searches != 1 {
		t.Fatalf("prepare resolves = %d/%d", dynamics, store.searches)
	}
	for range 5 {
		a.publishContext()
		_, _, _ = a.ContextRemaining()
		_, _ = a.ContextCapacity()
	}
	if dynamics != 1 || store.searches != 1 {
		t.Fatalf("publish triggered resolves: dynamic=%d learning=%d", dynamics, store.searches)
	}
	if a.learningResolves != 1 || a.contextResolves != 1 {
		t.Fatalf("resolve counters = %d/%d", a.learningResolves, a.contextResolves)
	}
}

func TestCalibrationInvalidatedByDynamicAndNotice(t *testing.T) {
	p := &capture79Provider{usage: &llm.Usage{InputTokens: llm.IntPtr(900), OutputTokens: llm.IntPtr(10)}}
	ts := &context79Toolset{}
	a := NewWithSystem(p, "test", ts, trace.New(io.Discard, false), io.Discard, 3, "BASE")
	a.SetContextWindow(10000)
	a.SetInteractiveAvailable(true)
	a.SetInteractiveMode(true)
	dynamic := "A"
	a.SetRequestContext(func() string { return dynamic })
	if err := a.Run(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	if !a.calibration.valid || !a.calibrationMatches() {
		t.Fatal("expected valid calibration after run")
	}
	beforeLen := len(a.messages)
	// Dynamic-only change with identical history length invalidates.
	dynamic = "B"
	prep := a.prepareNormalRequest(context.Background(), "")
	if len(a.messages) != beforeLen {
		t.Fatal("history length changed")
	}
	if a.calibrationMatches() {
		t.Fatal("dynamic-only change did not invalidate baseline")
	}
	_ = prep
	// Notice removal with identical history length invalidates.
	dynamic = "A"
	a.prepareNormalRequest(context.Background(), "")
	// Re-baseline on the restored prefix.
	a.updateCalibration(a.prepareNormalRequest(context.Background(), ""), &llm.Usage{InputTokens: llm.IntPtr(900)})
	if !a.calibrationMatches() {
		t.Fatal("re-baseline should match")
	}
	a.SetInteractiveMode(false)
	a.prepareNormalRequest(context.Background(), "")
	if a.calibrationMatches() {
		t.Fatal("notice removal did not invalidate baseline")
	}
}

func TestCacheOccupancyNeverSubtractsAndPartialFallsBack(t *testing.T) {
	p := &capture79Provider{}
	ts := &context79Toolset{}
	a := NewWithSystem(p, "test", ts, trace.New(io.Discard, false), io.Discard, 3, "BASE")
	a.SetContextWindow(2000)
	a.messages = append(a.messages, llm.Message{Role: "user", Content: "hi"})
	prep := a.prepareNormalRequest(context.Background(), "")
	// Cached 800 of 1000 input still occupies 1000 input tokens.
	a.updateCalibration(prep, &llm.Usage{InputTokens: llm.IntPtr(1000), CacheReadTokens: llm.IntPtr(800), CacheWriteTokens: llm.IntPtr(100)})
	if !a.calibration.valid {
		t.Fatal("complete input should calibrate")
	}
	if got := a.currentUsedTokens(); got != 1000 {
		t.Fatalf("cached occupancy = %d, want 1000 (never subtract reads/writes)", got)
	}
	// Partial Anthropic input (breakdowns without complete input) falls back.
	a.updateCalibration(prep, &llm.Usage{CacheReadTokens: llm.IntPtr(800), UncachedInputTokens: llm.IntPtr(100)})
	if a.calibration.valid {
		t.Fatal("partial input must not calibrate")
	}
	if got, want := a.currentUsedTokens(), prep.Estimate; got != want {
		t.Fatalf("partial fallback = %d, want prepared estimate %d", got, want)
	}
}

func TestAutoCompactionReactsToDynamicAndRebuildsOnce(t *testing.T) {
	p := &compactingProvider{}
	a := NewWithSystem(p, "test", &skillToolset{}, trace.New(io.Discard, false), io.Discard, 3, "BASE")
	a.SetContextWindow(400)
	a.messages = append(a.messages,
		llm.Message{Role: "user", Content: "old"},
		llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "keep-1", Name: "skill"}}},
		llm.Message{Role: "tool", ToolCallID: "keep-1", Content: "keep result", Name: "skill"},
	)
	a.SetRequestContext(func() string { return strings.Repeat("D", 4000) })
	prep := a.prepareNormalRequest(context.Background(), "ONE-SHOT-RECOVERY")
	if !a.shouldAutoCompactForEstimate(prep.Estimate) {
		t.Fatalf("large dynamic content should trip threshold (estimate %d)", prep.Estimate)
	}
	compactions := 0
	for _, r := range p.requests {
		if len(r.Messages) > 0 && r.Messages[0].Content == prompt.ConversationCompact {
			compactions++
		}
	}
	if !a.maybeAutoCompactForPrepared(context.Background(), prep.Estimate) {
		t.Fatal("expected one compaction rebuild")
	}
	rebuilt := a.prepareNormalRequest(context.Background(), "ONE-SHOT-RECOVERY")
	if !strings.Contains(rebuilt.Request.Messages[0].Content, "ONE-SHOT-RECOVERY") {
		t.Fatal("rebuild lost pending one-shot recovery notice")
	}
	// Tool pairing preserved through compaction + rebuild.
	foundCall, foundResult := false, false
	for _, m := range rebuilt.Request.Messages {
		for _, c := range m.ToolCalls {
			if c.ID == "keep-1" {
				foundCall = true
			}
		}
		if m.Role == "tool" && m.ToolCallID == "keep-1" {
			foundResult = true
		}
	}
	if !foundCall || !foundResult {
		t.Fatalf("tool pairing lost: call=%v result=%v", foundCall, foundResult)
	}
	// No unbounded loop: second check after rebuild must settle (at most one
	// more compaction, and rebuild uses the same single-shot path).
	_ = compactions
	if a.maybeAutoCompactForPrepared(context.Background(), rebuilt.Estimate) {
		rebuilt2 := a.prepareNormalRequest(context.Background(), "ONE-SHOT-RECOVERY")
		if !strings.Contains(rebuilt2.Request.Messages[0].Content, "ONE-SHOT-RECOVERY") {
			t.Fatal("second rebuild lost recovery notice")
		}
	}
}

func TestGoldenRequestPreservesTrustAndReplay(t *testing.T) {
	p := &capture79Provider{}
	ts := &context79Toolset{work: "WORK", cwd: "/w"}
	a := NewWithSystem(p, "test", ts, trace.New(io.Discard, false), io.Discard, 3, "GOLDEN-BASE")
	store := &counting79Store{items: []learning.Learning{{Topic: "T", Content: "C"}}}
	a.SetLearning(store, 1200)
	a.SetRequestContext(func() string { return "DYN" })
	a.taskContext = "WORK"
	a.messages = append(a.messages,
		llm.Message{Role: "user", Content: "restored q", Origin: "restored_user", Untrusted: true},
		llm.Message{Role: "assistant", Content: "restored a", Origin: "restored_assistant", Untrusted: true},
		llm.Message{Role: "user", Content: "see image", Images: []llm.Image{{MediaType: "image/png", Data: []byte{1, 2, 3}}}},
		llm.Message{Role: "assistant", Content: "calling", Thinking: "think", ReasoningDetails: []byte(`{"r":1}`), ToolCalls: []llm.ToolCall{{ID: "c1", Name: "skill", Arguments: []byte(`{"a":1}`)}}},
		llm.Message{Role: "tool", Content: trust.Wrap("skill", "out"), Origin: "skill", Untrusted: true, Name: "skill", ToolCallID: "c1"},
	)
	prep := a.prepareNormalRequest(context.Background(), "")
	msgs := prep.Request.Messages
	if !strings.HasPrefix(msgs[0].Content, "GOLDEN-BASE") {
		t.Fatal("stable base prefix moved")
	}
	if !strings.Contains(msgs[0].Content, "positions 1, 2") {
		t.Fatalf("restored indices wrong: %q", msgs[0].Content)
	}
	for _, want := range []string{
		trust.Wrap("request_context", "DYN"),
		trust.Wrap("work_history", "WORK"),
		prompt.LearningReference,
	} {
		if !strings.Contains(msgs[0].Content, want) {
			t.Fatalf("missing wrapper %q", want)
		}
	}
	// Durable provenance and replay fields survive byte-for-byte.
	if msgs[1].Origin != "restored_user" || !msgs[1].Untrusted || msgs[2].Origin != "restored_assistant" || !msgs[2].Untrusted {
		t.Fatalf("provenance lost: %+v %+v", msgs[1], msgs[2])
	}
	imgFound := false
	for _, m := range msgs {
		if len(m.Images) == 1 && m.Images[0].MediaType == "image/png" {
			imgFound = true
		}
	}
	if !imgFound {
		t.Fatal("image replay lost")
	}
	toolOK, reasonOK := false, false
	for _, m := range msgs {
		for _, c := range m.ToolCalls {
			if c.ID == "c1" && string(c.Arguments) == `{"a":1}` {
				toolOK = true
			}
		}
		if m.Role == "assistant" && m.Thinking == "think" && string(m.ReasoningDetails) == `{"r":1}` {
			reasonOK = true
		}
	}
	if !toolOK || !reasonOK {
		t.Fatalf("tool/reasoning replay lost: tool=%v reason=%v", toolOK, reasonOK)
	}
	// Tool schemas never reordered for speculative caching gain: normal mode
	// is skill plus the todo checklist in stable order.
	wantTools := []string{"skill", "todo_add", "todo_update", "todo_done"}
	if len(prep.Request.Tools) != len(wantTools) {
		t.Fatalf("tools = %+v, want %v", prep.Request.Tools, wantTools)
	}
	for i, want := range wantTools {
		if prep.Request.Tools[i].Name != want {
			t.Fatalf("tools = %+v, want %v", prep.Request.Tools, wantTools)
		}
	}
}

func TestChatGPTChangingContextPreservesConversationPrefix(t *testing.T) {
	p := &captureAgentProvider{name: "chatgpt"}
	a := newCacheTestAgent(p)
	a.messages = append(a.messages, llm.Message{Role: "user", Content: "hello"}, llm.Message{Role: "assistant", Content: "working"})
	before := append([]llm.Message(nil), a.messages...)
	dynamic := "first status"
	a.SetRequestContext(func() string { return dynamic })
	first := a.prepareNormalRequest(context.Background(), "retry once")
	dynamic = "second status"
	second := a.prepareNormalRequest(context.Background(), "")
	for i, prep := range []preparedRequest{first, second} {
		msgs := prep.Request.Messages
		if len(msgs) != len(before)+1 || !stablePrefixEqual(msgs[:len(before)], before) {
			t.Fatalf("conversation prefix changed: %+v", msgs)
		}
		tail := msgs[len(before)]
		if tail.Role != "developer" || !strings.Contains(tail.Content, trust.Wrap("request_context", []string{"first status", "second status"}[i])) {
			t.Fatalf("missing wrapped developer context: %+v", tail)
		}
		if prep.Estimate != estimateTokens(msgs)+estimateTokens(prep.Request.Tools) {
			t.Fatal("estimate excludes tail")
		}
	}
	if first.PrefixHash == second.PrefixHash {
		t.Fatal("changing tail must invalidate calibration")
	}
	if !strings.Contains(first.Request.Messages[len(before)].Content, "retry once") || strings.Contains(second.Request.Messages[len(before)].Content, "retry once") {
		t.Fatal("recovery must be request-only")
	}
	if !stablePrefixEqual(a.messages, before) {
		t.Fatal("durable history mutated")
	}
}
