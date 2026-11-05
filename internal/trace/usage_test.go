package trace

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"qcode/internal/llm"
)

func decodeUsageEvent(t *testing.T, raw string) map[string]any {
	t.Helper()
	var event map[string]any
	if err := json.Unmarshal([]byte(raw), &event); err != nil {
		t.Fatalf("decode usage event: %v\n%s", err, raw)
	}
	return event
}

func TestUsageEventSchemaVersionAndCoverage(t *testing.T) {
	var output bytes.Buffer
	logger := New(&output, true)
	usage := llm.NewUsage(llm.IntPtr(1000), llm.IntPtr(100), llm.IntPtr(800), llm.IntPtr(50), nil, nil)
	var session llm.SessionUsage
	session.Add(usage)
	session.Add(nil)
	logger.Usage("openai", "gpt-test", usage, session)

	event := decodeUsageEvent(t, strings.TrimSpace(output.String()))
	if event["event"] != "usage" || event["kind"] != "llm" || event["name"] != "openai" {
		t.Fatalf("identity = %#v", event)
	}
	if event["model"] != "gpt-test" {
		t.Fatalf("model = %#v", event["model"])
	}
	if version, ok := event["usage_schema_version"].(float64); !ok || version != 2 {
		t.Fatalf("schema version = %#v", event["usage_schema_version"])
	}
	usageRaw, ok := event["usage"].(map[string]any)
	if !ok {
		t.Fatalf("usage = %#v", event["usage"])
	}
	if usageRaw["prompt_tokens"] != float64(1000) || usageRaw["completion_tokens"] != float64(100) {
		t.Fatalf("numeric tokens = %#v", usageRaw)
	}
	if usageRaw["cache_read_tokens"] != float64(800) || usageRaw["cache_write_tokens"] != float64(50) {
		t.Fatalf("cache tokens = %#v", usageRaw)
	}
	if _, ok := usageRaw["uncached_input_tokens"]; !ok {
		t.Fatalf("optional uncached field missing: %#v", usageRaw)
	}
	sessionRaw, ok := event["session_usage"].(map[string]any)
	if !ok {
		t.Fatalf("session_usage = %#v", event["session_usage"])
	}
	for _, key := range []string{
		"input_tokens", "output_tokens", "total_tokens",
		"requests", "input_reported", "output_reported",
		"cache_read_reported", "cache_write_reported",
		"cache_read_tokens", "cache_write_tokens",
		"missing_completions", "legacy_uncertain",
	} {
		if _, ok := sessionRaw[key]; !ok {
			t.Fatalf("session coverage %q missing: %#v", key, sessionRaw)
		}
	}
	if sessionRaw["requests"] != float64(2) || sessionRaw["missing_completions"] != float64(1) {
		t.Fatalf("coverage = %#v", sessionRaw)
	}
}

func TestUsageEventNullVersusZero(t *testing.T) {
	var output bytes.Buffer
	logger := New(&output, true)
	explicitZero := llm.NewUsage(llm.IntPtr(0), nil, nil, nil, nil, nil)
	var session llm.SessionUsage
	session.Add(explicitZero)
	logger.Usage("ollama", "qwen", explicitZero, session)

	event := decodeUsageEvent(t, strings.TrimSpace(output.String()))
	usageRaw := event["usage"].(map[string]any)
	if usageRaw["prompt_tokens"] != float64(0) {
		t.Fatalf("explicit zero = %#v", usageRaw["prompt_tokens"])
	}
	if value, ok := usageRaw["completion_tokens"]; !ok || value != nil {
		t.Fatalf("missing output must be null, got %#v", usageRaw["completion_tokens"])
	}
	// Ollama-style cache absence stays null, never a fabricated zero.
	if value, ok := usageRaw["cache_read_tokens"]; !ok || value != nil {
		t.Fatalf("missing cache read must be null, got %#v", usageRaw["cache_read_tokens"])
	}
	sessionRaw := event["session_usage"].(map[string]any)
	if sessionRaw["input_tokens"] != float64(0) {
		t.Fatalf("session explicit zero = %#v", sessionRaw["input_tokens"])
	}
}

func TestUsageEventOmitsSensitiveFields(t *testing.T) {
	var output bytes.Buffer
	logger := New(&output, true)
	usage := llm.NewUsage(llm.IntPtr(10), llm.IntPtr(5), llm.IntPtr(4), nil, nil, nil)
	var session llm.SessionUsage
	session.Add(usage)
	logger.Usage("openai", "gpt-test", usage, session)

	raw := strings.TrimSpace(output.String())
	lowered := strings.ToLower(raw)
	for _, forbidden := range []string{
		"cache_key", "prompt_cache_key", "api_key", "authorization",
		"credential", "cost", "price", "billing", "payload",
	} {
		if strings.Contains(lowered, forbidden) {
			t.Fatalf("usage event contains %q:\n%s", forbidden, raw)
		}
	}
	event := decodeUsageEvent(t, raw)
	var walk func(value any)
	walk = func(value any) {
		switch typed := value.(type) {
		case map[string]any:
			for key, child := range typed {
				lower := strings.ToLower(key)
				if lower == "prompt" || lower == "content" || lower == "text" ||
					lower == "raw" || lower == "credential" || lower == "cost" {
					t.Fatalf("sensitive key %q present", key)
				}
				walk(child)
			}
		case []any:
			for _, child := range typed {
				walk(child)
			}
		}
	}
	walk(event)
}
