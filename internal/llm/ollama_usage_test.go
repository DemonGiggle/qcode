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

func ollamaNDJSONServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Errorf("path = %q, want /api/chat", r.URL.Path)
		}
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func ollamaComplete(t *testing.T, srv *httptest.Server, onText StreamCallback) Response {
	t.Helper()
	provider, err := newOllama(Config{BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := provider.Complete(context.Background(), Request{
		Model:    "test",
		Messages: []Message{{Role: "user", Content: "hi"}},
	}, onText)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func mustOllamaUnknown(t *testing.T, what string, got int, ok bool) {
	t.Helper()
	mustUnknown(t, "ollama "+what, got, ok)
}

func mustOllamaKnown(t *testing.T, what string, got int, ok bool, want int) {
	t.Helper()
	mustKnown(t, "ollama "+what, got, ok, want)
}

func TestOllamaUsageCompleteFinal(t *testing.T) {
	body := strings.Join([]string{
		`{"message":{"role":"assistant","thinking":"checking"},"done":false}`,
		`{"message":{"role":"assistant","content":"hi"},"done":false}`,
		`{"message":{"role":"assistant","content":""},"done":true,"prompt_eval_count":25,"eval_count":7}`,
	}, "\n") + "\n"
	resp := ollamaComplete(t, ollamaNDJSONServer(t, body), nil)
	if resp.Message.Content != "hi" {
		t.Fatalf("content = %q, want %q", resp.Message.Content, "hi")
	}
	if resp.Message.Thinking != "checking" {
		t.Fatalf("thinking = %q, want %q", resp.Message.Thinking, "checking")
	}
	if resp.Usage == nil {
		t.Fatal("usage is nil")
	}
	in, ok := resp.Usage.KnownInput()
	mustOllamaKnown(t, "input", in, ok, 25)
	out, ok := resp.Usage.KnownOutput()
	mustOllamaKnown(t, "output", out, ok, 7)
	if got := resp.Usage.Total(); got != 32 {
		t.Fatalf("total = %d, want 32", got)
	}
	// No cache or reasoning breakdown exists on this endpoint: every
	// breakdown stays unknown (nil), never an invented zero.
	if v, ok := resp.Usage.KnownCacheRead(); ok {
		t.Fatalf("cache read = %d,true want unknown", v)
	}
	cw, cwOk := resp.Usage.KnownCacheWrite()
	mustOllamaUnknown(t, "cache write", cw, cwOk)
	unc, uncOk := resp.Usage.KnownUncached()
	mustOllamaUnknown(t, "uncached input", unc, uncOk)
	rea, reaOk := resp.Usage.KnownReasoning()
	mustOllamaUnknown(t, "reasoning", rea, reaOk)
	// Wire JSON serializes unknown breakdowns as null, never numeric zero.
	raw, err := json.Marshal(resp.Usage)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"cache_read_tokens":null`, `"cache_write_tokens":null`, `"uncached_input_tokens":null`, `"reasoning_tokens":null`} {
		if !strings.Contains(string(raw), key) {
			t.Fatalf("usage JSON = %s, want %s", raw, key)
		}
	}
}

func TestOllamaUsagePromptOnly(t *testing.T) {
	body := `{"message":{"role":"assistant","content":"hi"},"done":true,"prompt_eval_count":10}` + "\n"
	resp := ollamaComplete(t, ollamaNDJSONServer(t, body), nil)
	if resp.Usage == nil {
		t.Fatal("usage is nil")
	}
	in, ok := resp.Usage.KnownInput()
	mustOllamaKnown(t, "input", in, ok, 10)
	out, outOk := resp.Usage.KnownOutput()
	mustOllamaUnknown(t, "output", out, outOk)
}

func TestOllamaUsageOutputOnly(t *testing.T) {
	// Regression: output-only reports were dropped by the prompt-gated
	// parser. Presence is independent per field.
	body := `{"message":{"role":"assistant","content":"hi"},"done":true,"eval_count":9}` + "\n"
	resp := ollamaComplete(t, ollamaNDJSONServer(t, body), nil)
	if resp.Usage == nil {
		t.Fatal("usage is nil, want output-only usage retained")
	}
	out, ok := resp.Usage.KnownOutput()
	mustOllamaKnown(t, "output", out, ok, 9)
	in, inOk := resp.Usage.KnownInput()
	mustOllamaUnknown(t, "input", in, inOk)
}

func TestOllamaUsageExplicitZero(t *testing.T) {
	body := `{"message":{"role":"assistant","content":""},"done":true,"prompt_eval_count":0,"eval_count":0}` + "\n"
	resp := ollamaComplete(t, ollamaNDJSONServer(t, body), nil)
	if resp.Usage == nil {
		t.Fatal("usage is nil, want explicit zeros retained")
	}
	in, ok := resp.Usage.KnownInput()
	mustOllamaKnown(t, "input", in, ok, 0)
	out, ok := resp.Usage.KnownOutput()
	mustOllamaKnown(t, "output", out, ok, 0)
}

func TestOllamaUsageAbsentFields(t *testing.T) {
	body := strings.Join([]string{
		`{"message":{"role":"assistant","content":"hi"},"done":false}`,
		`{"message":{"role":"assistant","content":""},"done":true}`,
	}, "\n") + "\n"
	resp := ollamaComplete(t, ollamaNDJSONServer(t, body), nil)
	if resp.Usage != nil {
		t.Fatalf("usage = %+v, want nil for absent fields", resp.Usage)
	}
	if resp.Message.Content != "hi" {
		t.Fatalf("content = %q, want %q", resp.Message.Content, "hi")
	}
}

func TestOllamaUsageRepeatedCumulative(t *testing.T) {
	// Intermediate lines carry cumulative snapshots: the final line omits
	// eval_count, so the earlier output value survives; values replace and
	// are never summed (10+20 would be 30, want latest 20).
	body := strings.Join([]string{
		`{"message":{"role":"assistant","content":"a"},"done":false,"prompt_eval_count":10,"eval_count":5}`,
		`{"message":{"role":"assistant","content":"b"},"done":false,"prompt_eval_count":20,"eval_count":12}`,
		`{"message":{"role":"assistant","content":""},"done":true,"prompt_eval_count":20}`,
	}, "\n") + "\n"
	resp := ollamaComplete(t, ollamaNDJSONServer(t, body), nil)
	if resp.Usage == nil {
		t.Fatal("usage is nil")
	}
	in, ok := resp.Usage.KnownInput()
	mustOllamaKnown(t, "input", in, ok, 20)
	out, ok := resp.Usage.KnownOutput()
	mustOllamaKnown(t, "output", out, ok, 12)
	if resp.Message.Content != "ab" {
		t.Fatalf("content = %q, want %q", resp.Message.Content, "ab")
	}
}

func TestOllamaUsageExplicitZeroReplaces(t *testing.T) {
	body := strings.Join([]string{
		`{"message":{"role":"assistant","content":"a"},"done":false,"prompt_eval_count":20,"eval_count":12}`,
		`{"message":{"role":"assistant","content":""},"done":true,"prompt_eval_count":0,"eval_count":0}`,
	}, "\n") + "\n"
	resp := ollamaComplete(t, ollamaNDJSONServer(t, body), nil)
	if resp.Usage == nil {
		t.Fatal("usage is nil")
	}
	in, ok := resp.Usage.KnownInput()
	mustOllamaKnown(t, "input", in, ok, 0)
	out, ok := resp.Usage.KnownOutput()
	mustOllamaKnown(t, "output", out, ok, 0)
}

func TestOllamaUsageWithCallback(t *testing.T) {
	body := strings.Join([]string{
		`{"message":{"role":"assistant","thinking":"checking"},"done":false}`,
		`{"message":{"role":"assistant","content":"hi"},"done":false}`,
		`{"message":{"role":"assistant","tool_calls":[{"function":{"name":"list","arguments":{"path":"."}}}]},"done":false}`,
		`{"message":{"role":"assistant","content":""},"done":true,"prompt_eval_count":25,"eval_count":7}`,
	}, "\n") + "\n"
	var streamed strings.Builder
	resp := ollamaComplete(t, ollamaNDJSONServer(t, body), func(event StreamEvent) {
		streamed.WriteString(event.Text)
	})
	if streamed.String() != "checkinghi" {
		t.Fatalf("streamed = %q, want %q", streamed.String(), "checkinghi")
	}
	if resp.Usage == nil {
		t.Fatal("usage is nil with callback")
	}
	in, ok := resp.Usage.KnownInput()
	mustOllamaKnown(t, "input", in, ok, 25)
	out, ok := resp.Usage.KnownOutput()
	mustOllamaKnown(t, "output", out, ok, 7)
	if len(resp.Message.ToolCalls) != 1 || resp.Message.ToolCalls[0].Name != "list" {
		t.Fatalf("tool calls = %#v, want one list call", resp.Message.ToolCalls)
	}
	if resp.Message.Thinking != "checking" || resp.Message.Content != "hi" {
		t.Fatalf("message = %#v", resp.Message)
	}
}

func TestOllamaUsageNilCallbackParsesUsage(t *testing.T) {
	body := strings.Join([]string{
		`{"message":{"role":"assistant","content":"hi"},"done":false}`,
		`{"message":{"role":"assistant","content":""},"done":true,"prompt_eval_count":25,"eval_count":7}`,
	}, "\n") + "\n"
	resp := ollamaComplete(t, ollamaNDJSONServer(t, body), nil)
	if resp.Message.Content != "hi" {
		t.Fatalf("content = %q, want %q", resp.Message.Content, "hi")
	}
	if resp.Usage == nil {
		t.Fatal("usage is nil with nil callback")
	}
	in, ok := resp.Usage.KnownInput()
	mustOllamaKnown(t, "input", in, ok, 25)
	out, ok := resp.Usage.KnownOutput()
	mustOllamaKnown(t, "output", out, ok, 7)
}

func TestOllamaUsageNullFieldsKeepEarlier(t *testing.T) {
	body := strings.Join([]string{
		`{"message":{"role":"assistant","content":"a"},"done":false,"prompt_eval_count":25,"eval_count":7}`,
		`{"message":{"role":"assistant","content":""},"done":true,"prompt_eval_count":null,"eval_count":null}`,
	}, "\n") + "\n"
	resp := ollamaComplete(t, ollamaNDJSONServer(t, body), nil)
	if resp.Usage == nil {
		t.Fatal("usage is nil")
	}
	in, ok := resp.Usage.KnownInput()
	mustOllamaKnown(t, "input", in, ok, 25)
	out, ok := resp.Usage.KnownOutput()
	mustOllamaKnown(t, "output", out, ok, 7)
}

func TestOllamaUsageMalformedMetadataKeepsText(t *testing.T) {
	body := `{"message":{"role":"assistant","content":"hi"},"done":true,"prompt_eval_count":"lots"}` + "\n"
	resp := ollamaComplete(t, ollamaNDJSONServer(t, body), nil)
	if resp.Message.Content != "hi" {
		t.Fatalf("content = %q, want %q", resp.Message.Content, "hi")
	}
	if resp.Usage != nil {
		t.Fatalf("usage = %+v, want nil for malformed metadata", resp.Usage)
	}
}

func TestOllamaUsageNegativeBecomesUnknown(t *testing.T) {
	body := `{"message":{"role":"assistant","content":"hi"},"done":true,"prompt_eval_count":-3,"eval_count":7}` + "\n"
	resp := ollamaComplete(t, ollamaNDJSONServer(t, body), nil)
	if resp.Usage == nil {
		t.Fatal("usage is nil, want output retained")
	}
	in, inOk := resp.Usage.KnownInput()
	mustOllamaUnknown(t, "input", in, inOk)
	out, ok := resp.Usage.KnownOutput()
	mustOllamaKnown(t, "output", out, ok, 7)
}
