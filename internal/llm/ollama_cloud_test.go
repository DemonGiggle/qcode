package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const cloudFixtureKey = "cloud-fixture-key"

func TestOllamaCloudRejectsTruncatedStream(t *testing.T) {
	client := doerFunc(func(*http.Request) (*http.Response, error) {
		body := `{"message":{"content":"partial"},"done":false}` + "\n"
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	p, err := New(OllamaCloudKind, Config{APIKey: cloudFixtureKey, HTTP: client})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := p.Complete(context.Background(), Request{Model: "cloud-model"}, nil)
	if err == nil || !strings.Contains(err.Error(), "stream ended before completion") || resp.Message.Content != "" {
		t.Fatalf("expected a truncated stream error, got %+v, %v", resp, err)
	}
}

func TestOllamaCloudConnection(t *testing.T) {
	for _, base := range []string{"http://proxy.test", "//proxy.test", "https:proxy.test", "https:///path", "https://user:secret@proxy.test", "https://proxy.test?", "https://proxy.test?x=1", "https://proxy.test#"} {
		if _, err := New(OllamaCloudKind, Config{BaseURL: base, APIKey: cloudFixtureKey}); err == nil || strings.Contains(err.Error(), base) {
			t.Errorf("invalid base %q: %v", base, err)
		}
	}
	for _, base := range []string{"", "https://proxy.test"} {
		if _, err := New(OllamaCloudKind, Config{BaseURL: base, APIKey: " \t"}); err == nil {
			t.Errorf("accepted blank key for %q", base)
		}
	}
	for base, want := range map[string]string{"": "https://ollama.com/api/tags", "https://api": "https://api/api/tags", "https://proxy.test/gateway/api///": "https://proxy.test/gateway/api/tags"} {
		p, err := New(OllamaCloudKind, Config{BaseURL: base, APIKey: cloudFixtureKey, HTTP: doerFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.String() != want || req.Header.Get("Authorization") != "Bearer "+cloudFixtureKey {
				t.Errorf("unexpected route or credential for %q", req.URL)
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"models":[]}`))}, nil
		})})
		if err != nil {
			t.Fatal(err)
		}
		if models, err := p.(ModelLister).Models(context.Background()); err != nil || len(models) != 0 {
			t.Fatalf("empty catalog: %v, %v", models, err)
		}
	}
}

func TestOllamaCloudChatAndModelMetadata(t *testing.T) {
	var showCalls atomic.Int32
	requests := make(chan map[string]any, 5)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Authorization") != "Bearer "+cloudFixtureKey {
			t.Error("missing Bearer key")
		}
		if req.Method == http.MethodPost && req.Header.Get("Content-Type") != "application/json" {
			t.Error("missing JSON content type")
		}
		switch req.Method + " " + req.URL.Path {
		case "GET /gateway/api/tags":
			fmt.Fprint(w, `{"models":[{"name":"Gemma:31b","model":"unused","thinking":{"values":[false,true],"default":false},"details":{"context_length":32768}},`+
				`{"model":"org/Named-Model"},{"name":"broken","details":"unexpected","model_info":false,"thinking":"unknown"},{"name":"Gemma:31b"},{}]}`)
		case "POST /gateway/api/show":
			showCalls.Add(1)
			var body map[string]string
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			switch body["model"] {
			case "org/Named-Model":
				fmt.Fprint(w, `{"thinking":{"values":["Light","Intense"],"default":"Light"},"model_info":{"foo.context_length":65536,"bad.context_length":"bad"}}`)
			case "broken":
				http.NotFound(w, req)
			default:
				t.Errorf("unexpected model lookup: %v", body)
				http.NotFound(w, req)
			}
		case "POST /gateway/api/chat":
			var body map[string]any
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			requests <- body
			fmt.Fprintln(w, `{"message":{"thinking":"Reasoning"},"prompt_eval_count":10,"prompt_eval_cached_count":3}`)
			fmt.Fprintln(w, `{"message":{"content":"OK","tool_calls":[{"function":{"name":"inspect","arguments":{"a":1}}}]},"eval_count":2,"prompt_eval_cached_count":"bad"}`)
			fmt.Fprintln(w, `{"done":true,"eval_count":4,"prompt_eval_cached_count":0}`)
		default:
			t.Errorf("unexpected request: %s %s", req.Method, req.URL.Path)
			http.NotFound(w, req)
		}
	}))
	defer server.Close()
	p, err := New(OllamaCloudKind, Config{InstanceName: "work-cloud", BaseURL: server.URL + "/gateway/api/", HTTP: server.Client(), APIKey: cloudFixtureKey})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	models, err := p.(ModelLister).Models(ctx)
	if err != nil || !reflect.DeepEqual(models, []string{"Gemma:31b", "org/Named-Model", "broken"}) || showCalls.Load() != 0 {
		t.Fatalf("discovery was not catalog-only: models=%v err=%v show=%d", models, err, showCalls.Load())
	}
	if p.Name() != "work-cloud" || Kind(p) != OllamaCloudKind {
		t.Fatal("Cloud identity lost")
	}
	metadata := p.(ModelMetadataProvider)
	for _, model := range models {
		metadata.RefreshModelMetadata(ctx, model)
	}
	tracking := p.(ContextWindowProvider)
	for i, want := range []int{32768, 65536, 0} {
		if got, err := tracking.ContextWindow(ctx, models[i]); err != nil || got != want {
			t.Errorf("context for %q = %d, %v", models[i], got, err)
		}
	}
	if showCalls.Load() != 2 {
		t.Errorf("cached metadata repeated /api/show: %d", showCalls.Load())
	}
	capability := p.(ThinkingProvider).ThinkingCapability("org/Named-Model")
	if !capability.Adjustable || !reflect.DeepEqual(capability.Levels, []string{"light", "intense"}) || capability.Default != "light" {
		t.Fatalf("wrong capability: %+v", capability)
	}
	for _, tc := range []struct {
		model, level string
		think        any
	}{
		{"Gemma:31b", "on", true}, {"Gemma:31b", "off", false}, {"org/Named-Model", "intense", "Intense"}, {"broken", "high", nil}, {"Gemma:31b", "", nil},
	} {
		var events []StreamEvent
		zero := 0.0
		resp, err := p.Complete(ctx, Request{Model: tc.model, Thinking: tc.level, Temperature: &zero, PromptCacheKey: "not-sent",
			Messages: []Message{{Role: "user", Images: []Image{{Data: []byte{1, 2, 3}}}}, {Role: "assistant", ToolCalls: []ToolCall{{Name: "inspect", Arguments: json.RawMessage(`{"a":1}`)}}}, {Role: "tool", Name: "inspect", Content: "found"}},
			Tools:    []Tool{{Name: "inspect"}},
		}, func(event StreamEvent) { events = append(events, event) })
		if err != nil {
			t.Fatal(err)
		}
		body := <-requests
		_, thinkPresent := body["think"]
		if body["model"] != tc.model || body["stream"] != true || body["think"] != tc.think || thinkPresent != (tc.think != nil) || body["prompt_cache_key"] != nil || body["options"].(map[string]any)["temperature"] != float64(0) {
			t.Errorf("bad chat request: %v", body)
		}
		messages := body["messages"].([]any)
		if messages[0].(map[string]any)["images"].([]any)[0] != "AQID" || messages[1].(map[string]any)["tool_calls"] == nil || messages[2].(map[string]any)["tool_name"] != "inspect" || body["tools"].([]any)[0].(map[string]any)["type"] != "function" {
			t.Errorf("bad message/tool payload: %v", body)
		}
		if resp.Message.Content != "OK" || resp.Message.Thinking != "Reasoning" || len(resp.Message.ToolCalls) != 1 || resp.Message.ToolCalls[0].Name != "inspect" || !reflect.DeepEqual(events, []StreamEvent{{Kind: StreamThinking, Text: "Reasoning"}, {Kind: StreamOutput, Text: "OK"}}) {
			t.Fatalf("bad response: %+v, %+v", resp, events)
		}
		if n, ok := resp.Usage.KnownCacheRead(); !ok || n != 0 || resp.Usage.Total() != 14 {
			t.Fatalf("bad reported usage: %+v", resp.Usage)
		}
	}
}

func TestOllamaCloudHTTPAndStreamErrors(t *testing.T) {
	var redirected atomic.Bool
	other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Store(true) }))
	defer other.Close()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			http.Redirect(w, r, other.URL+"/catalog", http.StatusTemporaryRedirect)
		case "/api/chat":
			var body struct {
				Model string `json:"model"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			switch body.Model {
			case "bad-key":
				http.Error(w, cloudFixtureKey, http.StatusUnauthorized)
			case "forbidden":
				http.Error(w, cloudFixtureKey, http.StatusForbidden)
			case "bad-stream":
				fmt.Fprintln(w, `{"error":"stream failed for `+cloudFixtureKey+`"}`)
			case "redirect":
				http.Redirect(w, r, "/another", http.StatusTemporaryRedirect)
			case "cancel":
				<-r.Context().Done()
			}
		default:
			redirected.Store(true)
		}
	}))
	defer server.Close()
	p, err := New(OllamaCloudKind, Config{BaseURL: server.URL, APIKey: cloudFixtureKey, HTTP: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.(ModelLister).Models(context.Background()); err == nil || !strings.Contains(err.Error(), "redirects") {
		t.Fatalf("catalog redirect error: %v", err)
	}
	for model, hint := range map[string]string{"bad-key": "OLLAMA_API_KEY", "forbidden": "HTTP 403", "bad-stream": "stream failed", "redirect": "redirects"} {
		if _, err := p.Complete(context.Background(), Request{Model: model}, nil); err == nil || strings.Contains(err.Error(), cloudFixtureKey) || !strings.Contains(err.Error(), hint) {
			t.Errorf("%s error = %v", model, err)
		}
	}
	if redirected.Load() {
		t.Fatal("redirect target received a request")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if _, err := p.Complete(ctx, Request{Model: "cancel"}, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancel error = %v", err)
	}
}

func TestLocalOllamaDoesNotSendCloudCredentials(t *testing.T) {
	p, err := New("ollama", Config{APIKey: cloudFixtureKey, HTTP: doerFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "http://127.0.0.1:11434/api/chat" || r.Header.Get("Authorization") != "" {
			t.Errorf("local Ollama changed: %s", r.URL)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"done":true,"prompt_eval_count":12,"prompt_eval_cached_count":5}`))}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := p.Complete(context.Background(), Request{Model: "test"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := resp.Usage.KnownCacheRead(); ok {
		t.Fatal("Cloud cache accounting was applied to local Ollama")
	}
}

func TestCloudMetadataFailureDoesNotBlockChat(t *testing.T) {
	var paths []string
	p, err := New(OllamaCloudKind, Config{APIKey: cloudFixtureKey, HTTP: doerFunc(func(r *http.Request) (*http.Response, error) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/api/tags":
			return &http.Response{StatusCode: 404, Body: http.NoBody}, nil
		case "/api/show":
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`not JSON`))}, nil
		case "/api/chat":
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"message":{"content":"OK"},"done":true}`))}, nil
		default:
			t.Fatalf("unexpected request: %s", r.URL)
			return nil, nil
		}
	})})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	p.(ModelMetadataProvider).RefreshModelMetadata(ctx, "chat-only-model")
	if n, err := p.(ContextWindowProvider).ContextWindow(ctx, "chat-only-model"); n != 0 || err != nil {
		t.Fatalf("context = %d, %v, want unknown", n, err)
	}
	if r, err := p.Complete(ctx, Request{Model: "chat-only-model"}, nil); r.Message.Content != "OK" || err != nil {
		t.Fatalf("chat with no metadata: %+v, %v", r, err)
	}
	if !reflect.DeepEqual(paths, []string{"/api/tags", "/api/show", "/api/chat"}) {
		t.Fatalf("unexpected metadata retries: %v", paths)
	}
}
