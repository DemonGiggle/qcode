package llm

import (
	"encoding/json"
	"testing"
)

func TestUsageNilVersusExplicitZeroRoundTrip(t *testing.T) {
	// Explicit zeros survive as reported zeros.
	explicit := NewUsage(IntPtr(0), IntPtr(0), nil, nil, nil, nil)
	if explicit == nil {
		t.Fatal("explicit zero usage normalized to absent")
	}
	data, err := json.Marshal(explicit)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Usage
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if in, ok := decoded.KnownInput(); !ok || in != 0 {
		t.Fatalf("input round-trip = %v,%v want 0,true", in, ok)
	}
	if out, ok := decoded.KnownOutput(); !ok || out != 0 {
		t.Fatalf("output round-trip = %v,%v want 0,true", out, ok)
	}

	// Unknown fields emit null, never numeric zero.
	partial := NewUsage(IntPtr(10), nil, nil, nil, nil, nil)
	data, err = json.Marshal(partial)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["completion_tokens"] != nil {
		t.Fatalf("unknown output should encode null, got %v", raw["completion_tokens"])
	}
	if raw["cache_read_tokens"] != nil {
		t.Fatalf("unknown cache read should encode null, got %v", raw["cache_read_tokens"])
	}
	var back Usage
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if _, ok := back.KnownOutput(); ok {
		t.Fatal("unknown output decoded as known")
	}
	if in, ok := back.KnownInput(); !ok || in != 10 {
		t.Fatalf("input = %v,%v", in, ok)
	}

	// Legacy numeric JSON still decodes.
	var legacy Usage
	if err := json.Unmarshal([]byte(`{"prompt_tokens":750,"completion_tokens":50}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if in, ok := legacy.KnownInput(); !ok || in != 750 {
		t.Fatalf("legacy input = %v,%v", in, ok)
	}
	if out, ok := legacy.KnownOutput(); !ok || out != 50 {
		t.Fatalf("legacy output = %v,%v", out, ok)
	}
}

func TestUsageCanonicalTotal(t *testing.T) {
	u := NewUsage(IntPtr(1000), IntPtr(100), IntPtr(800), nil, nil, IntPtr(20))
	if u == nil {
		t.Fatal("canonical usage normalized to absent")
	}
	if got := u.Total(); got != 1100 {
		t.Fatalf("total = %d, want 1100", got)
	}
	// Neither reasoning nor cache counters are added to totals.
	if in, _ := u.KnownInput(); in != 1000 {
		t.Fatalf("input = %d", in)
	}
}

func TestUsageInvalidMetadataKeepsTotals(t *testing.T) {
	// Negative values become unknown for that field.
	u := NewUsage(IntPtr(-5), IntPtr(100), IntPtr(-1), nil, nil, nil)
	if u == nil {
		t.Fatal("valid output dropped with invalid input")
	}
	if _, ok := u.KnownInput(); ok {
		t.Fatal("negative input should be unknown")
	}
	if out, ok := u.KnownOutput(); !ok || out != 100 {
		t.Fatalf("output = %v,%v", out, ok)
	}

	// Cache reads exceeding inclusive input are discarded, input kept.
	u = NewUsage(IntPtr(100), IntPtr(10), IntPtr(5000), nil, nil, nil)
	if u == nil {
		t.Fatal("valid totals dropped with inconsistent cache")
	}
	if _, ok := u.KnownCacheRead(); ok {
		t.Fatal("inconsistent cache read should be unknown")
	}
	if in, ok := u.KnownInput(); !ok || in != 100 {
		t.Fatalf("input = %v,%v", in, ok)
	}

	// Reasoning exceeding output becomes unknown.
	u = NewUsage(IntPtr(100), IntPtr(10), nil, nil, nil, IntPtr(50))
	if _, ok := u.KnownReasoning(); ok {
		t.Fatal("reasoning exceeding output should be unknown")
	}
	if out, ok := u.KnownOutput(); !ok || out != 10 {
		t.Fatalf("output = %v,%v", out, ok)
	}

	// Aggregation cannot yield negative or >100% ratios.
	var s SessionUsage
	s.Add(NewUsage(IntPtr(1000), IntPtr(100), IntPtr(800), nil, nil, IntPtr(20)))
	s.Add(NewUsage(IntPtr(-10), IntPtr(50), nil, nil, nil, nil))
	s.Add(NewUsage(IntPtr(100), IntPtr(50), IntPtr(9000), nil, nil, nil))
	s.Add(nil)
	if s.TotalTokens != 1300 {
		t.Fatalf("total = %d, want 1300", s.TotalTokens)
	}
	ratio, ok := s.CacheHitRatio()
	if !ok {
		t.Fatal("ratio unavailable")
	}
	if ratio < 0 || ratio > 1 {
		t.Fatalf("ratio out of range: %v", ratio)
	}
	if ratio != 0.8 {
		t.Fatalf("ratio = %v, want 0.8", ratio)
	}
	// Ratio sums only cover requests with both known input and cache-read.
	if s.CacheRatioRequests != 1 || s.CacheRatioReadTokens != 800 || s.CacheRatioInputTokens != 1000 {
		t.Fatalf("ratio sums = %+v", s)
	}
	if s.CacheReadTokens != 800 {
		t.Fatalf("reported cache total = %d, want 800", s.CacheReadTokens)
	}
}

func TestSessionCoverageCountsRequests(t *testing.T) {
	var s SessionUsage
	s.Add(NewUsage(IntPtr(100), IntPtr(10), IntPtr(80), nil, nil, nil))
	s.Add(NewUsage(IntPtr(200), nil, nil, nil, nil, nil))
	s.Add(nil)
	if s.Requests != 3 {
		t.Fatalf("requests = %d", s.Requests)
	}
	if s.Missing != 1 {
		t.Fatalf("missing = %d", s.Missing)
	}
	if s.InputReported != 2 || s.OutputReported != 1 || s.CacheReadReported != 1 {
		t.Fatalf("coverage = %+v", s)
	}
	if s.TotalTokens != 310 {
		t.Fatalf("total = %d, want 310 (partial subtotal)", s.TotalTokens)
	}
	if s.CacheRatioRequests != 1 {
		t.Fatalf("ratio requests = %d", s.CacheRatioRequests)
	}
}
