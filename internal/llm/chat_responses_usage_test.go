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

// Helpers for presence assertions: every field is either known (reported,
// including explicit zero) or unknown (nil).

func mustKnown(t *testing.T, what string, got int, ok bool, want int) {
	t.Helper()
	if !ok || got != want {
		t.Fatalf("%s = %d,%v want %d,true", what, got, ok, want)
	}
}

func mustUnknown(t *testing.T, what string, got int, ok bool) {
	t.Helper()
	if ok {
		t.Fatalf("%s = %d,true want unknown", what, got)
	}
}

func chatStream(lines ...string) string {
	return strings.Join(lines, "\n") + "\n"
}

func TestChatUsageCanonicalBreakdown(t *testing.T) {
	// Fixture from #74: 1000 prompt, 100 completion, 800 cached,
	// 20 reasoning => input 1000, output 100, cached 800, total 1100.
	stream := chatStream(
		`data: {"choices":[{"delta":{"content":"hi"}}]}`,
		`data: {"usage":{"prompt_tokens":1000,"completion_tokens":100,"prompt_tokens_details":{"cached_tokens":800},"completion_tokens_details":{"reasoning_tokens":20}}}`,
		`data: [DONE]`,
	)
	resp, err := parseOpenAIStream(strings.NewReader(stream), nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Usage == nil {
		t.Fatal("usage is nil")
	}
	in, ok := resp.Usage.KnownInput()
	mustKnown(t, "input", in, ok, 1000)
	out, ok := resp.Usage.KnownOutput()
	mustKnown(t, "output", out, ok, 100)
	cached, ok := resp.Usage.KnownCacheRead()
	mustKnown(t, "cache read", cached, ok, 800)
	reasoning, ok := resp.Usage.KnownReasoning()
	mustKnown(t, "reasoning", reasoning, ok, 20)
	// No write path is mapped on Chat Completions, and uncached input is
	// never derived by subtraction.
	write, ok := resp.Usage.KnownCacheWrite()
	mustUnknown(t, "cache write", write, ok)
	uncached, ok := resp.Usage.KnownUncached()
	mustUnknown(t, "uncached input", uncached, ok)
	if got := resp.Usage.Total(); got != 1100 {
		t.Fatalf("total = %d, want 1100", got)
	}
	if resp.Message.Content != "hi" {
		t.Fatalf("content = %q", resp.Message.Content)
	}
}

func TestChatUsagePresenceMerge(t *testing.T) {
	full := `data: {"usage":{"prompt_tokens":1000,"completion_tokens":100,"prompt_tokens_details":{"cached_tokens":800},"completion_tokens_details":{"reasoning_tokens":20}}}`
	tests := []struct {
		name  string
		lines []string
		check func(t *testing.T, usage *Usage)
	}{
		{
			name: "repeated snapshots merge latest fields",
			lines: []string{
				`data: {"usage":{"prompt_tokens":1000}}`,
				`data: {"usage":{"completion_tokens":100,"prompt_tokens_details":{"cached_tokens":800},"completion_tokens_details":{"reasoning_tokens":20}}}`,
				`data: [DONE]`,
			},
			check: func(t *testing.T, usage *Usage) {
				t.Helper()
				if usage == nil {
					t.Fatal("usage is nil")
				}
				in, ok := usage.KnownInput()
				mustKnown(t, "input", in, ok, 1000)
				out, ok := usage.KnownOutput()
				mustKnown(t, "output", out, ok, 100)
				cached, ok := usage.KnownCacheRead()
				mustKnown(t, "cache read", cached, ok, 800)
				reasoning, ok := usage.KnownReasoning()
				mustKnown(t, "reasoning", reasoning, ok, 20)
			},
		},
		{
			name:  "empty usage keeps earlier values",
			lines: []string{full, `data: {"usage":{}}`, `data: [DONE]`},
			check: func(t *testing.T, usage *Usage) {
				t.Helper()
				if usage == nil {
					t.Fatal("usage is nil")
				}
				in, ok := usage.KnownInput()
				mustKnown(t, "input", in, ok, 1000)
				cached, ok := usage.KnownCacheRead()
				mustKnown(t, "cache read", cached, ok, 800)
			},
		},
		{
			name: "null details keep earlier values",
			lines: []string{
				full,
				`data: {"usage":{"prompt_tokens_details":null,"completion_tokens_details":null}}`,
				`data: [DONE]`,
			},
			check: func(t *testing.T, usage *Usage) {
				t.Helper()
				if usage == nil {
					t.Fatal("usage is nil")
				}
				cached, ok := usage.KnownCacheRead()
				mustKnown(t, "cache read", cached, ok, 800)
				reasoning, ok := usage.KnownReasoning()
				mustKnown(t, "reasoning", reasoning, ok, 20)
			},
		},
		{
			name: "explicit zero replaces previous value",
			lines: []string{
				full,
				`data: {"usage":{"prompt_tokens_details":{"cached_tokens":0}}}`,
				`data: [DONE]`,
			},
			check: func(t *testing.T, usage *Usage) {
				t.Helper()
				if usage == nil {
					t.Fatal("usage is nil")
				}
				cached, ok := usage.KnownCacheRead()
				mustKnown(t, "cache read", cached, ok, 0)
				in, ok := usage.KnownInput()
				mustKnown(t, "input", in, ok, 1000)
			},
		},
		{
			name: "output only leaves input unknown",
			lines: []string{
				`data: {"usage":{"completion_tokens":100}}`,
				`data: [DONE]`,
			},
			check: func(t *testing.T, usage *Usage) {
				t.Helper()
				if usage == nil {
					t.Fatal("usage is nil")
				}
				out, ok := usage.KnownOutput()
				mustKnown(t, "output", out, ok, 100)
				in, ok := usage.KnownInput()
				mustUnknown(t, "input", in, ok)
			},
		},
		{
			name: "input only leaves output unknown",
			lines: []string{
				`data: {"usage":{"prompt_tokens":1000}}`,
				`data: [DONE]`,
			},
			check: func(t *testing.T, usage *Usage) {
				t.Helper()
				if usage == nil {
					t.Fatal("usage is nil")
				}
				in, ok := usage.KnownInput()
				mustKnown(t, "input", in, ok, 1000)
				out, ok := usage.KnownOutput()
				mustUnknown(t, "output", out, ok)
			},
		},
		{
			name: "missing usage is absent",
			lines: []string{
				`data: {"choices":[{"delta":{"content":"hi"}}]}`,
				`data: [DONE]`,
			},
			check: func(t *testing.T, usage *Usage) {
				t.Helper()
				if usage != nil {
					t.Fatalf("usage = %+v, want nil", usage)
				}
			},
		},
		{
			name: "empty usage alone is absent",
			lines: []string{
				`data: {"usage":{}}`,
				`data: [DONE]`,
			},
			check: func(t *testing.T, usage *Usage) {
				t.Helper()
				if usage != nil {
					t.Fatalf("usage = %+v, want nil", usage)
				}
			},
		},
		{
			name: "explicit zeros are reported",
			lines: []string{
				`data: {"usage":{"prompt_tokens":0,"completion_tokens":0}}`,
				`data: [DONE]`,
			},
			check: func(t *testing.T, usage *Usage) {
				t.Helper()
				if usage == nil {
					t.Fatal("usage is nil")
				}
				in, ok := usage.KnownInput()
				mustKnown(t, "input", in, ok, 0)
				out, ok := usage.KnownOutput()
				mustKnown(t, "output", out, ok, 0)
			},
		},
		{
			name: "unmapped write field is ignored",
			lines: []string{
				`data: {"usage":{"prompt_tokens":100,"completion_tokens":10,"cache_write_tokens":50}}`,
				`data: [DONE]`,
			},
			check: func(t *testing.T, usage *Usage) {
				t.Helper()
				if usage == nil {
					t.Fatal("usage is nil")
				}
				write, ok := usage.KnownCacheWrite()
				mustUnknown(t, "cache write", write, ok)
				in, ok := usage.KnownInput()
				mustKnown(t, "input", in, ok, 100)
			},
		},
		{
			name: "inconsistent cache read is dropped",
			lines: []string{
				`data: {"usage":{"prompt_tokens":100,"completion_tokens":10,"prompt_tokens_details":{"cached_tokens":5000}}}`,
				`data: [DONE]`,
			},
			check: func(t *testing.T, usage *Usage) {
				t.Helper()
				if usage == nil {
					t.Fatal("usage is nil")
				}
				in, ok := usage.KnownInput()
				mustKnown(t, "input", in, ok, 100)
				cached, ok := usage.KnownCacheRead()
				mustUnknown(t, "cache read", cached, ok)
			},
		},
		{
			name: "negative values are unknown",
			lines: []string{
				`data: {"usage":{"prompt_tokens":-5,"completion_tokens":100}}`,
				`data: [DONE]`,
			},
			check: func(t *testing.T, usage *Usage) {
				t.Helper()
				if usage == nil {
					t.Fatal("usage is nil")
				}
				in, ok := usage.KnownInput()
				mustUnknown(t, "input", in, ok)
				out, ok := usage.KnownOutput()
				mustKnown(t, "output", out, ok, 100)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resp, err := parseOpenAIStream(strings.NewReader(chatStream(test.lines...)), nil)
			if err != nil {
				t.Fatal(err)
			}
			test.check(t, resp.Usage)
		})
	}
}

func TestChatUsageMalformedMetadataKeepsText(t *testing.T) {
	stream := chatStream(
		`data: {"usage":{"prompt_tokens":"lots"},"choices":[{"delta":{"content":"hi"}}]}`,
		`data: [DONE]`,
	)
	resp, err := parseOpenAIStream(strings.NewReader(stream), nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message.Content != "hi" {
		t.Fatalf("content = %q, want %q", resp.Message.Content, "hi")
	}
	if resp.Usage != nil {
		t.Fatalf("usage = %+v, want nil for malformed metadata", resp.Usage)
	}
}

func TestResponsesUsageCanonicalBreakdown(t *testing.T) {
	// Fixture from #74: 1000 input, 100 output, 200 read, 300 write =>
	// input stays 1000 (never 1500); total is 1100.
	stream := chatStream(
		`data: {"type":"response.output_text.delta","delta":"hi"}`,
		`data: {"type":"response.completed","response":{"output":[{"type":"message","content":[{"type":"output_text","text":"hi"}]}],"usage":{"input_tokens":1000,"output_tokens":100,"input_tokens_details":{"cached_tokens":200,"cache_write_tokens":300},"output_tokens_details":{"reasoning_tokens":20}}}}`,
	)
	resp, err := parseResponsesStream(strings.NewReader(stream), nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Usage == nil {
		t.Fatal("usage is nil")
	}
	in, ok := resp.Usage.KnownInput()
	mustKnown(t, "input", in, ok, 1000)
	out, ok := resp.Usage.KnownOutput()
	mustKnown(t, "output", out, ok, 100)
	cached, ok := resp.Usage.KnownCacheRead()
	mustKnown(t, "cache read", cached, ok, 200)
	write, ok := resp.Usage.KnownCacheWrite()
	mustKnown(t, "cache write", write, ok, 300)
	reasoning, ok := resp.Usage.KnownReasoning()
	mustKnown(t, "reasoning", reasoning, ok, 20)
	// Uncached input is never derived by subtraction.
	uncached, ok := resp.Usage.KnownUncached()
	mustUnknown(t, "uncached input", uncached, ok)
	if got := resp.Usage.Total(); got != 1100 {
		t.Fatalf("total = %d, want 1100", got)
	}
	if resp.Message.Content != "hi" {
		t.Fatalf("content = %q", resp.Message.Content)
	}
}

func TestResponsesUsagePresenceMerge(t *testing.T) {
	completed := func(usage string) string {
		return `data: {"type":"response.completed","response":{"output":[],"usage":` + usage + `}}`
	}
	tests := []struct {
		name  string
		lines []string
		check func(t *testing.T, usage *Usage)
	}{
		{
			name: "repeated snapshots merge latest fields",
			lines: []string{
				completed(`{"input_tokens":1000,"input_tokens_details":{"cached_tokens":200}}`),
				completed(`{"output_tokens":100,"input_tokens_details":{"cache_write_tokens":300},"output_tokens_details":{"reasoning_tokens":20}}`),
			},
			check: func(t *testing.T, usage *Usage) {
				t.Helper()
				if usage == nil {
					t.Fatal("usage is nil")
				}
				in, ok := usage.KnownInput()
				mustKnown(t, "input", in, ok, 1000)
				out, ok := usage.KnownOutput()
				mustKnown(t, "output", out, ok, 100)
				cached, ok := usage.KnownCacheRead()
				mustKnown(t, "cache read", cached, ok, 200)
				write, ok := usage.KnownCacheWrite()
				mustKnown(t, "cache write", write, ok, 300)
				reasoning, ok := usage.KnownReasoning()
				mustKnown(t, "reasoning", reasoning, ok, 20)
			},
		},
		{
			name: "null details keep earlier values",
			lines: []string{
				completed(`{"input_tokens":1000,"output_tokens":100,"input_tokens_details":{"cached_tokens":200,"cache_write_tokens":300}}`),
				completed(`{"input_tokens_details":null,"output_tokens_details":null}`),
			},
			check: func(t *testing.T, usage *Usage) {
				t.Helper()
				if usage == nil {
					t.Fatal("usage is nil")
				}
				cached, ok := usage.KnownCacheRead()
				mustKnown(t, "cache read", cached, ok, 200)
				write, ok := usage.KnownCacheWrite()
				mustKnown(t, "cache write", write, ok, 300)
			},
		},
		{
			name: "explicit zero replaces previous value",
			lines: []string{
				completed(`{"input_tokens":1000,"output_tokens":100,"input_tokens_details":{"cached_tokens":200,"cache_write_tokens":300}}`),
				completed(`{"input_tokens_details":{"cache_write_tokens":0}}`),
			},
			check: func(t *testing.T, usage *Usage) {
				t.Helper()
				if usage == nil {
					t.Fatal("usage is nil")
				}
				write, ok := usage.KnownCacheWrite()
				mustKnown(t, "cache write", write, ok, 0)
				cached, ok := usage.KnownCacheRead()
				mustKnown(t, "cache read", cached, ok, 200)
			},
		},
		{
			name: "input only leaves output unknown",
			lines: []string{
				completed(`{"input_tokens":1000}`),
			},
			check: func(t *testing.T, usage *Usage) {
				t.Helper()
				if usage == nil {
					t.Fatal("usage is nil")
				}
				in, ok := usage.KnownInput()
				mustKnown(t, "input", in, ok, 1000)
				out, ok := usage.KnownOutput()
				mustUnknown(t, "output", out, ok)
			},
		},
		{
			name: "output only leaves input unknown",
			lines: []string{
				completed(`{"output_tokens":100}`),
			},
			check: func(t *testing.T, usage *Usage) {
				t.Helper()
				if usage == nil {
					t.Fatal("usage is nil")
				}
				out, ok := usage.KnownOutput()
				mustKnown(t, "output", out, ok, 100)
				in, ok := usage.KnownInput()
				mustUnknown(t, "input", in, ok)
			},
		},
		{
			name: "missing usage is absent",
			lines: []string{
				`data: {"type":"response.completed","response":{"output":[]}}`,
			},
			check: func(t *testing.T, usage *Usage) {
				t.Helper()
				if usage != nil {
					t.Fatalf("usage = %+v, want nil", usage)
				}
			},
		},
		{
			name: "empty usage alone is absent",
			lines: []string{
				completed(`{}`),
			},
			check: func(t *testing.T, usage *Usage) {
				t.Helper()
				if usage != nil {
					t.Fatalf("usage = %+v, want nil", usage)
				}
			},
		},
		{
			name: "explicit zeros are reported",
			lines: []string{
				completed(`{"input_tokens":0,"output_tokens":0}`),
			},
			check: func(t *testing.T, usage *Usage) {
				t.Helper()
				if usage == nil {
					t.Fatal("usage is nil")
				}
				in, ok := usage.KnownInput()
				mustKnown(t, "input", in, ok, 0)
				out, ok := usage.KnownOutput()
				mustKnown(t, "output", out, ok, 0)
			},
		},
		{
			name: "undocumented write paths are ignored",
			lines: []string{
				completed(`{"input_tokens":1000,"output_tokens":100,"cache_write_tokens":999,"input_tokens_details":{"cached_tokens":200}}`),
			},
			check: func(t *testing.T, usage *Usage) {
				t.Helper()
				if usage == nil {
					t.Fatal("usage is nil")
				}
				// Only input_tokens_details.cache_write_tokens maps; a
				// top-level write field must not be inferred.
				write, ok := usage.KnownCacheWrite()
				mustUnknown(t, "cache write", write, ok)
				in, ok := usage.KnownInput()
				mustKnown(t, "input", in, ok, 1000)
			},
		},
		{
			name: "inconsistent breakdown is dropped",
			lines: []string{
				completed(`{"input_tokens":100,"output_tokens":10,"input_tokens_details":{"cached_tokens":5000}}`),
			},
			check: func(t *testing.T, usage *Usage) {
				t.Helper()
				if usage == nil {
					t.Fatal("usage is nil")
				}
				in, ok := usage.KnownInput()
				mustKnown(t, "input", in, ok, 100)
				cached, ok := usage.KnownCacheRead()
				mustUnknown(t, "cache read", cached, ok)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resp, err := parseResponsesStream(strings.NewReader(chatStream(test.lines...)), nil)
			if err != nil {
				t.Fatal(err)
			}
			test.check(t, resp.Usage)
		})
	}
}

func TestResponsesUsageMalformedMetadataKeepsText(t *testing.T) {
	stream := chatStream(
		`data: {"type":"response.output_text.delta","delta":"hi"}`,
		`data: {"type":"response.completed","response":{"output":[{"type":"message","content":[{"type":"output_text","text":"hi"}]}],"usage":{"input_tokens":"lots"}}}`,
	)
	resp, err := parseResponsesStream(strings.NewReader(stream), nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message.Content != "hi" {
		t.Fatalf("content = %q, want %q", resp.Message.Content, "hi")
	}
	if resp.Usage != nil {
		t.Fatalf("usage = %+v, want nil for malformed metadata", resp.Usage)
	}
}

func TestResponsesUsagePreservesToolAndReasoningReplay(t *testing.T) {
	stream := chatStream(
		`data: {"type":"response.output_text.delta","delta":"Checking"}`,
		`data: {"type":"response.output_item.added","output_index":2,"item":{"type":"function_call","call_id":"call_1","name":"read","arguments":""}}`,
		`data: {"type":"response.function_call_arguments.delta","output_index":2,"delta":"{\"path\":\"README.md\"}"}`,
		`data: {"type":"response.completed","response":{"usage":{"input_tokens":1000,"output_tokens":100,"input_tokens_details":{"cached_tokens":200,"cache_write_tokens":300}},"output":[{"type":"reasoning","encrypted_content":"opaque"},{"type":"message","content":[{"type":"output_text","text":"Checking"}]},{"type":"function_call","call_id":"call_1","name":"read","arguments":"{\"path\":\"README.md\"}"}]}}`,
	)
	var thinking, output strings.Builder
	resp, err := parseResponsesStream(strings.NewReader(stream), func(event StreamEvent) {
		switch event.Kind {
		case StreamThinking:
			thinking.WriteString(event.Text)
		case StreamOutput:
			output.WriteString(event.Text)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if output.String() != "Checking" || resp.Message.Content != "Checking" {
		t.Fatalf("content = %q, streamed = %q", resp.Message.Content, output.String())
	}
	if len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("tool calls = %+v", resp.Message.ToolCalls)
	}
	call := resp.Message.ToolCalls[0]
	if call.ID != "call_1" || call.Name != "read" || string(call.Arguments) != `{"path":"README.md"}` {
		t.Fatalf("tool call = %#v", call)
	}
	if !strings.Contains(string(resp.Message.ReasoningDetails), `"encrypted_content":"opaque"`) {
		t.Fatalf("reasoning details lost: %s", resp.Message.ReasoningDetails)
	}
	if !json.Valid(resp.Message.ReasoningDetails) {
		t.Fatalf("reasoning details invalid: %s", resp.Message.ReasoningDetails)
	}
	if resp.Usage == nil {
		t.Fatal("usage is nil")
	}
	in, ok := resp.Usage.KnownInput()
	mustKnown(t, "input", in, ok, 1000)
	write, ok := resp.Usage.KnownCacheWrite()
	mustKnown(t, "cache write", write, ok, 300)
}

// TestChatResponsesUsageRouteDispatch exercises the openai, ChatGPT and
// OpenCode Go Responses route dispatch over real HTTP (httptest), both with
// and without a text callback. Both are streaming paths: the request bodies
// must keep stream enabled and carry no new cache-control fields.
func TestChatResponsesUsageRouteDispatch(t *testing.T) {
	chatBody := chatStream(
		`data: {"choices":[{"delta":{"content":"hello "}}]}`,
		`data: {"usage":{"prompt_tokens":1000,"completion_tokens":100,"prompt_tokens_details":{"cached_tokens":800},"completion_tokens_details":{"reasoning_tokens":20}}}`,
		`data: [DONE]`,
	)
	responsesBody := chatStream(
		`data: {"type":"response.output_text.delta","delta":"hello "}`,
		`data: {"type":"response.completed","response":{"output":[{"type":"message","content":[{"type":"output_text","text":"hello "}]}],"usage":{"input_tokens":1000,"output_tokens":100,"input_tokens_details":{"cached_tokens":200,"cache_write_tokens":300}}}}`,
	)

	checkNoCacheControl := func(t *testing.T, r *http.Request) {
		t.Helper()
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if _, ok := body["prompt_cache_key"]; ok {
			t.Fatal("request must not emit PromptCacheKey in this issue")
		}
		var streamFlag bool
		if err := json.Unmarshal(body["stream"], &streamFlag); err != nil || !streamFlag {
			t.Fatalf("stream flag = %s, want true", body["stream"])
		}
	}

	t.Run("openai chat route", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/chat/completions" {
				t.Errorf("path = %q", r.URL.Path)
			}
			checkNoCacheControl(t, r)
			io.WriteString(w, chatBody)
		}))
		defer server.Close()
		provider, err := newOpenAI(Config{BaseURL: server.URL, APIKey: "secret", HTTP: server.Client()})
		if err != nil {
			t.Fatal(err)
		}
		for _, withCallback := range []bool{true, false} {
			var streamed strings.Builder
			var callback StreamCallback
			if withCallback {
				callback = func(event StreamEvent) { streamed.WriteString(event.Text) }
			}
			resp, err := provider.Complete(context.Background(), Request{
				Model:    "gpt-5",
				Messages: []Message{{Role: "user", Content: "hi"}},
			}, callback)
			if err != nil {
				t.Fatal(err)
			}
			if withCallback && streamed.String() != "hello " {
				t.Fatalf("streamed = %q", streamed.String())
			}
			if resp.Message.Content != "hello " {
				t.Fatalf("content = %q", resp.Message.Content)
			}
			if resp.Usage == nil {
				t.Fatal("usage is nil")
			}
			in, ok := resp.Usage.KnownInput()
			mustKnown(t, "input", in, ok, 1000)
			cached, ok := resp.Usage.KnownCacheRead()
			mustKnown(t, "cache read", cached, ok, 800)
			write, ok := resp.Usage.KnownCacheWrite()
			mustUnknown(t, "cache write", write, ok)
		}
	})

	t.Run("opencode-go responses route", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/responses" {
				t.Errorf("path = %q", r.URL.Path)
			}
			checkNoCacheControl(t, r)
			io.WriteString(w, responsesBody)
		}))
		defer server.Close()
		provider, err := newOpenCodeGo(Config{BaseURL: server.URL, APIKey: "go-secret", HTTP: server.Client()})
		if err != nil {
			t.Fatal(err)
		}
		for _, withCallback := range []bool{true, false} {
			var streamed strings.Builder
			var callback StreamCallback
			if withCallback {
				callback = func(event StreamEvent) { streamed.WriteString(event.Text) }
			}
			resp, err := provider.Complete(context.Background(), Request{
				Model:    "muse-spark-1.3-contributor",
				Messages: []Message{{Role: "user", Content: "hi"}},
			}, callback)
			if err != nil {
				t.Fatal(err)
			}
			if withCallback && streamed.String() != "hello " {
				t.Fatalf("streamed = %q", streamed.String())
			}
			if resp.Message.Content != "hello " {
				t.Fatalf("content = %q", resp.Message.Content)
			}
			if resp.Usage == nil {
				t.Fatal("usage is nil")
			}
			in, ok := resp.Usage.KnownInput()
			mustKnown(t, "input", in, ok, 1000)
			cached, ok := resp.Usage.KnownCacheRead()
			mustKnown(t, "cache read", cached, ok, 200)
			write, ok := resp.Usage.KnownCacheWrite()
			mustKnown(t, "cache write", write, ok, 300)
		}
	})

	t.Run("chatgpt responses route", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/responses" {
				t.Errorf("path = %q", r.URL.Path)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer test-access" {
				t.Errorf("authorization = %q", got)
			}
			checkNoCacheControl(t, r)
			io.WriteString(w, responsesBody)
		}))
		defer server.Close()
		path := writeChatGPTOAuth(t, chatgptoauth.Tokens{AccessToken: "test-access", RefreshToken: "test-refresh", AccountID: "acct-1"})
		provider, err := newChatGPT(Config{BaseURL: server.URL, HTTP: server.Client(), ChatGPTOAuthPath: path})
		if err != nil {
			t.Fatal(err)
		}
		for _, withCallback := range []bool{true, false} {
			var streamed strings.Builder
			var callback StreamCallback
			if withCallback {
				callback = func(event StreamEvent) { streamed.WriteString(event.Text) }
			}
			resp, err := provider.Complete(context.Background(), Request{
				Model:    "gpt-5.6-codex",
				Messages: []Message{{Role: "user", Content: "hi"}},
			}, callback)
			if err != nil {
				t.Fatal(err)
			}
			if withCallback && streamed.String() != "hello " {
				t.Fatalf("streamed = %q", streamed.String())
			}
			if resp.Message.Content != "hello " {
				t.Fatalf("content = %q", resp.Message.Content)
			}
			if resp.Usage == nil {
				t.Fatal("usage is nil")
			}
			in, ok := resp.Usage.KnownInput()
			mustKnown(t, "input", in, ok, 1000)
			cached, ok := resp.Usage.KnownCacheRead()
			mustKnown(t, "cache read", cached, ok, 200)
			write, ok := resp.Usage.KnownCacheWrite()
			mustKnown(t, "cache write", write, ok, 300)
		}
	})
}
