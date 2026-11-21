package trace

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"qcode/internal/llm"
)

func TestCacheDiagnosticsEvent(t *testing.T) {
	var output bytes.Buffer
	logger := New(&output, true)
	diag := &llm.ResponsesCacheDiagnostics{
		Type: "cache_miss", Reason: "tools_changed",
		ComparisonReusableTokens: llm.IntPtr(5629), CacheMissedTokens: llm.IntPtr(5629),
	}
	logger.CacheDiagnostics("chatgpt", "gpt-6-astra", "resp_123", llm.CacheComparisonRequested, diag)
	raw := strings.TrimSpace(output.String())
	if raw == "" {
		t.Fatal("expected diagnostics event")
	}
	var event map[string]any
	if err := json.Unmarshal([]byte(raw), &event); err != nil {
		t.Fatal(err)
	}
	if event["event"] != "cache_diagnostics" || event["type"] != "cache_miss" || event["request_status"] != "requested" {
		t.Fatalf("event = %#v", event)
	}
	if event["reason"] != "tools_changed" || event["response_id"] != "resp_123" {
		t.Fatalf("fields = %#v", event)
	}
	if !strings.Contains(strings.ToLower(raw), "tool") {
		t.Fatalf("suggested fix missing: %s", raw)
	}
	lowered := strings.ToLower(raw)
	for _, forbidden := range []string{"prompt_cache_key", "authorization", "credential", "cost", "price", "billing", "payload"} {
		if strings.Contains(lowered, forbidden) {
			t.Fatalf("diagnostics event contains %q: %s", forbidden, raw)
		}
	}
}

func TestCacheDiagnosticsNilEmitsNothing(t *testing.T) {
	var output bytes.Buffer
	logger := New(&output, true)
	logger.CacheDiagnostics("chatgpt", "gpt-6-astra", "", "", nil)
	if strings.TrimSpace(output.String()) != "" {
		t.Fatalf("nil diagnostics must emit nothing: %q", output.String())
	}
}

func TestCacheDiagnosticsTraceWhenProviderOmitsReport(t *testing.T) {
	var output bytes.Buffer
	logger := New(&output, true)
	logger.CacheDiagnostics("chatgpt", "model", "resp_1", llm.CacheComparisonRequested, nil)
	var event map[string]any
	if err := json.Unmarshal(output.Bytes(), &event); err != nil {
		t.Fatal(err)
	}
	if event["request_status"] != "requested" || event["type"] != nil || event["reason"] != nil {
		t.Fatalf("unexpected comparison metadata: %v", event)
	}
}

func TestCacheDiagnosticsVerboseLine(t *testing.T) {
	var output bytes.Buffer
	logger := New(&output, false)
	logger.SetVerbose(true)
	diag := &llm.ResponsesCacheDiagnostics{Type: "cache_miss", Reason: "tools_changed"}
	logger.CacheDiagnostics("chatgpt", "gpt-6-astra", "resp_1", llm.CacheComparisonRequested, diag)
	got := strings.TrimSpace(output.String())
	if !strings.Contains(got, "cache_miss") || !strings.Contains(got, "tools_changed") {
		t.Fatalf("verbose line missing: %q", got)
	}
	var quiet bytes.Buffer
	quietLogger := New(&quiet, false)
	quietLogger.SetVerbose(false)
	quietLogger.CacheDiagnostics("chatgpt", "gpt-6-astra", "resp_1", llm.CacheComparisonRequested, diag)
	if strings.TrimSpace(quiet.String()) != "" {
		t.Fatalf("non-verbose must stay silent: %q", quiet.String())
	}
}
