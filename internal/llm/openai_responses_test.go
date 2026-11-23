package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

const responsesCompletedStream = `data: {"type":"response.output_text.delta","delta":"hi"}
data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"hi"}]}],"usage":{"input_tokens":10,"output_tokens":4,"input_tokens_details":{"cached_tokens":6,"cache_write_tokens":2},"output_tokens_details":{"reasoning_tokens":2}}}}

`

func responsesMeta(ids ...string) *ProviderModelMeta {
	models := map[string]ProviderModelOptions{}
	for _, id := range ids {
		models[id] = ProviderModelOptions{Effort: []string{"low", "high"}}
	}
	return &ProviderModelMeta{Version: ProviderModelMetaVersion, Provider: "openai", Models: models}
}

type capturedRequest struct {
	path    string
	auth    string
	body    map[string]any
	rawBody []byte
}

func recordingClient(t *testing.T, responses []*http.Response, captured *[]capturedRequest) HTTPDoer {
	t.Helper()
	calls := 0
	return doerFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		parsed := map[string]any{}
		if err := json.Unmarshal(body, &parsed); err != nil {
			t.Fatalf("request body: %v", err)
		}
		*captured = append(*captured, capturedRequest{path: r.URL.Path, auth: r.Header.Get("Authorization"), body: parsed, rawBody: body})
		idx := calls
		calls++
		if idx >= len(responses) {
			t.Fatalf("unexpected request %d", idx+1)
		}
		return responses[idx], nil
	})
}

func okResponse(body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func errResponse(message string) *http.Response {
	body := fmt.Sprintf(`{"error":{"message":%q}}`, message)
	return &http.Response{StatusCode: http.StatusBadRequest, Status: "400 Bad Request", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func newResponsesProvider(t *testing.T, baseURL string, meta *ProviderModelMeta, client HTTPDoer) *openAIProvider {
	t.Helper()
	provider, err := newOpenAI(Config{BaseURL: baseURL, APIKey: "sk-test", HTTP: client, ModelMeta: meta})
	if err != nil {
		t.Fatal(err)
	}
	p, ok := provider.(*openAIProvider)
	if !ok {
		t.Fatalf("provider type %T", provider)
	}
	return p
}

func TestOpenAIResponsesWireRoundTrip(t *testing.T) {
	var captured []capturedRequest
	client := recordingClient(t, []*http.Response{okResponse(responsesCompletedStream)}, &captured)
	p := newResponsesProvider(t, "", responsesMeta("gpt-6.2-sol"), client)

	response, err := p.Complete(context.Background(), Request{
		Model:          "gpt-6.2-sol",
		Thinking:       "high",
		Messages:       []Message{{Role: "system", Content: "durable rules"}, {Role: "user", Content: "hi"}},
		Tools:          []Tool{{Name: "read", Description: "read a file", Parameters: map[string]any{"type": "object"}}},
		PromptCacheKey: "cache-session",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(captured) != 1 {
		t.Fatalf("requests = %d", len(captured))
	}
	got := captured[0]
	if got.path != "/v1/responses" {
		t.Errorf("path = %q", got.path)
	}
	if got.auth != "Bearer sk-test" {
		t.Errorf("auth = %q", got.auth)
	}
	body := got.body
	if _, ok := body["messages"]; ok {
		t.Errorf("body must not carry messages: %s", got.rawBody)
	}
	if body["stream"] != true || body["store"] != false {
		t.Errorf("stream/store = %v/%v", body["stream"], body["store"])
	}
	// Durable system content leads as a developer message.
	input, _ := body["input"].([]any)
	if len(input) != 2 {
		t.Fatalf("input items = %d (%s)", len(input), got.rawBody)
	}
	first, _ := input[0].(map[string]any)
	if first["role"] != "developer" {
		t.Errorf("first input role = %v", first["role"])
	}
	if body["prompt_cache_key"] != "cache-session" {
		t.Errorf("prompt_cache_key = %v", body["prompt_cache_key"])
	}
	reasoning, _ := body["reasoning"].(map[string]any)
	if reasoning["effort"] != "high" || reasoning["summary"] != "auto" {
		t.Errorf("reasoning = %v", reasoning)
	}
	if _, ok := body["reasoning_effort"]; ok {
		t.Errorf("reasoning_effort must not be sent on the Responses route")
	}
	tools, _ := body["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %s", got.rawBody)
	}
	if tool, _ := tools[0].(map[string]any); tool["type"] != "function" || tool["name"] != "read" {
		t.Errorf("tool = %v", tool)
	}
	if response.Message.Content != "hi" {
		t.Errorf("content = %q", response.Message.Content)
	}
	if response.Usage == nil || response.Usage.CacheReadTokens == nil || *response.Usage.CacheReadTokens != 6 {
		t.Fatalf("usage = %#v", response.Usage)
	}
	if response.Usage.CacheWriteTokens == nil || *response.Usage.CacheWriteTokens != 2 {
		t.Fatalf("cache write usage = %#v", response.Usage)
	}
	if response.Usage.ReasoningTokens == nil || *response.Usage.ReasoningTokens != 2 {
		t.Fatalf("reasoning usage = %#v", response.Usage)
	}
}

func TestOpenAIRoutesToResponsesByDefault(t *testing.T) {
	var captured []capturedRequest
	client := recordingClient(t, []*http.Response{okResponse(responsesCompletedStream)}, &captured)
	p := newResponsesProvider(t, "", nil, client)
	_, err := p.Complete(context.Background(), Request{Model: "gpt-6.2-sol", Messages: []Message{{Role: "user", Content: "hi"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if captured[0].path != "/v1/responses" {
		t.Fatalf("path = %q, want /v1/responses", captured[0].path)
	}
}

func TestOpenAIResponsesCustomBaseURLOmitsCacheKey(t *testing.T) {
	var captured []capturedRequest
	client := recordingClient(t, []*http.Response{okResponse(responsesCompletedStream)}, &captured)
	p := newResponsesProvider(t, "http://gateway.test/v1", nil, client)
	_, err := p.Complete(context.Background(), Request{
		Model:          "gpt-6.2-sol",
		Messages:       []Message{{Role: "user", Content: "hi"}},
		PromptCacheKey: "cache-session",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := captured[0].body["prompt_cache_key"]; ok {
		t.Fatalf("custom base URL must not send prompt_cache_key: %s", captured[0].rawBody)
	}
}

func TestOpenAIResponsesRequestOnlyInstructionsDualTrack(t *testing.T) {
	t.Run("gpt-6.2-sol routes to top-level instructions", func(t *testing.T) {
		var captured []capturedRequest
		client := recordingClient(t, []*http.Response{okResponse(responsesCompletedStream)}, &captured)
		p := newResponsesProvider(t, "", responsesMeta("gpt-6.2-sol"), client)
		_, err := p.Complete(context.Background(), Request{
			Model:                   "gpt-6.2-sol",
			Messages:                []Message{{Role: "system", Content: "durable rules"}, {Role: "user", Content: "hi"}},
			RequestOnlyInstructions: "request only",
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		body := captured[0].body
		if body["instructions"] != "request only" {
			t.Fatalf("instructions = %v", body["instructions"])
		}
		for _, item := range body["input"].([]any) {
			m := item.(map[string]any)
			if m["role"] == "developer" {
				text, _ := json.Marshal(m["content"])
				if strings.Contains(string(text), "request only") {
					t.Fatalf("request-only text leaked into input: %s", text)
				}
			}
		}
	})
	t.Run("older model gets trailing developer message", func(t *testing.T) {
		var captured []capturedRequest
		client := recordingClient(t, []*http.Response{okResponse(responsesCompletedStream)}, &captured)
		p := newResponsesProvider(t, "", nil, client)
		_, err := p.Complete(context.Background(), Request{
			Model:                   "gpt-5.4",
			Messages:                []Message{{Role: "system", Content: "durable rules"}, {Role: "user", Content: "hi"}},
			RequestOnlyInstructions: "request only",
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		body := captured[0].body
		if _, ok := body["instructions"]; ok {
			t.Fatalf("older model must not receive top-level instructions: %s", captured[0].rawBody)
		}
		input := body["input"].([]any)
		last, _ := input[len(input)-1].(map[string]any)
		if last["role"] != "developer" {
			t.Fatalf("last input = %v", last)
		}
		text, _ := json.Marshal(last["content"])
		if !strings.Contains(string(text), "request only") {
			t.Fatalf("trailing developer message missing guidance: %s", text)
		}
	})
}

func TestOpenAIResponsesCacheKeyRejectionRetriesWithoutCacheFields(t *testing.T) {
	var captured []capturedRequest
	client := recordingClient(t, []*http.Response{
		errResponse("prompt_cache_key is not supported on this model"),
		okResponse(responsesCompletedStream),
	}, &captured)
	p := newResponsesProvider(t, "", nil, client)
	response, err := p.Complete(context.Background(), Request{
		Model:          "gpt-6.2-sol",
		Messages:       []Message{{Role: "user", Content: "hi"}},
		PromptCacheKey: "cache-session",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(captured) != 2 {
		t.Fatalf("requests = %d", len(captured))
	}
	if _, ok := captured[0].body["prompt_cache_key"]; !ok {
		t.Fatalf("first request missing prompt_cache_key")
	}
	if _, ok := captured[1].body["prompt_cache_key"]; ok {
		t.Fatalf("retry must drop prompt_cache_key: %s", captured[1].rawBody)
	}
	if response.Usage == nil {
		t.Fatal("expected usage on retry")
	}
}

func TestOpenAIResponsesCacheWording400WithoutCacheFieldsIsNotRetried(t *testing.T) {
	// Custom base URL: no prompt_cache_key is sent and no comparison is set,
	// so a 400 that merely mentions cache fields must not trigger a retry.
	var captured []capturedRequest
	client := recordingClient(t, []*http.Response{errResponse("prompt caching is not supported on this model")}, &captured)
	p := newResponsesProvider(t, "http://gateway.test/v1", nil, client)
	_, err := p.Complete(context.Background(), Request{
		Model:          "gpt-6.2-sol",
		Messages:       []Message{{Role: "user", Content: "hi"}},
		PromptCacheKey: "cache-session", // must be withheld on a custom base URL
	}, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if len(captured) != 1 {
		t.Fatalf("requests = %d, want 1 (no cache fields sent, nothing to retry without)", len(captured))
	}
	if _, ok := captured[0].body["prompt_cache_key"]; ok {
		t.Fatalf("custom base URL must not send prompt_cache_key: %s", captured[0].rawBody)
	}
}

func TestOpenAIResponsesComparisonRejectionKeepsCacheKey(t *testing.T) {
	var captured []capturedRequest
	client := recordingClient(t, []*http.Response{
		errResponse("comparison_response_id is not supported on this model"),
		okResponse(responsesCompletedStream),
	}, &captured)
	p := newResponsesProvider(t, "", nil, client)
	response, err := p.Complete(context.Background(), Request{
		Model:                "gpt-6.2-sol",
		Messages:             []Message{{Role: "user", Content: "hi"}},
		PromptCacheKey:       "cache-session",
		ComparisonResponseID: "resp_prev",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(captured) != 2 {
		t.Fatalf("requests = %d", len(captured))
	}
	if captured[1].body["prompt_cache_key"] != "cache-session" {
		t.Fatalf("comparison-only rejection must keep prompt_cache_key: %s", captured[1].rawBody)
	}
	if _, ok := captured[1].body["prompt_cache_options"]; ok {
		t.Fatalf("retry must drop prompt_cache_options: %s", captured[1].rawBody)
	}
	if response.ComparisonStatus != CacheComparisonRetriedWithoutID {
		t.Fatalf("comparison status = %q", response.ComparisonStatus)
	}
}

func TestOpenAIResponsesOther400IsNotRetried(t *testing.T) {
	var captured []capturedRequest
	client := recordingClient(t, []*http.Response{errResponse("model not found")}, &captured)
	p := newResponsesProvider(t, "", nil, client)
	_, err := p.Complete(context.Background(), Request{
		Model:          "gpt-6.2-sol",
		Messages:       []Message{{Role: "user", Content: "hi"}},
		PromptCacheKey: "cache-session",
	}, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if len(captured) != 1 {
		t.Fatalf("requests = %d, want 1", len(captured))
	}
}

func TestOpenAIResponsesStoreOptIn(t *testing.T) {
	t.Run("store responses", func(t *testing.T) {
		var captured []capturedRequest
		client := recordingClient(t, []*http.Response{okResponse(responsesCompletedStream)}, &captured)
		p := newResponsesProvider(t, "", nil, client)
		_, err := p.Complete(context.Background(), Request{
			Model:          "gpt-6.2-sol",
			Messages:       []Message{{Role: "user", Content: "hi"}},
			StoreResponses: true,
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if captured[0].body["store"] != true {
			t.Fatalf("store = %v", captured[0].body["store"])
		}
	})
	t.Run("previous response id implies store", func(t *testing.T) {
		var captured []capturedRequest
		client := recordingClient(t, []*http.Response{okResponse(responsesCompletedStream)}, &captured)
		p := newResponsesProvider(t, "", nil, client)
		_, err := p.Complete(context.Background(), Request{
			Model:              "gpt-6.2-sol",
			Messages:           []Message{{Role: "user", Content: "hi"}},
			PreviousResponseID: "resp_prev",
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if captured[0].body["store"] != true || captured[0].body["previous_response_id"] != "resp_prev" {
			t.Fatalf("body = %s", captured[0].rawBody)
		}
	})
}

func TestOpenAIResponsesTemperatureOmittedForReasoningModels(t *testing.T) {
	temp := 0.2
	t.Run("reasoning model omits temperature", func(t *testing.T) {
		var captured []capturedRequest
		client := recordingClient(t, []*http.Response{okResponse(responsesCompletedStream)}, &captured)
		p := newResponsesProvider(t, "", responsesMeta("gpt-6.2-sol"), client)
		_, err := p.Complete(context.Background(), Request{
			Model:       "gpt-6.2-sol",
			Thinking:    "low",
			Temperature: &temp,
			Messages:    []Message{{Role: "user", Content: "hi"}},
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := captured[0].body["temperature"]; ok {
			t.Fatalf("reasoning model must not receive temperature: %s", captured[0].rawBody)
		}
	})
	t.Run("non-reasoning model keeps temperature", func(t *testing.T) {
		var captured []capturedRequest
		client := recordingClient(t, []*http.Response{okResponse(responsesCompletedStream)}, &captured)
		p := newResponsesProvider(t, "", nil, client)
		_, err := p.Complete(context.Background(), Request{
			Model:       "gpt-6.2-sol",
			Temperature: &temp,
			Messages:    []Message{{Role: "user", Content: "hi"}},
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if captured[0].body["temperature"] != temp {
			t.Fatalf("temperature = %v", captured[0].body["temperature"])
		}
	})
}

func TestOpenAIResponsesReplaysToolRoundTrip(t *testing.T) {
	var captured []capturedRequest
	client := recordingClient(t, []*http.Response{okResponse(responsesCompletedStream)}, &captured)
	p := newResponsesProvider(t, "", nil, client)
	_, err := p.Complete(context.Background(), Request{
		Model: "gpt-6.2-sol",
		Messages: []Message{
			{Role: "system", Content: "durable rules"},
			{Role: "user", Content: "read the file"},
			{Role: "assistant", ToolCalls: []ToolCall{{ID: "call_1", Name: "read", Arguments: json.RawMessage(`{"path":"x"}`)}}, ReasoningDetails: json.RawMessage(`[{"type":"reasoning","encrypted_content":"abc"},{"type":"function_call","call_id":"call_1","name":"read","arguments":"{\"path\":\"x\"}"}]`)},
			{Role: "tool", ToolCallID: "call_1", Content: "file contents"},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	body := string(captured[0].rawBody)
	for _, want := range []string{`"type":"reasoning"`, `"encrypted_content":"abc"`, `"type":"function_call"`, `"call_id":"call_1"`, `"type":"function_call_output"`, `"output":"file contents"`} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %s: %s", want, body)
		}
	}
}
