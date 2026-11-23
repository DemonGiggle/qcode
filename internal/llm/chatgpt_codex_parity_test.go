package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"qcode/internal/chatgptoauth"
)

// Static Codex parity evidence for ChatGPT gpt-6.x (SIWC).
//
// Codex source snapshot: openai/codex @ 583ec34 (Nov 2026).
// Relevant symbols: core/src/client.rs build_responses_request,
// core/src/thread_manager.rs PromptCacheKey, codex-api/src/common.rs
// ResponsesApiRequest, codex-api/src/sse/responses.rs ResponseCompletedUsage,
// protocol/src/protocol.rs TokenUsage. No live calls; all fixtures below are
// synthetic and redacted.

func TestChatGPTCodexParityBaselineWire(t *testing.T) {
	var body map[string]any
	p, err := newChatGPT(Config{BaseURL: "", HTTP: doerFunc(func(r *http.Request) (*http.Response, error) {
		body = nil
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(responsesStream("ok")))}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.(*chatGPTProvider).doCodex(context.Background(), Request{
		Model:          "gpt-6.1-sol",
		PromptCacheKey: "session-key",
		Messages:       []Message{{Role: "system", Content: "durable"}, {Role: "user", Content: "question"}},
	}, chatgptoauth.Tokens{AccessToken: "test"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// SIWC contract preserved.
	if body["store"] != false || body["stream"] != true {
		t.Fatalf("SIWC contract changed: %v", body)
	}
	if _, ok := body["instructions"]; ok {
		t.Fatalf("must use leading developer message, not top-level instructions: %v", body)
	}
	if _, ok := body["prompt_cache_key"]; !ok {
		t.Fatalf("session-stable prompt_cache_key missing: %v", body)
	}
	// Codex parity: implicit caching only — no options, no explicit markers.
	if _, ok := body["prompt_cache_options"]; ok {
		t.Fatalf("prompt_cache_options must not be sent (Codex parity): %v", body)
	}
	encoded, _ := json.Marshal(body["input"])
	if strings.Contains(string(encoded), "prompt_cache_breakpoint") {
		t.Fatalf("explicit breakpoints must not be sent (Codex parity): %s", encoded)
	}
	if !strings.Contains(string(encoded), "durable") {
		t.Fatalf("leading developer instructions missing: %s", encoded)
	}
}

func TestChatGPTRequestOnlyInstructionsFollowReplayableInput(t *testing.T) {
	for _, tc := range []struct {
		model, baseURL string
		topLevel       bool
	}{
		{model: "gpt-6.2-sol", topLevel: true},
		{model: "gpt-6.2-astra", topLevel: true},
		{model: "gpt-6.10-test", topLevel: true},
		{model: "gpt-7", topLevel: true},
		{model: "gpt-6.1-sol"},
		{model: "gpt-5.6"},
		{model: "unknown"},
		{model: "gpt-6.2-sol", baseURL: "https://proxy.example/v1"},
	} {
		t.Run(tc.model+"/"+tc.baseURL, func(t *testing.T) {
			provider := &chatGPTProvider{baseURL: tc.baseURL}
			rest := []Message{{Role: "user", Content: "question"}, {Role: "tool", ToolCallID: "call_1", Content: "result"}}
			baseline := provider.buildCodexBody(Request{Model: tc.model}, "durable", rest, true)
			baseItems := baseline["input"].([]any)
			for _, guidance := range []string{"retry only the failed action", "another notice", ""} {
				body := provider.buildCodexBody(Request{Model: tc.model, RequestOnlyInstructions: guidance}, "durable", rest, true)
				items := body["input"].([]any)
				wantItems := baseItems
				var wantInstructions any
				if guidance != "" {
					if tc.topLevel {
						wantInstructions = guidance
					} else {
						wantItems = append(append([]any(nil), baseItems...), responsesMessage("developer", guidance, nil))
					}
				}
				if body["instructions"] != wantInstructions || !reflect.DeepEqual(items, wantItems) {
					t.Fatalf("guidance %q produced input %v and instructions %v", guidance, items, body["instructions"])
				}
			}
		})
	}
}

func TestChatGPTCodexParityNullDetailsStayUnknown(t *testing.T) {
	// Codex test fixture shape (core/tests/common/responses.rs ev_completed):
	// input_tokens_details null means Codex TokenUsage defaults cached to 0
	// (i64). qcode presence-aware contract keeps it unknown (nil), so /usage
	// correctly shows unavailable instead of 0.
	stream := strings.Join([]string{
		`data: {"type":"response.completed","response":{"output":[],"usage":{"input_tokens":15000,"input_tokens_details":null,"output_tokens":320,"output_tokens_details":null,"total_tokens":15320}}}`,
	}, "\n") + "\n\n"
	resp, err := parseResponsesStream(strings.NewReader(stream), nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Usage == nil {
		t.Fatal("usage is nil")
	}
	if in, ok := resp.Usage.KnownInput(); !ok || in != 15000 {
		t.Fatalf("input = %d,%v want 15000,true", in, ok)
	}
	if _, ok := resp.Usage.KnownCacheRead(); ok {
		t.Fatal("null input_tokens_details must stay unknown, not explicit 0")
	}
	if _, ok := resp.Usage.KnownCacheWrite(); ok {
		t.Fatal("null details must not infer a write")
	}
}

func TestChatGPTCodexParityTerraSendsKeyOnly(t *testing.T) {
	// Codex parity sends key only (no options/breakpoints), so Terra-class
	// models no longer 400 on prompt_cache_options. If the key itself is
	// rejected, the fallback strips the key but keeps the developer-message
	// shape so the reusable prefix stays byte-identical.
	calls := 0
	var bodies []map[string]any
	p, err := newChatGPT(Config{ChatGPTOAuthPath: writeChatGPTOAuth(t, chatgptoauth.Tokens{AccessToken: "test", RefreshToken: "refresh"}), HTTP: doerFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		bodies = append(bodies, body)
		if calls == 1 {
			return &http.Response{StatusCode: 400, Status: "400 Bad Request", Body: io.NopCloser(strings.NewReader(`{"error":{"message":"prompt_cache_key is not supported on this model","type":"invalid_request_error","param":"prompt_cache_key","code":"invalid_parameter"}}`))}, nil
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(responsesStream("ok")))}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Complete(context.Background(), Request{Model: "gpt-6.1-terra", PromptCacheKey: "k", Messages: []Message{{Role: "user", Content: "hi"}}}, nil); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("requests = %d, want 2 (key attempt + keyless fallback)", calls)
	}
	first, _ := json.Marshal(bodies[0])
	if !strings.Contains(string(first), "prompt_cache_key") {
		t.Fatalf("first Terra attempt must carry the stable key: %s", first)
	}
	if strings.Contains(string(first), "prompt_cache_options") || strings.Contains(string(first), "prompt_cache_breakpoint") {
		t.Fatalf("Codex parity sends key only, no explicit cache fields: %s", first)
	}
	second, _ := json.Marshal(bodies[1])
	if strings.Contains(string(second), "prompt_cache") {
		t.Fatalf("fallback must strip all cache fields: %s", second)
	}
}

func TestChatGPTCodexParityReasoningOmittedByDefault(t *testing.T) {
	// qcode omits top-level reasoning when no thinking level is set (server
	// default preserved). Codex always sends an explicit effort (model default
	// resolved). Both are stable within a session, but a mid-session thinking
	// change rewrites hidden instructions on either client.
	var body map[string]any
	p, err := newChatGPT(Config{BaseURL: "", HTTP: doerFunc(func(r *http.Request) (*http.Response, error) {
		body = nil
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(responsesStream("ok")))}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.(*chatGPTProvider).doCodex(context.Background(), Request{
		Model:    "gpt-6.1-sol",
		Messages: []Message{{Role: "user", Content: "hi"}},
	}, chatgptoauth.Tokens{AccessToken: "test"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := body["reasoning"]; ok {
		t.Fatalf("empty thinking must omit reasoning (preserve server default): %v", body)
	}
	inc, ok := body["include"].([]any)
	if !ok || len(inc) != 1 || inc[0] != "reasoning.encrypted_content" {
		t.Fatalf("reasoning-capable model must request reasoning.encrypted_content in include: %v", body["include"])
	}
}
