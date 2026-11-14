package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestGeminiProviderDefaultsModelsAndThinking(t *testing.T) {
	var seenURL, auth string
	p, err := newGemini(Config{APIKey: "key", HTTP: doerFunc(func(r *http.Request) (*http.Response, error) {
		seenURL, auth = r.URL.String(), r.Header.Get("Authorization")
		return response(http.StatusOK, `{"data":[{"id":"gemini-3-flash-preview"},{"id":"gemini-embedding-001"},{"id":"gemini-embedding-2"},{"id":"embedding-001"},{"id":"text-embedding-004"},{"id":"models/gemini-embedding-2"},{"id":"gemini-experimental-unknown"}]}`), nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != "gemini" {
		t.Fatalf("name = %q", p.Name())
	}
	models, err := p.(ModelLister).Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(models, []string{"gemini-3-flash-preview", "gemini-experimental-unknown"}) {
		t.Fatalf("Gemini chat picker models = %v", models)
	}
	if seenURL != geminiBaseURL+"/models" || auth != "Bearer key" {
		t.Fatalf("request = %s auth=%q", seenURL, auth)
	}
	thinking := p.(ThinkingProvider)
	if !reflect.DeepEqual(thinking.ThinkingCapability("gemini-4-hydrogen").Levels, []string{"minimal", "low", "medium", "high", "xhigh"}) {
		t.Fatalf("Gemini 4 capability = %+v", thinking.ThinkingCapability("gemini-4-hydrogen"))
	}
	if containsChoice(thinking.ThinkingCapability("gemini-3-pro-preview").Levels, "none") {
		t.Fatal("Gemini 3 must not expose disabled thinking")
	}
	if len(thinking.ThinkingCapability("unrecognized-model").Levels) != 0 {
		t.Fatal("unknown model got fabricated thinking choices")
	}
	if got, err := p.(ContextWindowProvider).ContextWindow(context.Background(), "gemini-3-flash-preview"); err != nil || got != 1048576 {
		t.Fatalf("context window = %d, err = %v", got, err)
	}
}

func TestGeminiMetadataConstrainsThinkingChoices(t *testing.T) {
	meta := EmptyProviderModelMeta("gemini")
	meta.Models["gemini-4-hydrogen"] = ProviderModelOptions{Effort: []string{"low", "high"}}
	meta.Models["gemini-2.5-pro"] = ProviderModelOptions{Effort: []string{"high"}}
	meta.Models["gemini-3-pro-preview"] = ProviderModelOptions{Effort: []string{"xhigh", "high"}}
	meta.Models["unrecognized-model"] = ProviderModelOptions{Effort: []string{"xhigh", "max"}}
	var body map[string]any
	p, err := newGemini(Config{ModelMeta: meta, HTTP: doerFunc(func(r *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		return response(http.StatusOK, "data: [DONE]\n\n"), nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	capability := p.(ThinkingProvider).ThinkingCapability("gemini-4-hydrogen")
	if !reflect.DeepEqual(capability.Levels, []string{"low", "high"}) {
		t.Fatalf("metadata-constrained levels = %v", capability.Levels)
	}
	if _, err := p.Complete(context.Background(), Request{Model: "gemini-4-hydrogen", Thinking: "xhigh"}, nil); err == nil {
		t.Fatal("metadata-excluded effort was accepted")
	}
	gemini3 := p.(ThinkingProvider).ThinkingCapability("gemini-3-pro-preview")
	if !reflect.DeepEqual(gemini3.Levels, []string{"high"}) {
		t.Fatalf("model metadata expanded documented levels: %+v", gemini3)
	}
	if _, err := p.Complete(context.Background(), Request{Model: "gemini-3-pro-preview", Thinking: "xhigh"}, nil); err == nil {
		t.Fatal("undocumented Gemini 3 effort was accepted")
	}
	if got := p.(ThinkingProvider).ThinkingCapability("unrecognized-model"); got.Supported || len(got.Levels) != 0 {
		t.Fatalf("metadata created capabilities for an unknown model: %+v", got)
	}
	one := p.(ThinkingProvider).ThinkingCapability("gemini-2.5-pro")
	if !one.AlwaysOn || one.Adjustable || !reflect.DeepEqual(one.Levels, []string{"high"}) {
		t.Fatalf("single-choice capability = %+v", one)
	}
	if _, err := p.Complete(context.Background(), Request{Model: "gemini-2.5-pro", Thinking: "high"}, nil); err != nil {
		t.Fatal(err)
	}
	if body["reasoning_effort"] != "high" {
		t.Fatalf("single-choice reasoning_effort = %#v; body=%v", body["reasoning_effort"], body)
	}
}

func TestGeminiThinkingFallsBackToDocumentedCatalogWhenMetadataUnavailable(t *testing.T) {
	meta, err := LoadProviderModelMeta(context.Background(), filepath.Join(t.TempDir(), "gemini-meta.json"), "gemini", doerFunc(func(*http.Request) (*http.Response, error) {
		return nil, io.ErrUnexpectedEOF
	}))
	if err == nil {
		t.Fatal("expected metadata fetch error")
	}
	p, err := newGemini(Config{ModelMeta: meta})
	if err != nil {
		t.Fatal(err)
	}
	capability := p.(ThinkingProvider).ThinkingCapability("gemini-3-pro-preview")
	if !reflect.DeepEqual(capability.Levels, []string{"minimal", "low", "medium", "high"}) {
		t.Fatalf("metadata-failure fallback = %+v", capability)
	}
	if unknown := p.(ThinkingProvider).ThinkingCapability("gemini-future-preview"); unknown.Supported || len(unknown.Levels) != 0 {
		t.Fatalf("fallback fabricated unknown model choices: %+v", unknown)
	}
}

func TestGeminiThinkingValidationAndEffortRequest(t *testing.T) {
	calls := 0
	var body map[string]any
	p, err := newGemini(Config{HTTP: doerFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if _, ok := body["stream_options"]; ok {
			t.Error("Gemini request must not send unsupported OpenAI stream_options")
		}
		return response(http.StatusOK, "data: [DONE]\n\n"), nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Complete(context.Background(), Request{Model: "gemini-3-pro-preview", Thinking: "none"}, nil); err == nil {
		t.Fatal("unsupported none level accepted")
	}
	if calls != 0 {
		t.Fatalf("invalid request sent; calls=%d", calls)
	}
	if _, err := p.Complete(context.Background(), Request{Model: "gemini-3-pro-preview"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["reasoning_effort"]; ok {
		t.Fatalf("default request unexpectedly set effort: %v", body)
	}
	if _, err := p.Complete(context.Background(), Request{Model: "gemini-3-pro-preview", Thinking: "high"}, nil); err != nil {
		t.Fatal(err)
	}
	if body["reasoning_effort"] != "high" {
		t.Fatalf("reasoning_effort = %#v", body["reasoning_effort"])
	}
}

func TestGeminiThoughtSignatureStreamAndReplay(t *testing.T) {
	var requests int
	var replay map[string]any
	stream := `data: {"choices":[{"delta":{"content":"checking"}}]}` + "\n" +
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call-1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"one\"}"}},{"index":1,"id":"call-2","type":"function","function":{"name":"inspect","arguments":"{\"q\":\"two\"}"}}]}}]}` + "\n" +
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"extra_content":{"google":{"thought_signature":"sig-one"}}}]}}]}` + "\n" +
		`data: {"choices":[{"delta":{"tool_calls":[{"index":1,"extra_content":{"google":{"thought_signature":"sig-two"}}}]}}]}` + "\n" +
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"extra_content":{"google":{}}}]}}]}` + "\n" +
		"data: [DONE]\n\n"
	p, err := newGemini(Config{HTTP: doerFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		if requests == 1 {
			return response(http.StatusOK, stream), nil
		}
		if err := json.NewDecoder(r.Body).Decode(&replay); err != nil {
			t.Fatal(err)
		}
		return response(http.StatusOK, "data: [DONE]\n\n"), nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.Complete(context.Background(), Request{Model: "gemini-3-pro-preview", Messages: []Message{{Role: "user", Content: "find"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Message.Content != "checking" || len(got.Message.ToolCalls) != 2 {
		t.Fatalf("response = %+v", got.Message)
	}
	wantCalls := []ToolCall{
		{ID: "call-1", Name: "lookup", Arguments: json.RawMessage(`{"q":"one"}`), ThoughtSignature: "sig-one"},
		{ID: "call-2", Name: "inspect", Arguments: json.RawMessage(`{"q":"two"}`), ThoughtSignature: "sig-two"},
	}
	if !reflect.DeepEqual(got.Message.ToolCalls, wantCalls) {
		t.Fatalf("parsed tool calls = %#v, want %#v", got.Message.ToolCalls, wantCalls)
	}
	toolResults := []Message{
		{Role: "tool", ToolCallID: "call-1", Name: "lookup", Content: "result-one"},
		{Role: "tool", ToolCallID: "call-2", Name: "inspect", Content: "result-two"},
	}
	conversation := []Message{{Role: "assistant", ToolCalls: got.Message.ToolCalls}, toolResults[0], toolResults[1]}
	if _, err := p.Complete(context.Background(), Request{Model: "gemini-3-pro-preview", Messages: conversation}, nil); err != nil {
		t.Fatal(err)
	}
	items, ok := replay["messages"].([]any)
	if !ok || len(items) != 3 {
		t.Fatalf("replayed messages = %#v, want assistant plus two tool results", replay["messages"])
	}
	assistant := items[0].(map[string]any)
	calls := assistant["tool_calls"].([]any)
	if len(calls) != 2 {
		t.Fatalf("replayed calls = %#v", calls)
	}
	for index, want := range []string{"sig-one", "sig-two"} {
		wireCall := calls[index].(map[string]any)
		extra := wireCall["extra_content"].(map[string]any)
		google := extra["google"].(map[string]any)
		if google["thought_signature"] != want {
			t.Errorf("call %d signature = %v, want %q", index, google["thought_signature"], want)
		}
		toolCallID := wireCall["id"]
		if toolCallID != wantCalls[index].ID {
			t.Errorf("call %d id = %v", index, toolCallID)
		}
	}
	for i, want := range toolResults {
		wireTool := items[i+1].(map[string]any)
		if wireTool["role"] != "tool" || wireTool["tool_call_id"] != want.ToolCallID || wireTool["name"] != want.Name || wireTool["content"] != want.Content {
			t.Errorf("tool result %d = %#v, want %+v", i, wireTool, want)
		}
	}
	if requests != 2 {
		t.Fatalf("requests = %d", requests)
	}
}

func TestGeminiBaseURLOverride(t *testing.T) {
	var got string
	p, err := newGemini(Config{BaseURL: "https://proxy.example/v1/", HTTP: doerFunc(func(r *http.Request) (*http.Response, error) {
		got = r.URL.String()
		return response(http.StatusOK, "data: [DONE]\n\n"), nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Complete(context.Background(), Request{Model: "unknown"}, nil); err != nil {
		t.Fatal(err)
	}
	if got != "https://proxy.example/v1/chat/completions" {
		t.Fatalf("URL = %q", got)
	}
}

func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Status: http.StatusText(status), Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

var _ = json.RawMessage(nil)
