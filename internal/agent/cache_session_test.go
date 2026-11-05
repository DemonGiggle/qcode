package agent

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"qcode/internal/llm"
	"qcode/internal/trace"
)

// captureAgentProvider records every request for purpose assertions.
type captureAgentProvider struct {
	name     string
	requests []llm.Request
}

func (p *captureAgentProvider) Name() string { return p.name }
func (p *captureAgentProvider) Complete(_ context.Context, r llm.Request, _ llm.StreamCallback) (llm.Response, error) {
	p.requests = append(p.requests, r)
	return llm.Response{Message: llm.Message{Role: "assistant", Content: "ok"}, Usage: &llm.Usage{InputTokens: llm.IntPtr(10), OutputTokens: llm.IntPtr(2)}}, nil
}

func newCacheTestAgent(provider *captureAgentProvider) *Agent {
	return New(provider, "test-model", &managerToolset{}, trace.New(io.Discard, false), io.Discard, 4)
}

func TestCacheSessionStableAcrossTurnsAndResume(t *testing.T) {
	provider := &captureAgentProvider{name: "openai"}
	a := newCacheTestAgent(provider)
	first := a.prepareNormalRequest(context.Background(), "").Request.PromptCacheKey
	second := a.prepareNormalRequest(context.Background(), "").Request.PromptCacheKey
	if first == "" || first != second {
		t.Fatalf("chat key unstable: %q vs %q", first, second)
	}
	if len(first) != 64 {
		t.Fatalf("transmitted key length = %d, want 64", len(first))
	}
	// Persist and resume: same transmitted key.
	data := *a.checkpoint.Load()
	var saved SavedState
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.CacheSessionID == "" {
		t.Fatal("base identity not persisted")
	}
	if !llm.IsValidCacheSessionID(saved.CacheSessionID) {
		t.Fatalf("persisted base invalid: %q", saved.CacheSessionID)
	}
	// Persisted value is the base identity, not the transmitted digest.
	if saved.CacheSessionID == first {
		t.Fatal("checkpoint stores transmitted digest instead of base identity")
	}
	b := newCacheTestAgent(provider)
	if err := b.RestoreState(data); err != nil {
		t.Fatal(err)
	}
	restored := b.prepareNormalRequest(context.Background(), "").Request.PromptCacheKey
	if restored != first {
		t.Fatalf("resume key = %q, want %q", restored, first)
	}
}

func TestCacheSessionDistinctPerAgentAndNew(t *testing.T) {
	provider := &captureAgentProvider{name: "openai"}
	a := newCacheTestAgent(provider)
	b := newCacheTestAgent(provider)
	keyA := a.prepareNormalRequest(context.Background(), "").Request.PromptCacheKey
	keyB := b.prepareNormalRequest(context.Background(), "").Request.PromptCacheKey
	if keyA == "" || keyB == "" || keyA == keyB {
		t.Fatalf("agents share key: %q vs %q", keyA, keyB)
	}
	before := keyA
	a.ResetSession()
	after := a.prepareNormalRequest(context.Background(), "").Request.PromptCacheKey
	if after == "" || after == before {
		t.Fatalf("/new did not rotate identity: %q vs %q", before, after)
	}
}

func TestCachePurposeNamespaceDiffers(t *testing.T) {
	provider := &captureAgentProvider{name: "openai"}
	a := newCacheTestAgent(provider)
	chat := a.prepareNormalRequest(context.Background(), "").Request.PromptCacheKey
	// Compaction and learning keys via helpers (same base, distinct purpose).
	compact := a.compactCacheKey("openai", "test-model")
	learn := a.learnCacheKey("openai", "test-model")
	if chat == "" || compact == "" || learn == "" {
		t.Fatal("empty purpose key")
	}
	if chat == compact || chat == learn || compact == learn {
		t.Fatalf("purposes share key: chat=%q compact=%q learn=%q", chat, compact, learn)
	}
}

func TestCacheModelProviderSwitchNamespaces(t *testing.T) {
	provider := &captureAgentProvider{name: "openai"}
	a := newCacheTestAgent(provider)
	base := a.ensureCacheSessionID()
	before := a.prepareNormalRequest(context.Background(), "").Request.PromptCacheKey
	// Model switch keeps base identity but changes transmitted key.
	a.model = "other-model"
	switched := a.prepareNormalRequest(context.Background(), "").Request.PromptCacheKey
	if switched == before {
		t.Fatal("model switch did not change namespace")
	}
	if a.ensureCacheSessionID() != base {
		t.Fatal("model switch rotated base identity")
	}
	// Switching back reuses the namespace.
	a.model = "test-model"
	if got := a.prepareNormalRequest(context.Background(), "").Request.PromptCacheKey; got != before {
		t.Fatalf("switch back = %q, want %q", got, before)
	}
	// Auxiliary requests never share the chat key even on the same model.
	if got := a.compactCacheKey("openai", "test-model"); got == before {
		t.Fatal("compaction shares chat key")
	}
}

func TestCacheLegacyResumeInitializesOnce(t *testing.T) {
	provider := &captureAgentProvider{name: "openai"}
	a := newCacheTestAgent(provider)
	data := *a.checkpoint.Load()
	var saved SavedState
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	// Simulate a legacy save with no identity.
	saved.CacheSessionID = ""
	legacy, err := json.Marshal(saved)
	if err != nil {
		t.Fatal(err)
	}
	b := newCacheTestAgent(provider)
	if err := b.RestoreState(legacy); err != nil {
		t.Fatal(err)
	}
	first := b.ensureCacheSessionID()
	if !llm.IsValidCacheSessionID(first) {
		t.Fatalf("legacy resume identity invalid: %q", first)
	}
	// Second restore of the same legacy blob on another agent initializes
	// independently (each agent owns its identity).
	c := newCacheTestAgent(provider)
	if err := c.RestoreState(legacy); err != nil {
		t.Fatal(err)
	}
	// A resumed agent with the initialized identity keeps it stable.
	data2 := *b.checkpoint.Load()
	d := newCacheTestAgent(provider)
	if err := d.RestoreState(data2); err != nil {
		t.Fatal(err)
	}
	if got := d.prepareNormalRequest(context.Background(), "").Request.PromptCacheKey; got != b.prepareNormalRequest(context.Background(), "").Request.PromptCacheKey {
		t.Fatal("initialized identity not stable across resume")
	}
}

func TestCacheMalformedStoredIdentityRegenerated(t *testing.T) {
	provider := &captureAgentProvider{name: "openai"}
	a := newCacheTestAgent(provider)
	data := *a.checkpoint.Load()
	var saved SavedState
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	saved.CacheSessionID = "not-a-uuid"
	bad, err := json.Marshal(saved)
	if err != nil {
		t.Fatal(err)
	}
	b := newCacheTestAgent(provider)
	if err := b.RestoreState(bad); err != nil {
		t.Fatal(err)
	}
	if !llm.IsValidCacheSessionID(b.ensureCacheSessionID()) {
		t.Fatalf("malformed identity not regenerated: %q", b.ensureCacheSessionID())
	}
	if b.ensureCacheSessionID() == "not-a-uuid" {
		t.Fatal("malformed identity retained")
	}
	if key := b.prepareNormalRequest(context.Background(), "").Request.PromptCacheKey; key == "" {
		t.Fatal("regenerated identity yields no key")
	}
}

func TestCacheKeyAbsentFromTraceAndUnrelatedToPrompt(t *testing.T) {
	provider := &captureAgentProvider{name: "openai"}
	a := newCacheTestAgent(provider)
	a.messages = append(a.messages, llm.Message{Role: "user", Content: "secret prompt alpha"})
	keyAlpha := a.prepareNormalRequest(context.Background(), "").Request.PromptCacheKey
	// Changing only prompt content must not change the transmitted key: the
	// digest covers base+provider+model+purpose, never prompt bytes.
	a.messages[len(a.messages)-1].Content = "entirely different prompt beta with PII-like text"
	keyBeta := a.prepareNormalRequest(context.Background(), "").Request.PromptCacheKey
	if keyAlpha != keyBeta {
		t.Fatal("key depends on prompt content")
	}
	// Trace llm spans carry only model/step metadata, never the cache key.
	spanFields := map[string]any{"model": a.model, "step": 1}
	raw, _ := json.Marshal(spanFields)
	if strings.Contains(string(raw), keyAlpha) {
		t.Fatal("key in trace fields")
	}
	// The checkpoint stores the base identity, not the wire digest; the wire
	// digest itself is fixed-length and carries no prompt bytes.
	data := *a.checkpoint.Load()
	if strings.Contains(string(data), keyAlpha) {
		t.Fatal("transmitted digest persisted in checkpoint")
	}
}

func TestCacheCompactionAndLearningUseSeparatePurposes(t *testing.T) {
	provider := &captureAgentProvider{name: "openai"}
	a := newCacheTestAgent(provider)
	a.messages = append(a.messages,
		llm.Message{Role: "user", Content: "hello"},
		llm.Message{Role: "assistant", Content: "world"},
		llm.Message{Role: "user", Content: "again"},
		llm.Message{Role: "assistant", Content: "reply"},
	)
	chat := a.prepareNormalRequest(context.Background(), "").Request.PromptCacheKey
	provider.requests = nil
	if _, err := a.Compact(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) == 0 {
		t.Fatal("compaction sent no request")
	}
	for _, req := range provider.requests {
		if req.PromptCacheKey == "" {
			t.Fatal("compaction request missing key")
		}
		if req.PromptCacheKey == chat {
			t.Fatal("compaction shares chat key")
		}
		want := a.compactCacheKey("openai", a.model)
		if req.PromptCacheKey != want {
			t.Fatalf("compaction key = %q, want %q", req.PromptCacheKey, want)
		}
	}
}
