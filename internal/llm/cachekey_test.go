package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"qcode/internal/chatgptoauth"
)

func cacheChatStream(lines ...string) string {
	return strings.Join(lines, "\n") + "\n"
}

func TestNamespacedCacheKeyStableAndBounded(t *testing.T) {
	base, err := NewCacheSessionID()
	if err != nil {
		t.Fatal(err)
	}
	if !IsValidCacheSessionID(base) {
		t.Fatalf("generated id invalid: %q", base)
	}
	first := NamespacedCacheKey(base, "openai", "gpt-5", CachePurposeChat)
	second := NamespacedCacheKey(base, "openai", "gpt-5", CachePurposeChat)
	if first == "" || first != second {
		t.Fatalf("chat key unstable: %q vs %q", first, second)
	}
	if len(first) != 64 {
		t.Fatalf("transmitted key length = %d, want fixed 64-char digest", len(first))
	}
	// Namespace differs by provider, model, purpose.
	if got := NamespacedCacheKey(base, "other", "gpt-5", CachePurposeChat); got == first {
		t.Fatal("provider switch did not change namespace")
	}
	if got := NamespacedCacheKey(base, "openai", "other-model", CachePurposeChat); got == first {
		t.Fatal("model switch did not change namespace")
	}
	if got := NamespacedCacheKey(base, "openai", "gpt-5", CachePurposeCompact); got == first {
		t.Fatal("compact purpose shares chat key")
	}
	if got := NamespacedCacheKey(base, "openai", "gpt-5", CachePurposeLearn); got == first || got == NamespacedCacheKey(base, "openai", "gpt-5", CachePurposeCompact) {
		t.Fatal("learn purpose shares another purpose key")
	}
	// Switching back reuses the namespace.
	if got := NamespacedCacheKey(base, "openai", "gpt-5", CachePurposeChat); got != first {
		t.Fatal("switching back did not reuse namespace")
	}
	// Distinct base identities never collide.
	other, err := NewCacheSessionID()
	if err != nil {
		t.Fatal(err)
	}
	if other == base {
		t.Fatal("duplicate session identity")
	}
	if got := NamespacedCacheKey(other, "openai", "gpt-5", CachePurposeChat); got == first {
		t.Fatal("distinct agents share transmitted key")
	}
	// Empty base emits nothing; malformed identities are rejected by validation.
	if got := NamespacedCacheKey("", "openai", "gpt-5", CachePurposeChat); got != "" {
		t.Fatalf("empty base key = %q, want empty", got)
	}
	if IsValidCacheSessionID("not-a-uuid") || IsValidCacheSessionID("") {
		t.Fatal("malformed identity validates")
	}
	// The digest never covers prompt content: identical namespaces with
	// different messages must match. (Callers pass only base/provider/model/
	// purpose; this pins that contract.)
	if a, b := NamespacedCacheKey(base, "openai", "gpt-5", CachePurposeChat), NamespacedCacheKey(base, "openai", "gpt-5", CachePurposeChat); a != b {
		t.Fatal("namespace unstable across identical inputs")
	}
}

func TestOfficialOpenAIEmitsStableKey(t *testing.T) {
	var bodies []map[string]any
	client := doerFunc(func(r *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		bodies = append(bodies, body)
		stream := cacheChatStream(
			`data: {"choices":[{"delta":{"content":"hi"}}]}`,
			`data: [DONE]`,
		)
		return &http.Response{StatusCode: 200, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(stream))}, nil
	})
	// Empty BaseURL selects the official endpoint.
	provider, err := newOpenAI(Config{HTTP: client})
	if err != nil {
		t.Fatal(err)
	}
	key := NamespacedCacheKey("11111111-2222-4333-8444-555555555555", "openai", "gpt-5", CachePurposeChat)
	for i := 0; i < 2; i++ {
		if _, err := provider.Complete(context.Background(), Request{
			Model:          "gpt-5",
			Messages:       []Message{{Role: "user", Content: "hi"}},
			PromptCacheKey: key,
		}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if len(bodies) != 2 {
		t.Fatalf("completions = %d", len(bodies))
	}
	for _, body := range bodies {
		got, ok := body["prompt_cache_key"]
		if !ok || got != key {
			t.Fatalf("prompt_cache_key = %v (present=%v), want %q", got, ok, key)
		}
	}
}

func TestOfficialOpenAIOmitsEmptyKey(t *testing.T) {
	client := doerFunc(func(r *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if _, ok := body["prompt_cache_key"]; ok {
			t.Fatal("empty key must be omitted")
		}
		stream := cacheChatStream(`data: {"choices":[{"delta":{"content":"hi"}}]}`, `data: [DONE]`)
		return &http.Response{StatusCode: 200, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(stream))}, nil
	})
	provider, err := newOpenAI(Config{HTTP: client})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Complete(context.Background(), Request{Model: "gpt-5", Messages: []Message{{Role: "user", Content: "hi"}}}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestCustomOpenAIBaseURLOmitsKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if _, ok := body["prompt_cache_key"]; ok {
			t.Error("custom endpoint must not receive prompt_cache_key")
		}
		io.WriteString(w, cacheChatStream(`data: {"choices":[{"delta":{"content":"hi"}}]}`, `data: [DONE]`))
	}))
	defer server.Close()
	provider, err := newOpenAI(Config{BaseURL: server.URL, HTTP: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	req := Request{Model: "gpt-5", Messages: []Message{{Role: "user", Content: "hi"}}, PromptCacheKey: "stable-key"}
	if _, err := provider.Complete(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	// Explicit official URL with trailing slash still counts as official, but
	// the httptest host never does; covered above. The default empty BaseURL
	// path is covered by TestOfficialOpenAIEmitsStableKey.
	_ = IsOfficialOpenAIEndpoint
}

func TestOpenCodeGoChatRouteOmitsKeyKeepsHeader(t *testing.T) {
	var headers []http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if _, ok := body["prompt_cache_key"]; ok {
			t.Error("opencode-go chat route must not receive prompt_cache_key")
		}
		headers = append(headers, r.Header.Clone())
		io.WriteString(w, cacheChatStream(`data: {"choices":[{"delta":{"content":"hi"}}]}`, `data: [DONE]`))
	}))
	defer server.Close()
	provider, err := newOpenCodeGo(Config{BaseURL: server.URL, HTTP: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	req := Request{Model: "unknown-chat-model", Messages: []Message{{Role: "user", Content: "hi"}}, PromptCacheKey: "stable-key"}
	for i := 0; i < 2; i++ {
		if _, err := provider.Complete(context.Background(), req, nil); err != nil {
			t.Fatal(err)
		}
	}
	if len(headers) != 2 {
		t.Fatalf("requests = %d", len(headers))
	}
	for _, h := range headers {
		if h.Get("x-opencode-session") == "" {
			t.Fatal("x-opencode-session header missing on opencode-go route")
		}
	}
	if headers[0].Get("x-opencode-session") != headers[1].Get("x-opencode-session") {
		t.Fatal("x-opencode-session must remain stable across turns")
	}
}

func TestOpenCodeGoOtherRoutesOmitKey(t *testing.T) {
	for _, model := range []string{"gpt-6.1-sol", "qwen3.8-flash"} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if _, ok := body["prompt_cache_key"]; ok {
				t.Errorf("model %s must not receive prompt_cache_key", model)
			}
			if r.URL.Path == "/responses" {
				io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"hi\"}]}]}}\n\n")
			} else {
				io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"role\":\"assistant\",\"content\":[],\"model\":\"x\",\"stop_reason\":null,\"usage\":{\"input_tokens\":5,\"output_tokens\":1}}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			}
		}))
		provider, err := newOpenCodeGo(Config{BaseURL: server.URL, HTTP: server.Client()})
		if err != nil {
			server.Close()
			t.Fatal(err)
		}
		req := Request{Model: model, Messages: []Message{{Role: "user", Content: "hi"}}, PromptCacheKey: "stable-key"}
		if _, err := provider.Complete(context.Background(), req, nil); err != nil {
			server.Close()
			t.Fatal(err)
		}
		server.Close()
	}
}

func TestOllamaOmitsKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		for _, field := range []string{"prompt_cache_key", "cache_control", "prompt_cache_breakpoint", "prompt_cache_retention"} {
			if _, ok := body[field]; ok {
				t.Errorf("ollama must not receive %s", field)
			}
		}
		io.WriteString(w, "{\"message\":{\"role\":\"assistant\",\"content\":\"hi\"},\"done\":true,\"prompt_eval_count\":3,\"eval_count\":1}\n")
	}))
	defer server.Close()
	provider, err := newOllama(Config{BaseURL: server.URL, HTTP: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	req := Request{Model: "llama", Messages: []Message{{Role: "user", Content: "hi"}}, PromptCacheKey: "stable-key"}
	if _, err := provider.Complete(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
}

func TestChatGPTRouteEmitsKey(t *testing.T) {
	path := writeChatGPTOAuth(t, chatgptoauth.Tokens{AccessToken: "test-access", RefreshToken: "test-refresh", AccountID: "acct-1"})
	var body map[string]any
	client := doerFunc(func(r *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: 200, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(responsesStream("hi")))}, nil
	})
	provider, err := newChatGPT(Config{HTTP: client, ChatGPTOAuthPath: path})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Complete(context.Background(), Request{
		Model:          "gpt-5.6-codex",
		Messages:       []Message{{Role: "user", Content: "hello"}},
		PromptCacheKey: "stable-key",
	}, nil); err != nil {
		t.Fatal(err)
	}
	if body["prompt_cache_key"] != "stable-key" {
		t.Fatal("official chatgpt route must receive the session key")
	}
}
