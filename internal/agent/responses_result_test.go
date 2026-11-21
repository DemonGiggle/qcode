package agent

import (
	"context"
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

func TestPreparedRequestWiresResponsesOptInsBehindFlags(t *testing.T) {
	newAgent := func() *Agent {
		a := New(&usageProvider{}, "test", &skillToolset{}, trace.New(io.Discard, false), io.Discard, 3)
		a.messages = []llm.Message{{Role: "user", Content: "hi"}}
		return a
	}
	// Default: no opt-ins even with a last id.
	a := newAgent()
	a.recordResponsesResult(llm.Response{ResponseID: "resp_123", Usage: &llm.Usage{InputTokens: llm.IntPtr(10)}})
	t.Setenv("QCODE_RESPONSES_DIAGNOSTICS", "")
	prep := a.prepareNormalRequest(context.Background(), "")
	if prep.Request.ComparisonResponseID != "" || prep.Request.PreviousResponseID != "" || prep.Request.StoreResponses {
		t.Fatalf("default must stay stateless: %+v", prep.Request)
	}
	// Diagnostics flag reuses last id as comparison baseline.
	a = newAgent()
	a.recordResponsesResult(llm.Response{ResponseID: "resp_123", Usage: &llm.Usage{InputTokens: llm.IntPtr(10)}})
	t.Setenv("QCODE_RESPONSES_DIAGNOSTICS", "1")
	prep = a.prepareNormalRequest(context.Background(), "")
	if prep.Request.ComparisonResponseID != "resp_123" {
		t.Fatalf("diagnostics flag must set comparison: %+v", prep.Request)
	}
	if prep.Request.PreviousResponseID != "" {
		t.Fatalf("diagnostics must not imply continuation: %+v", prep.Request)
	}
	// No continuation auto-wiring: full history + previous_response_id would
	// duplicate every prior turn server-side, so delta sending is deferred
	// and the agent stays diagnostics-only.
	a = newAgent()
	a.recordResponsesResult(llm.Response{ResponseID: "resp_456", Usage: &llm.Usage{InputTokens: llm.IntPtr(10)}})
	t.Setenv("QCODE_RESPONSES_DIAGNOSTICS", "")
	prep = a.prepareNormalRequest(context.Background(), "")
	if prep.Request.PreviousResponseID != "" || prep.Request.StoreResponses {
		t.Fatalf("agent must not auto-chain previous_response_id (delta deferred): %+v", prep.Request)
	}
}

func TestFailedTurnDoesNotSeedBaseline(t *testing.T) {
	// Simulates agent.go gating: usage counted, but truncated/failed ids
	// never become the comparison baseline.
	a := New(&usageProvider{}, "test", &skillToolset{}, trace.New(io.Discard, false), io.Discard, 3)
	a.recordUsage(&llm.Usage{InputTokens: llm.IntPtr(100)})
	if got := a.LastResponseID(); got != "" {
		t.Fatalf("failed turn must not seed id, got %q", got)
	}
}
