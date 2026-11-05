package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func anthropicStream(lines ...string) string {
	return strings.Join(lines, "\n") + "\n"
}

func mustKnownAnthropic(t *testing.T, what string, got int, ok bool, want int) {
	t.Helper()
	if !ok || got != want {
		t.Fatalf("%s = %d,%v want %d,true", what, got, ok, want)
	}
}

func mustUnknownAnthropic(t *testing.T, what string, got int, ok bool) {
	t.Helper()
	if ok {
		t.Fatalf("%s = %d,true want unknown", what, got)
	}
}

func TestAnthropicUsageCanonicalBreakdown(t *testing.T) {
	// Fixture from #75: uncached 100 + read 800 + write 200 => complete
	// input 1100; output snapshots 1, 12, 30 => output 30 (replace, no sum).
	stream := anthropicStream(
		`data: {"type":"message_start","message":{"usage":{"input_tokens":100,"cache_read_input_tokens":800,"cache_creation_input_tokens":200,"output_tokens":1}}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
		`data: {"type":"message_delta","usage":{"output_tokens":12}}`,
		`data: {"type":"message_delta","usage":{"output_tokens":30}}`,
	)
	resp, err := parseAnthropicStream(strings.NewReader(stream), nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Usage == nil {
		t.Fatal("usage is nil")
	}
	in, ok := resp.Usage.KnownInput()
	mustKnownAnthropic(t, "input", in, ok, 1100)
	uncached, ok := resp.Usage.KnownUncached()
	mustKnownAnthropic(t, "uncached", uncached, ok, 100)
	cached, ok := resp.Usage.KnownCacheRead()
	mustKnownAnthropic(t, "cache read", cached, ok, 800)
	write, ok := resp.Usage.KnownCacheWrite()
	mustKnownAnthropic(t, "cache write", write, ok, 200)
	out, ok := resp.Usage.KnownOutput()
	mustKnownAnthropic(t, "output", out, ok, 30)
	if got := resp.Usage.Total(); got != 1130 {
		t.Fatalf("total = %d, want 1130", got)
	}
	if resp.Message.Content != "hi" {
		t.Fatalf("content = %q", resp.Message.Content)
	}
}

func TestAnthropicUsageAllZeroGivesCompleteInput(t *testing.T) {
	stream := anthropicStream(
		`data: {"type":"message_start","message":{"usage":{"input_tokens":100,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}`,
		`data: {"type":"message_delta","usage":{"output_tokens":0}}`,
	)
	resp, err := parseAnthropicStream(strings.NewReader(stream), nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Usage == nil {
		t.Fatal("usage is nil")
	}
	in, ok := resp.Usage.KnownInput()
	mustKnownAnthropic(t, "input", in, ok, 100)
	uncached, ok := resp.Usage.KnownUncached()
	mustKnownAnthropic(t, "uncached", uncached, ok, 100)
	cached, ok := resp.Usage.KnownCacheRead()
	mustKnownAnthropic(t, "cache read", cached, ok, 0)
	write, ok := resp.Usage.KnownCacheWrite()
	mustKnownAnthropic(t, "cache write", write, ok, 0)
	out, ok := resp.Usage.KnownOutput()
	mustKnownAnthropic(t, "output", out, ok, 0)
}

func TestAnthropicUsagePresenceMerge(t *testing.T) {
	fullStart := `data: {"type":"message_start","message":{"usage":{"input_tokens":100,"cache_read_input_tokens":800,"cache_creation_input_tokens":200,"output_tokens":1}}}`
	tests := []struct {
		name  string
		lines []string
		check func(t *testing.T, usage *Usage)
	}{
		{
			name: "repeated snapshots merge latest fields",
			lines: []string{
				`data: {"type":"message_start","message":{"usage":{"input_tokens":100,"cache_read_input_tokens":800}}}`,
				`data: {"type":"message_delta","usage":{"cache_creation_input_tokens":200,"output_tokens":30}}`,
			},
			check: func(t *testing.T, usage *Usage) {
				t.Helper()
				if usage == nil {
					t.Fatal("usage is nil")
				}
				in, ok := usage.KnownInput()
				mustKnownAnthropic(t, "input", in, ok, 1100)
				out, ok := usage.KnownOutput()
				mustKnownAnthropic(t, "output", out, ok, 30)
			},
		},
		{
			name:  "empty delta keeps earlier values",
			lines: []string{fullStart, `data: {"type":"message_delta","usage":{}}`},
			check: func(t *testing.T, usage *Usage) {
				t.Helper()
				if usage == nil {
					t.Fatal("usage is nil")
				}
				in, ok := usage.KnownInput()
				mustKnownAnthropic(t, "input", in, ok, 1100)
				out, ok := usage.KnownOutput()
				mustKnownAnthropic(t, "output", out, ok, 1)
			},
		},
		{
			name: "null usage keeps earlier values",
			lines: []string{
				fullStart,
				`data: {"type":"message_delta","usage":null}`,
			},
			check: func(t *testing.T, usage *Usage) {
				t.Helper()
				if usage == nil {
					t.Fatal("usage is nil")
				}
				in, ok := usage.KnownInput()
				mustKnownAnthropic(t, "input", in, ok, 1100)
			},
		},
		{
			name: "explicit zero replaces previous value",
			lines: []string{
				fullStart,
				`data: {"type":"message_delta","usage":{"cache_read_input_tokens":0}}`,
			},
			check: func(t *testing.T, usage *Usage) {
				t.Helper()
				if usage == nil {
					t.Fatal("usage is nil")
				}
				cached, ok := usage.KnownCacheRead()
				mustKnownAnthropic(t, "cache read", cached, ok, 0)
				// 100 + 0 + 200 = 300.
				in, ok := usage.KnownInput()
				mustKnownAnthropic(t, "input", in, ok, 300)
			},
		},
		{
			name: "missing one component leaves complete unknown",
			lines: []string{
				`data: {"type":"message_start","message":{"usage":{"input_tokens":100,"cache_read_input_tokens":800}}}`,
			},
			check: func(t *testing.T, usage *Usage) {
				t.Helper()
				if usage == nil {
					t.Fatal("usage is nil")
				}
				in, ok := usage.KnownInput()
				mustUnknownAnthropic(t, "input", in, ok)
				uncached, ok := usage.KnownUncached()
				mustKnownAnthropic(t, "uncached", uncached, ok, 100)
				cached, ok := usage.KnownCacheRead()
				mustKnownAnthropic(t, "cache read", cached, ok, 800)
				write, ok := usage.KnownCacheWrite()
				mustUnknownAnthropic(t, "cache write", write, ok)
			},
		},
		{
			name: "legacy input-only preserves uncached",
			lines: []string{
				`data: {"type":"message_start","message":{"usage":{"input_tokens":900}}}`,
			},
			check: func(t *testing.T, usage *Usage) {
				t.Helper()
				if usage == nil {
					t.Fatal("usage is nil")
				}
				uncached, ok := usage.KnownUncached()
				mustKnownAnthropic(t, "uncached", uncached, ok, 900)
				in, ok := usage.KnownInput()
				mustUnknownAnthropic(t, "input", in, ok)
			},
		},
		{
			name: "output-only delta leaves input unknown",
			lines: []string{
				`data: {"type":"message_delta","usage":{"output_tokens":30}}`,
			},
			check: func(t *testing.T, usage *Usage) {
				t.Helper()
				if usage == nil {
					t.Fatal("usage is nil")
				}
				out, ok := usage.KnownOutput()
				mustKnownAnthropic(t, "output", out, ok, 30)
				in, ok := usage.KnownInput()
				mustUnknownAnthropic(t, "input", in, ok)
			},
		},
		{
			name: "missing usage object is absent",
			lines: []string{
				`data: {"type":"message_start","message":{"id":"msg_1"}}`,
				`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
			},
			check: func(t *testing.T, usage *Usage) {
				t.Helper()
				if usage != nil {
					t.Fatalf("usage = %+v, want nil", usage)
				}
			},
		},
		{
			name: "zero output is reported",
			lines: []string{
				`data: {"type":"message_delta","usage":{"output_tokens":0}}`,
			},
			check: func(t *testing.T, usage *Usage) {
				t.Helper()
				if usage == nil {
					t.Fatal("usage is nil")
				}
				out, ok := usage.KnownOutput()
				mustKnownAnthropic(t, "output", out, ok, 0)
			},
		},
		{
			name: "aggregate plus TTL counts use aggregate once",
			lines: []string{
				`data: {"type":"message_start","message":{"usage":{"input_tokens":100,"cache_read_input_tokens":800,"cache_creation_input_tokens":200,"cache_creation":{"ephemeral_5m_input_tokens":200,"ephemeral_1h_input_tokens":200}}}}`,
			},
			check: func(t *testing.T, usage *Usage) {
				t.Helper()
				if usage == nil {
					t.Fatal("usage is nil")
				}
				write, ok := usage.KnownCacheWrite()
				mustKnownAnthropic(t, "cache write", write, ok, 200)
				in, ok := usage.KnownInput()
				mustKnownAnthropic(t, "input", in, ok, 1100)
			},
		},
		{
			name: "large components sum without dropping",
			lines: []string{
				`data: {"type":"message_start","message":{"usage":{"input_tokens":10,"cache_read_input_tokens":5000,"cache_creation_input_tokens":200}}}`,
			},
			check: func(t *testing.T, usage *Usage) {
				t.Helper()
				// Complete input is derived as the component sum, so it is
				// consistent by construction (10 + 5000 + 200 = 5210) and
				// retained; NormalizeUsage subset checks cannot trigger on
				// non-negative derived totals.
				if usage == nil {
					t.Fatal("usage is nil")
				}
				in, ok := usage.KnownInput()
				mustKnownAnthropic(t, "input", in, ok, 5210)
				cached, ok := usage.KnownCacheRead()
				mustKnownAnthropic(t, "cache read", cached, ok, 5000)
			},
		},
		{
			name: "negative values are unknown",
			lines: []string{
				`data: {"type":"message_start","message":{"usage":{"input_tokens":-5,"cache_read_input_tokens":800,"cache_creation_input_tokens":200}}}`,
				`data: {"type":"message_delta","usage":{"output_tokens":30}}`,
			},
			check: func(t *testing.T, usage *Usage) {
				t.Helper()
				if usage == nil {
					t.Fatal("usage is nil")
				}
				uncached, ok := usage.KnownUncached()
				mustUnknownAnthropic(t, "uncached", uncached, ok)
				in, ok := usage.KnownInput()
				mustUnknownAnthropic(t, "input", in, ok)
				out, ok := usage.KnownOutput()
				mustKnownAnthropic(t, "output", out, ok, 30)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resp, err := parseAnthropicStream(strings.NewReader(anthropicStream(test.lines...)), nil)
			if err != nil {
				t.Fatal(err)
			}
			test.check(t, resp.Usage)
		})
	}
}

func TestAnthropicUsageMalformedMetadataKeepsText(t *testing.T) {
	stream := anthropicStream(
		`data: {"type":"message_start","message":{"usage":{"input_tokens":"lots"}}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
	)
	resp, err := parseAnthropicStream(strings.NewReader(stream), nil)
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

func TestAnthropicUsagePreservesToolAssembly(t *testing.T) {
	stream := anthropicStream(
		`data: {"type":"message_start","message":{"usage":{"input_tokens":100,"cache_read_input_tokens":800,"cache_creation_input_tokens":200}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"tool_1","name":"read","input":{}}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"README"}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":".md\"}"}}`,
		`data: {"type":"message_delta","usage":{"output_tokens":30}}`,
	)
	var streamed strings.Builder
	resp, err := parseAnthropicStream(strings.NewReader(stream), func(event StreamEvent) {
		if event.Kind == StreamOutput {
			streamed.WriteString(event.Text)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("tool calls = %+v", resp.Message.ToolCalls)
	}
	call := resp.Message.ToolCalls[0]
	if call.ID != "tool_1" || call.Name != "read" || string(call.Arguments) != `{"path":"README.md"}` {
		t.Fatalf("tool call = %#v", call)
	}
	if resp.Usage == nil {
		t.Fatal("usage is nil")
	}
	in, ok := resp.Usage.KnownInput()
	mustKnownAnthropic(t, "input", in, ok, 1100)
}

// TestAnthropicUsageRouteDispatch exercises the Messages-routed OpenCode Go
// model over real HTTP (httptest), both with and without a text callback.
// Callbacks must not change usage; the request body must carry no new
// cache-control fields.
func TestAnthropicUsageRouteDispatch(t *testing.T) {
	body := anthropicStream(
		`event: message_start`,
		`data: {"type":"message_start","message":{"usage":{"input_tokens":100,"cache_read_input_tokens":800,"cache_creation_input_tokens":200}}}`,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"tool_1","name":"read","input":{}}}`,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\".\"}"}}`,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello "}}`,
		`event: message_delta`,
		`data: {"type":"message_delta","usage":{"output_tokens":30}}`,
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/messages" {
			t.Errorf("path = %q", r.URL.Path)
		}
		var payload map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if _, ok := payload["prompt_cache_key"]; ok {
			t.Error("request must not emit PromptCacheKey in this issue")
		}
		for _, field := range []string{"cache_control", "prompt_cache_breakpoint", "prompt_cache_retention"} {
			if _, ok := payload[field]; ok {
				t.Errorf("request must not emit %s in this issue", field)
			}
		}
		var streamFlag bool
		if err := json.Unmarshal(payload["stream"], &streamFlag); err != nil || !streamFlag {
			t.Errorf("stream flag = %s, want true", payload["stream"])
		}
		io.WriteString(w, body)
	}))
	defer server.Close()
	provider, err := newOpenCodeGo(Config{BaseURL: server.URL, APIKey: "go-secret", HTTP: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	var withoutUsage *Usage
	for _, withCallback := range []bool{true, false} {
		var streamed strings.Builder
		var callback StreamCallback
		if withCallback {
			callback = func(event StreamEvent) {
				if event.Kind == StreamOutput {
					streamed.WriteString(event.Text)
				}
			}
		}
		resp, err := provider.Complete(context.Background(), Request{
			Model:    "qwen3.8-flash",
			Messages: []Message{{Role: "user", Content: "hi"}},
			Tools:    []Tool{{Name: "read", Parameters: map[string]any{"type": "object"}}},
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
		if len(resp.Message.ToolCalls) != 1 || resp.Message.ToolCalls[0].ID != "tool_1" {
			t.Fatalf("tool calls = %+v", resp.Message.ToolCalls)
		}
		if resp.Usage == nil {
			t.Fatal("usage is nil")
		}
		in, ok := resp.Usage.KnownInput()
		mustKnownAnthropic(t, "input", in, ok, 1100)
		out, ok := resp.Usage.KnownOutput()
		mustKnownAnthropic(t, "output", out, ok, 30)
		uncached, ok := resp.Usage.KnownUncached()
		mustKnownAnthropic(t, "uncached", uncached, ok, 100)
		if withoutUsage == nil {
			withoutUsage = resp.Usage
		} else {
			// Callbacks and nil callbacks give identical usage.
			if *withoutUsage.InputTokens != *resp.Usage.InputTokens ||
				*withoutUsage.OutputTokens != *resp.Usage.OutputTokens ||
				*withoutUsage.UncachedInputTokens != *resp.Usage.UncachedInputTokens ||
				*withoutUsage.CacheReadTokens != *resp.Usage.CacheReadTokens ||
				*withoutUsage.CacheWriteTokens != *resp.Usage.CacheWriteTokens {
				t.Fatalf("usage differs with callback: %+v vs %+v", withoutUsage, resp.Usage)
			}
		}
	}
}
