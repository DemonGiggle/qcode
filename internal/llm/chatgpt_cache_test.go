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
	for _, tc := range []struct {
		model, base   string
		explicit, key bool
	}{
		{"gpt-5.6-codex", "", true, true},
		{"gpt-6.1-sol", "https://api.openai.com/v1/", true, true},
		{"gpt-6-astra", "", true, true},
		{"gpt-5.5", "", false, true},
		{"gpt-5.4-mini", "", false, true},
		{"unknown", "", false, true},
		{"gpt-6.10-test", "", false, true},
		{"gpt-5.60", "", false, true},
		{"gpt-6.1-sol", "https://proxy.example/v1", false, false},
		{"gpt-6.1-sol", "https://chatgpt.com/backend-api/codex", false, false},
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
				if strings.Contains(string(encoded), "prompt_cache_breakpoint") != tc.explicit {
					t.Fatalf("breakpoint policy: %s", encoded)
				}
				if _, ok := body["prompt_cache_options"]; ok != tc.explicit {
					t.Fatalf("options policy: %s", encoded)
				}
				if _, ok := body["instructions"]; ok == tc.explicit {
					t.Fatalf("instructions policy: %s", encoded)
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
	// Stable instructions, image, opaque reasoning/call, and tool result retain
	// identical wire prefixes and breakpoint positions despite a changed tail.
	if !reflect.DeepEqual(first[:5], second[:5]) {
		t.Fatal("continuation changed reusable prefix")
	}
	for _, index := range []int{0, 1, 4} {
		encoded, _ := json.Marshal(first[index])
		if strings.Count(string(encoded), "prompt_cache_breakpoint") != 1 {
			t.Fatalf("missing reusable boundary: %s", encoded)
		}
	}
	imageContent := first[1].(map[string]any)["content"].([]map[string]any)
	if _, ok := imageContent[1]["prompt_cache_breakpoint"]; !ok {
		t.Fatal("breakpoint must follow image, not precede it")
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
			return &http.Response{StatusCode: 400, Status: "400 Bad Request", Body: io.NopCloser(strings.NewReader(`{"error":{"message":"prompt_cache_options is not supported on this model","type":"invalid_request_error","param":"prompt_cache_options","code":"invalid_parameter"}}`))}, nil
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
	if !strings.Contains(string(first), "prompt_cache_options") || !strings.Contains(string(first), "prompt_cache_breakpoint") {
		t.Fatalf("first attempt must carry cache fields: %s", first)
	}
	second, _ := json.Marshal(bodies[1])
	if strings.Contains(string(second), "prompt_cache") {
		t.Fatalf("fallback must strip all cache fields: %s", second)
	}
	if _, ok := bodies[1]["instructions"]; ok == false {
		t.Fatalf("fallback must restore top-level instructions: %s", second)
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
	_, err = p.Complete(context.Background(), Request{Model: "gpt-6.1-sol", Messages: []Message{{Role: "user", Content: "hi"}}}, nil)
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
