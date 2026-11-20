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

func TestChatGPTCachePolicyRouting(t *testing.T) {
	// Codex parity: implicit caching only. The wire carries the stable
	// prompt_cache_key on the official endpoint and never sends explicit
	// breakpoints, prompt_cache_options, or top-level instructions.
	for _, tc := range []struct {
		model, base string
		key         bool
	}{
		{"gpt-5.6-codex", "", true},
		{"gpt-6.1-sol", "https://api.openai.com/v1/", true},
		{"gpt-6-astra", "", true},
		{"gpt-6.2-sol", "", true},
		{"gpt-6.3", "", true},
		{"gpt-6.10-test", "", true},
		{"gpt-7", "", true},
		{"gpt-5.6", "", true},
		{"gpt-5.5", "", true},
		{"gpt-5.4-mini", "", true},
		{"gpt-5", "", true},
		{"gpt-4o", "", true},
		{"unknown", "", true},
		{"gpt-5.60", "", true},
		{"gpt-6.1-sol", "https://proxy.example/v1", false},
		{"gpt-6.1-sol", "https://chatgpt.com/backend-api/codex", false},
	} {
		t.Run(tc.model+tc.base, func(t *testing.T) {
			var body map[string]any
			p, err := newChatGPT(Config{BaseURL: tc.base, HTTP: doerFunc(func(r *http.Request) (*http.Response, error) {
				body = nil
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(responsesStream("ok")))}, nil
			})})
			if err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"session-key", ""} {
				_, err = p.(*chatGPTProvider).doCodex(context.Background(), Request{Model: tc.model, PromptCacheKey: key, Messages: []Message{{Role: "system", Content: "durable"}, {Role: "user", Content: "question"}}}, chatgptoauth.Tokens{AccessToken: "test"}, nil)
				if err != nil {
					t.Fatal(err)
				}
				_, hasKey := body["prompt_cache_key"]
				if hasKey != (tc.key && key != "") {
					t.Fatalf("cache key policy: %v", body)
				}
				encoded, _ := json.Marshal(body)
				if strings.Contains(string(encoded), "prompt_cache_breakpoint") {
					t.Fatalf("explicit breakpoints must not be sent (Codex parity): %s", encoded)
				}
				if _, ok := body["prompt_cache_options"]; ok {
					t.Fatalf("prompt_cache_options must not be sent (Codex parity): %v", body)
				}
				if _, ok := body["instructions"]; ok {
					t.Fatalf("top-level instructions must not be sent (leading developer message): %v", body)
				}
				// Durable instructions travel as the leading developer message.
				items, ok := body["input"].([]any)
				if !ok || len(items) < 2 {
					t.Fatalf("input must start with developer instructions: %v", body)
				}
				initial, ok := items[0].(map[string]any)
				if !ok || initial["role"] != "developer" {
					t.Fatalf("first input item must be developer instructions: %v", body)
				}
				if body["store"] != false || body["stream"] != true {
					t.Fatal("SIWC contract changed")
				}
			}
		})
	}
}

func TestChatGPTCacheBoundariesSurviveContinuation(t *testing.T) {
	replay := json.RawMessage(`[{"type":"reasoning","encrypted_content":"opaque"},{"type":"function_call","call_id":"c1","name":"read","arguments":"{}"}]`)
	history := []Message{
		{Role: "user", Content: "inspect", Images: []Image{{MediaType: "image/png", Data: []byte{1, 2}}}},
		{Role: "assistant", ReasoningDetails: replay},
		{Role: "tool", ToolCallID: "c1", Content: "file contents"},
	}
	before, _ := json.Marshal(history)
	first := chatGPTCacheInput("stable instructions", append(append([]Message(nil), history...), Message{Role: "developer", Content: "cwd /one"}))
	nextHistory := append(append([]Message(nil), history...), Message{Role: "assistant", Content: "done"}, Message{Role: "user", Content: "continue"}, Message{Role: "developer", Content: "cwd /two"})
	second := chatGPTCacheInput("stable instructions", nextHistory)
	// Codex parity: stable instructions, image, opaque reasoning/call, and
	// tool result retain identical wire prefixes despite a changed tail.
	// No explicit breakpoints are emitted; the server selects implicit
	// boundaries.
	if !reflect.DeepEqual(first[:5], second[:5]) {
		t.Fatal("continuation changed reusable prefix")
	}
	for _, index := range []int{0, 1, 4} {
		encoded, _ := json.Marshal(first[index])
		if strings.Contains(string(encoded), "prompt_cache_breakpoint") {
			t.Fatalf("explicit breakpoints must not be sent: %s", encoded)
		}
	}
	imageContent := first[1].(map[string]any)["content"].([]map[string]any)
	if len(imageContent) != 2 || imageContent[1]["type"] != "input_image" {
		t.Fatalf("image content must be preserved without breakpoint markers: %v", imageContent)
	}
	// Leading developer instructions stay stable; the ephemeral tail is a
	// plain developer message appended after history.
	initial, _ := json.Marshal(first[0])
	if !strings.Contains(string(initial), "stable instructions") {
		t.Fatalf("leading developer instructions missing: %s", initial)
	}
	tail, _ := json.Marshal(first[len(first)-1])
	if strings.Contains(string(tail), "prompt_cache_breakpoint") {
		t.Fatal("ephemeral developer tail must not get a breakpoint")
	}
	if string(first[2].(json.RawMessage)) != `{"type":"reasoning","encrypted_content":"opaque"}` {
		t.Fatal("opaque reasoning changed")
	}
	after, _ := json.Marshal(history)
	if string(before) != string(after) {
		t.Fatal("durable history mutated")
	}
	// Shared Responses serialization used by OpenCode receives no annotations.
	shared, _ := json.Marshal(responsesInput(history))
	if strings.Contains(string(shared), "prompt_cache_breakpoint") {
		t.Fatal("cache controls leaked to shared adapter")
	}
}

func TestChatGPTCacheRejectionRetriesWithoutCache(t *testing.T) {
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
	resp, err := p.Complete(context.Background(), Request{Model: "gpt-6.1-terra", PromptCacheKey: "session-key", Messages: []Message{{Role: "system", Content: "durable"}, {Role: "user", Content: "hi"}}}, nil)
	if err != nil || calls != 2 {
		t.Fatalf("err=%v requests=%d", err, calls)
	}
	if resp.Message.Content != "ok" {
		t.Fatalf("text=%q", resp.Message.Content)
	}
	first, _ := json.Marshal(bodies[0])
	if !strings.Contains(string(first), "prompt_cache_key") {
		t.Fatalf("first attempt must carry the stable key: %s", first)
	}
	if strings.Contains(string(first), "prompt_cache_options") || strings.Contains(string(first), "prompt_cache_breakpoint") {
		t.Fatalf("Codex parity sends key only, no explicit cache fields: %s", first)
	}
	second, _ := json.Marshal(bodies[1])
	if strings.Contains(string(second), "prompt_cache") {
		t.Fatalf("fallback must strip all cache fields: %s", second)
	}
	// Codex parity keeps the leading developer message on fallback; only the
	// key is stripped so the reusable prefix stays byte-identical.
	input, _ := json.Marshal(bodies[1]["input"])
	if !strings.Contains(string(input), "durable") {
		t.Fatalf("fallback must keep developer instructions in input: %s", second)
	}
	if _, ok := bodies[1]["instructions"]; ok {
		t.Fatalf("fallback must not restore top-level instructions: %s", second)
	}
}

func TestChatGPTCacheRejectionGivesUpAfterFallback(t *testing.T) {
	calls := 0
	p, err := newChatGPT(Config{ChatGPTOAuthPath: writeChatGPTOAuth(t, chatgptoauth.Tokens{AccessToken: "test", RefreshToken: "refresh"}), HTTP: doerFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 400, Status: "400 Bad Request", Body: io.NopCloser(strings.NewReader(`{"error":"unsupported cache field"}`))}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	// A key must be present to enter the cache-fallback path; without a key
	// there is nothing to strip and no retry occurs (covered separately).
	_, err = p.Complete(context.Background(), Request{Model: "gpt-6.1-sol", PromptCacheKey: "session-key", Messages: []Message{{Role: "user", Content: "hi"}}}, nil)
	if err == nil || calls != 2 {
		t.Fatalf("err=%v requests=%d", err, calls)
	}
}

func TestChatGPTNonCache400DoesNotRetry(t *testing.T) {
	calls := 0
	p, err := newChatGPT(Config{ChatGPTOAuthPath: writeChatGPTOAuth(t, chatgptoauth.Tokens{AccessToken: "test", RefreshToken: "refresh"}), HTTP: doerFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 400, Status: "400 Bad Request", Body: io.NopCloser(strings.NewReader(`{"error":{"message":"invalid model","param":"model","code":"invalid_parameter"}}`))}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Complete(context.Background(), Request{Model: "gpt-6.1-sol", Messages: []Message{{Role: "user", Content: "hi"}}}, nil)
	if err == nil || calls != 1 {
		t.Fatalf("err=%v requests=%d", err, calls)
	}
}
