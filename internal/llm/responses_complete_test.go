package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"qcode/internal/chatgptoauth"
)

func TestBuildResponsesBodyDefaultsStayStateless(t *testing.T) {
	body := buildResponsesBody("gpt-6.2-sol", ThinkingCapability{}, Request{
		Model:    "gpt-6.2-sol",
		Messages: []Message{{Role: "user", Content: "hi"}},
	}, responsesInput([]Message{{Role: "user", Content: "hi"}}), "", false)
	if _, ok := body["store"]; ok {
		t.Fatalf("default store must be omitted (opencode-go keeps previous wire): %v", body)
	}
	for _, key := range []string{"previous_response_id", "conversation", "prompt_cache_options", "prompt_cache_key", "text", "tool_choice", "parallel_tool_calls", "max_output_tokens", "service_tier", "metadata", "background", "truncation", "include"} {
		if _, ok := body[key]; ok {
			t.Fatalf("%s must be omitted by default: %v", key, body)
		}
	}
}

func TestBuildResponsesBodyComparisonSendsOptions(t *testing.T) {
	body := buildResponsesBody("gpt-6-astra", ThinkingCapability{}, Request{
		Model: "gpt-6-astra", ComparisonResponseID: "resp_123",
	}, []any{map[string]any{"role": "user"}}, "", false)
	opts, ok := body["prompt_cache_options"].(map[string]any)
	if !ok || opts["comparison_response_id"] != "resp_123" {
		t.Fatalf("comparison options missing: %v", body)
	}
}

func TestBuildResponsesBodyContinuationImpliesStore(t *testing.T) {
	body := buildResponsesBody("gpt-6-astra", ThinkingCapability{}, Request{
		Model: "gpt-6-astra", PreviousResponseID: "resp_prev",
	}, []any{map[string]any{"role": "user"}}, "", false)
	if body["store"] != true || body["previous_response_id"] != "resp_prev" {
		t.Fatalf("continuation must imply store:true: %v", body)
	}
	explicit := buildResponsesBody("gpt-6-astra", ThinkingCapability{}, Request{
		Model: "gpt-6-astra", StoreResponses: true,
	}, []any{}, "", false)
	if explicit["store"] != true {
		t.Fatalf("explicit store must be true: %v", explicit)
	}
}

func TestBuildResponsesBodyStructuredOutputAndControls(t *testing.T) {
	strict := true
	parallel := false
	maxTokens := 1024
	body := buildResponsesBody("gpt-6-astra", ThinkingCapability{}, Request{
		Model:             "gpt-6-astra",
		ResponseFormat:    &ResponsesTextFormat{Type: "json_schema", Name: "person", Schema: map[string]any{"type": "object"}, Strict: &strict},
		Verbosity:         "medium",
		ToolChoice:        "required",
		ParallelToolCalls: &parallel,
		MaxOutputTokens:   &maxTokens,
		ServiceTier:       "flex",
		Metadata:          map[string]any{"run": "test"},
		Background:        &parallel,
		Truncation:        "auto",
		Include:           []string{"reasoning.encrypted_content"},
	}, []any{}, "", false)
	text, ok := body["text"].(map[string]any)
	if !ok {
		t.Fatalf("text missing: %v", body)
	}
	format, ok := text["format"].(map[string]any)
	if !ok || format["type"] != "json_schema" || format["name"] != "person" {
		t.Fatalf("text.format wrong: %v", text)
	}
	if body["tool_choice"] != "required" || body["parallel_tool_calls"] != false || body["max_output_tokens"] != 1024 || body["service_tier"] != "flex" {
		t.Fatalf("controls wrong: %v", body)
	}
	if body["truncation"] != "auto" {
		t.Fatalf("truncation wrong: %v", body)
	}
}

func TestBuildResponsesBodyBuiltinAllowlist(t *testing.T) {
	body := buildResponsesBody("gpt-6-astra", ThinkingCapability{}, Request{
		Model: "gpt-6-astra",
		Tools: []Tool{{Name: "read", Parameters: map[string]any{"type": "object"}}},
		BuiltinTools: []map[string]any{
			{"type": "web_search"},
			{"type": "evil_tool"},
			{"type": "mcp", "server_label": "docs"},
		},
	}, []any{}, "", false)
	tools, ok := body["tools"].([]map[string]any)
	if !ok {
		t.Fatalf("tools missing: %v", body)
	}
	if len(tools) != 3 {
		t.Fatalf("builtin allowlist must drop unknown, got %d: %v", len(tools), tools)
	}
}

func TestParseResponsesDiagnosticsHit(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"type":"response.output_text.delta","delta":"ok"}`,
		`data: {"type":"response.completed","response":{"id":"resp_1","output":[],"usage":{"input_tokens":2500,"output_tokens":10,"input_tokens_details":{"cached_tokens":2000}},"prompt_cache_diagnostics":{"type":"cache_hit"}}}`,
	}, "\n") + "\n"
	resp, err := parseResponsesStream(strings.NewReader(stream), nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.ResponseID != "resp_1" {
		t.Fatalf("response id = %q", resp.ResponseID)
	}
	if resp.CacheDiagnostics == nil || resp.CacheDiagnostics.Type != "cache_hit" {
		t.Fatalf("diagnostics = %+v", resp.CacheDiagnostics)
	}
	if got, ok := resp.Usage.KnownCacheRead(); !ok || got != 2000 {
		t.Fatalf("cached = %d,%v", got, ok)
	}
}

func TestParseResponsesDiagnosticsMiss(t *testing.T) {
	stream := `data: {"type":"response.completed","response":{"id":"resp_2","output":[],"prompt_cache_diagnostics":{"type":"cache_miss","reason":"tools_changed","comparison_reusable_tokens":5629,"cache_missed_tokens":5629}}}` + "\n"
	resp, err := parseResponsesStream(strings.NewReader(stream), nil)
	if err != nil {
		t.Fatal(err)
	}
	diag := resp.CacheDiagnostics
	if diag == nil || diag.Type != "cache_miss" || diag.Reason != "tools_changed" {
		t.Fatalf("diagnostics = %+v", diag)
	}
	if diag.ComparisonReusableTokens == nil || *diag.ComparisonReusableTokens != 5629 {
		t.Fatalf("reusable = %+v", diag)
	}
	if diag.CacheMissedTokens == nil || *diag.CacheMissedTokens != 5629 {
		t.Fatalf("missed = %+v", diag)
	}
	if fix := diag.SuggestedFix(); !strings.Contains(fix, "tool") {
		t.Fatalf("fix = %q", fix)
	}
	// Diagnostic counts differ from usage counts: no usage here, diagnostics alone must not fail.
	if resp.Usage != nil {
		t.Fatalf("usage should be nil when unreported, got %+v", resp.Usage)
	}
}

func TestParseResponsesDiagnosticsNotFoundUnavailable(t *testing.T) {
	for _, typ := range []string{"comparison_response_not_found", "unavailable"} {
		stream := `data: {"type":"response.completed","response":{"id":"resp_3","output":[],"prompt_cache_diagnostics":{"type":"` + typ + `"}}}` + "\n"
		resp, err := parseResponsesStream(strings.NewReader(stream), nil)
		if err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
		if resp.CacheDiagnostics == nil || resp.CacheDiagnostics.Type != typ {
			t.Fatalf("%s diagnostics = %+v", typ, resp.CacheDiagnostics)
		}
		if resp.CacheDiagnostics.SuggestedFix() != "" {
			t.Fatalf("%s should have no fix, got %q", typ, resp.CacheDiagnostics.SuggestedFix())
		}
	}
}

func TestParseResponsesRefusalSurfaced(t *testing.T) {
	const delta = `data: {"type":"response.refusal.delta","delta":"cannot "}`
	const done = `data: {"type":"response.refusal.done","refusal":"cannot help"}`
	const completed = `data: {"type":"response.completed","response":{"id":"resp_r","output":[{"type":"message","content":[{"type":"refusal","refusal":"cannot help"}]}]}}`
	for _, tc := range []struct {
		name  string
		lines []string
	}{
		{"delta and done", []string{delta, done, completed}},
		{"done only", []string{done, completed}},
		{"completed only", []string{completed}},
		{"completed fills missing delta", []string{delta, completed}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var streamed strings.Builder
			resp, err := parseResponsesStream(strings.NewReader(chatStream(tc.lines...)), func(event StreamEvent) {
				streamed.WriteString(event.Text)
			})
			if err != nil || resp.Message.Content != "cannot help" || streamed.String() != "cannot help" {
				t.Fatalf("content=%q streamed=%q err=%v", resp.Message.Content, streamed.String(), err)
			}
		})
	}
}

func TestParseResponsesStreamRequiresCompletion(t *testing.T) {
	const partial = `data: {"type":"response.output_text.delta","delta":"partial"}`
	for _, stream := range []string{partial, chatStream(partial, "data: [DONE]")} {
		resp, err := parseResponsesStream(strings.NewReader(stream), nil)
		if !errors.Is(err, io.ErrUnexpectedEOF) || resp.Message.Content != "partial" {
			t.Fatalf("content=%q err=%v", resp.Message.Content, err)
		}
	}
}

func TestParseResponsesIncompleteFailed(t *testing.T) {
	for _, tc := range []struct {
		name, stream, want string
	}{
		{"incomplete event", `data: {"type":"response.incomplete","response":{"incomplete_details":{"reason":"max_output_tokens"}}}` + "\n", "max_output_tokens"},
		{"failed event", `data: {"type":"response.failed","response":{"error":{"message":"boom"}}}` + "\n", "boom"},
		{"completed incomplete", `data: {"type":"response.completed","response":{"status":"incomplete","incomplete_details":{"reason":"content_filter"}}}` + "\n", "content_filter"},
		{"completed failed", `data: {"type":"response.completed","response":{"status":"failed","error":{"message":"bad"}}}` + "\n", "bad"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := parseResponsesStream(strings.NewReader(tc.stream), nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			// Partial must survive for accounting: usage stays nil only
			// when unreported, never fabricated, and typed errors allow
			// callers to distinguish truncation from failure.
			_ = resp
			var incErr *ResponsesIncompleteError
			var failErr *ResponsesFailedError
			isInc := strings.Contains(tc.name, "incomplete")
			if isInc {
				if !errors.As(err, &incErr) {
					t.Fatalf("expected ResponsesIncompleteError, got %T: %v", err, err)
				}
			} else {
				if !errors.As(err, &failErr) {
					t.Fatalf("expected ResponsesFailedError, got %T: %v", err, err)
				}
			}
		})
	}
}

func TestChatGPTBodyUsesSupportedHTTPFields(t *testing.T) {
	p := &chatGPTProvider{baseURL: "https://api.openai.com/v1"}
	background, maxTokens := true, 500
	body := p.buildCodexBody(Request{
		Model: "gpt-6.2-sol", PromptCacheKey: "k", ComparisonResponseID: "resp_base",
		PreviousResponseID: "resp_prev", ConversationID: "conv_prev", StoreResponses: true,
		Background: &background, MaxOutputTokens: &maxTokens, Truncation: "auto",
		Metadata: map[string]any{"test": "value"},
		Tools:    []Tool{{Name: "read"}},
		BuiltinTools: []map[string]any{
			{"type": "web_search"}, {"type": "file_search"}, {"type": "code_interpreter"},
			{"type": "image_generation"}, {"type": "computer"}, {"type": "mcp"},
		},
	}, "durable", []Message{{Role: "user", Content: "hi"}}, true)
	if body["store"] != false || body["stream"] != true {
		t.Fatalf("wrong HTTP defaults: %v", body)
	}
	for _, key := range []string{"previous_response_id", "conversation", "background", "max_output_tokens", "metadata", "truncation"} {
		if _, ok := body[key]; ok {
			t.Fatalf("unsupported field %q in %v", key, body)
		}
	}
	if _, ok := body["prompt_cache_options"]; !ok || body["prompt_cache_key"] != "k" {
		t.Fatalf("cache options missing: %v", body)
	}
	tools, _ := body["tools"].([]map[string]any)
	if len(tools) != 2 || tools[0]["type"] != "function" || tools[1]["type"] != "web_search" {
		t.Fatalf("unexpected tools: %v", tools)
	}
	encoded, _ := json.Marshal(body["input"])
	if !strings.Contains(string(encoded), "durable") {
		t.Fatalf("durable instructions missing: %s", encoded)
	}
}

func TestChatGPTRejectsContinuationBeforeHTTP(t *testing.T) {
	path := writeChatGPTOAuth(t, chatgptoauth.Tokens{AccessToken: "test", RefreshToken: "refresh"})
	client := doerFunc(func(r *http.Request) (*http.Response, error) {
		t.Fatal("stateful request reached HTTP")
		return nil, nil
	})
	provider, err := newChatGPT(Config{ChatGPTOAuthPath: path, HTTP: client})
	if err != nil {
		t.Fatal(err)
	}
	for _, req := range []Request{
		{PreviousResponseID: "resp_missing"},
		{ConversationID: "conv_missing"},
		{StoreResponses: true},
	} {
		req.Model = "gpt-6.2-sol"
		_, err := provider.Complete(context.Background(), req, nil)
		if err == nil || !strings.Contains(err.Error(), "requires full conversation history") {
			t.Fatalf("err=%v", err)
		}
	}
}

func TestOpenCodeGoRejectedContinuationSendsNoContextFreeRetry(t *testing.T) {
	calls := 0
	client := doerFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["previous_response_id"] != "resp_missing" || body["store"] != true {
			t.Fatalf("continuation lost: %v", body)
		}
		return &http.Response{StatusCode: http.StatusNotFound, Status: "404 Not Found",
			Body: io.NopCloser(strings.NewReader(`{"error":{"message":"previous_response_id not found"}}`))}, nil
	})
	provider, err := newOpenCodeGo(Config{BaseURL: "http://go.test/v1", APIKey: "test", HTTP: client})
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Complete(context.Background(), Request{
		Model: "muse-spark-1.3-contributor", PreviousResponseID: "resp_missing",
		Messages: []Message{{Role: "tool", ToolCallID: "call_1", Content: "done"}},
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "previous_response_id not found") || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestChatGPTComparisonFallbackStripsOptionsKeepsKey(t *testing.T) {
	path := writeChatGPTOAuth(t, chatgptoauth.Tokens{AccessToken: "test", RefreshToken: "refresh"})
	calls := 0
	var bodies []map[string]any
	client := doerFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		bodies = append(bodies, body)
		if calls == 1 {
			return &http.Response{StatusCode: 400, Status: "400 Bad Request", Body: io.NopCloser(strings.NewReader(`{"error":{"message":"comparison_response_id is invalid","param":"prompt_cache_options"}}`))}, nil
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(responsesStream("ok")))}, nil
	})
	provider, err := newChatGPT(Config{ChatGPTOAuthPath: path, HTTP: client})
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Complete(context.Background(), Request{
		Model: "gpt-6-astra", PromptCacheKey: "k", ComparisonResponseID: "resp_expired",
		Messages: []Message{{Role: "user", Content: "hi"}},
	}, nil)
	if err != nil || calls != 2 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
	if _, ok := bodies[0]["prompt_cache_options"]; !ok {
		t.Fatalf("first attempt must carry comparison: %v", bodies[0])
	}
	second, _ := json.Marshal(bodies[1])
	if strings.Contains(string(second), "prompt_cache_options") || strings.Contains(string(second), "comparison_response") {
		t.Fatalf("fallback must strip comparison options: %s", second)
	}
	if _, ok := bodies[1]["prompt_cache_key"]; !ok {
		t.Fatalf("comparison-only fallback must keep the stable key: %s", second)
	}
}

func TestResponsesDiagnosticsFixTable(t *testing.T) {
	for reason, want := range map[string]string{
		"tools_changed":            "tool",
		"model_changed":            "same model",
		"prompt_cache_key_changed": "stable",
		"service_tier_changed":     "service tier",
		"text_format_changed":      "text.format",
		"reasoning_effort_changed": "reasoning",
		"verbosity_changed":        "verbosity",
		"context_compacted":        "compacted",
		"input_changed":            "earlier",
		"instructions_changed":     "durable",
		"unknown_reason":           "",
		"":                         "",
	} {
		if got := ResponsesDiagnosticsFix(reason); (want == "") != (got == "") || (want != "" && !strings.Contains(strings.ToLower(got), strings.ToLower(want))) {
			t.Fatalf("reason %q fix = %q, want containing %q", reason, got, want)
		}
	}
}

func TestResponsesBuiltinUnknownDropped(t *testing.T) {
	got := responsesBuiltinTools([]map[string]any{
		{"type": "web_search"}, {"type": "web_search_preview"}, {"type": "computer"},
		{"type": "computer_use_preview"}, {"type": "computer_use"}, {"type": "nope"}, nil,
	})
	want := map[string]bool{"web_search": true, "web_search_preview": true, "computer": true, "computer_use_preview": true}
	if len(got) != len(want) {
		t.Fatalf("allowlist = %v", got)
	}
	for _, tool := range got {
		typ, _ := tool["type"].(string)
		if !want[typ] {
			t.Fatalf("unexpected surviving type: %v", tool)
		}
		delete(want, typ)
	}
	if len(want) != 0 {
		t.Fatalf("missing types: %v", want)
	}
}

func TestChatGPTWrapperPreservesStatelessStoreFalse(t *testing.T) {
	p := &chatGPTProvider{baseURL: "https://api.openai.com/v1"}
	body := p.buildCodexBody(Request{Model: "gpt-6.2-sol", Messages: []Message{{Role: "user", Content: "hi"}}}, "durable", []Message{{Role: "user", Content: "hi"}}, true)
	if body["store"] != false {
		t.Fatalf("chatgpt must preserve store:false contract: %v", body)
	}
}

func TestParseResponsesIncompletePreservesPartial(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"type":"response.output_text.delta","delta":"hello"}`,
		`data: {"type":"response.completed","response":{"id":"resp_inc","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"message","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":100,"output_tokens":5},"prompt_cache_diagnostics":{"type":"cache_hit"}}}`,
	}, "\n") + "\n"
	resp, err := parseResponsesStream(strings.NewReader(stream), nil)
	if err == nil {
		t.Fatal("expected incomplete error")
	}
	if resp.Message.Content != "hello" {
		t.Fatalf("partial content lost: %q", resp.Message.Content)
	}
	if resp.ResponseID != "resp_inc" {
		t.Fatalf("partial id lost: %q", resp.ResponseID)
	}
	if resp.CacheDiagnostics == nil || resp.CacheDiagnostics.Type != "cache_hit" {
		t.Fatalf("partial diagnostics lost: %+v", resp.CacheDiagnostics)
	}
	if in, ok := resp.Usage.KnownInput(); !ok || in != 100 {
		t.Fatalf("partial usage lost: %+v", resp.Usage)
	}
	var incErr *ResponsesIncompleteError
	if !errors.As(err, &incErr) || incErr.Reason != "max_output_tokens" {
		t.Fatalf("typed error wrong: %T %v", err, err)
	}
}

func TestChatGPTCompletePreservesPartialOnIncomplete(t *testing.T) {
	for _, tc := range []struct {
		name, stream   string
		wantIncomplete bool
	}{
		{"completed incomplete", strings.Join([]string{
			`data: {"type":"response.output_text.delta","delta":"hello"}`,
			`data: {"type":"response.completed","response":{"id":"resp_inc","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"message","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":100,"output_tokens":5}}}`,
		}, "\n") + "\n\n", true},
		{"bare incomplete", strings.Join([]string{
			`data: {"type":"response.output_text.delta","delta":"hello"}`,
			`data: {"type":"response.incomplete","response":{"id":"resp_inc","incomplete_details":{"reason":"max_output_tokens"},"usage":{"input_tokens":100,"output_tokens":5}}}`,
		}, "\n") + "\n\n", true},
		{"bare failed", strings.Join([]string{
			`data: {"type":"response.output_text.delta","delta":"hello"}`,
			`data: {"type":"response.failed","response":{"id":"resp_inc","error":{"message":"boom"},"usage":{"input_tokens":100,"output_tokens":5}}}`,
		}, "\n") + "\n\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testChatGPTCompletePartial(t, tc.stream, tc.wantIncomplete)
		})
	}
}

func testChatGPTCompletePartial(t *testing.T, stream string, wantIncomplete bool) {
	t.Helper()
	path := writeChatGPTOAuth(t, chatgptoauth.Tokens{AccessToken: "test-access", RefreshToken: "test-refresh", AccountID: "acct-1"})
	client := doerFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(stream))}, nil
	})
	provider, err := newChatGPT(Config{HTTP: client, ChatGPTOAuthPath: path})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := provider.Complete(context.Background(), Request{Model: "gpt-6.2-sol", Messages: []Message{{Role: "user", Content: "hi"}}}, nil)
	if err == nil {
		t.Fatalf("expected terminal error for stream %q", stream)
	}
	if wantIncomplete {
		var incErr *ResponsesIncompleteError
		if !errors.As(err, &incErr) {
			t.Fatalf("expected ResponsesIncompleteError, got %T: %v", err, err)
		}
	} else {
		var failErr *ResponsesFailedError
		if !errors.As(err, &failErr) {
			t.Fatalf("expected ResponsesFailedError, got %T: %v", err, err)
		}
	}
	if resp.Message.Content != "hello" || resp.ResponseID != "resp_inc" {
		t.Fatalf("partial lost: %+v (stream %q)", resp, stream)
	}
	if in, ok := resp.Usage.KnownInput(); !ok || in != 100 {
		t.Fatalf("partial usage lost: %+v (stream %q)", resp.Usage, stream)
	}
}
