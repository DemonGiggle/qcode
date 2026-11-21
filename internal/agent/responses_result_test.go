package agent

import (
	"io"
	"testing"

	"qcode/internal/llm"
	"qcode/internal/trace"
)

func TestRecordResponsesResultStoresIDAndDiagnostics(t *testing.T) {
	a := New(&usageProvider{}, "test", &skillToolset{}, trace.New(io.Discard, false), io.Discard, 3)
	if got := a.LastResponseID(); got != "" {
		t.Fatalf("initial id = %q", got)
	}
	if got := a.LastDiagnostics(); got != nil {
		t.Fatalf("initial diagnostics = %+v", got)
	}
	a.recordResponsesResult(llm.Response{
		Usage:      &llm.Usage{InputTokens: llm.IntPtr(100)},
		ResponseID: "resp_123",
		CacheDiagnostics: &llm.ResponsesCacheDiagnostics{
			Type: "cache_miss", Reason: "tools_changed",
			CacheMissedTokens: llm.IntPtr(10),
		},
	})
	if got := a.LastResponseID(); got != "resp_123" {
		t.Fatalf("id = %q", got)
	}
	diag := a.LastDiagnostics()
	if diag == nil || diag.Type != "cache_miss" || diag.Reason != "tools_changed" {
		t.Fatalf("diagnostics = %+v", diag)
	}
	// A result without Responses metadata clears the previous id/diagnostics
	// so callers never chain from a stale baseline.
	a.recordResponsesResult(llm.Response{Usage: &llm.Usage{InputTokens: llm.IntPtr(10)}})
	if got := a.LastResponseID(); got != "" {
		t.Fatalf("empty result must clear id, got %q", got)
	}
	if got := a.LastDiagnostics(); got != nil {
		t.Fatalf("empty result must clear diagnostics, got %+v", got)
	}
}
