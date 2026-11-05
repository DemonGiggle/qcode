package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"qcode/internal/llm"
)

// openAISSEServer returns an httptest server speaking OpenAI Chat Completions
// SSE. Each response carries the next usage fixture in rotation.
func openAISSEServer(t *testing.T, usages []string, onRequest func(r *http.Request, body []byte)) (*httptest.Server, *int) {
	t.Helper()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		raw, _ := io.ReadAll(r.Body)
		if onRequest != nil {
			onRequest(r, raw)
		}
		usage := usages[(calls-1)%len(usages)]
		stream := "data: {\"choices\":[{\"delta\":{\"content\":\"synthetic reply\"}}]}\n" +
			"data: {\"choices\":[],\"usage\":" + usage + "}\n" +
			"data: [DONE]\n\n"
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, stream)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func fullUsage(input, output, cached int) string {
	return fmt.Sprintf(`{"prompt_tokens":%d,"completion_tokens":%d,"prompt_tokens_details":{"cached_tokens":%d}}`, input, output, cached)
}

func TestEveryScenarioRunsOffline(t *testing.T) {
	fixtures := []string{
		fullUsage(1000, 50, 800),                        // read
		fullUsage(1200, 60, 0),                          // write-ish/zero-read explicit
		`{"prompt_tokens":900,"completion_tokens":40}`,  // partial (no cache detail)
		`{"prompt_tokens":1100,"completion_tokens":55}`, // no usage detail at all
	}
	srv, calls := openAISSEServer(t, fixtures, nil)
	scens := allScenarios()
	runner := &benchRunner{
		scenarios: scens,
		opts: options{
			provider: "openai", model: "bench-model", variant: "test",
			reps: 1, gitRev: "test-rev", timeout: 10 * time.Second,
		},
		protocol: protocolFor("openai", "bench-model"),
		newProvider: func() (llm.Provider, error) {
			return llm.New("openai", llm.Config{BaseURL: srv.URL, APIKey: "dummy"})
		},
		now: time.Now,
	}
	var buf bytes.Buffer
	n, err := runner.run(context.Background(), &buf)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	expected := 0
	for _, s := range scens {
		expected += len(s.turns("bench-model"))
	}
	if n != expected {
		t.Fatalf("samples = %d, want %d", n, expected)
	}
	if *calls != expected {
		t.Fatalf("provider calls = %d, want %d", *calls, expected)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != expected {
		t.Fatalf("jsonl lines = %d, want %d", len(lines), expected)
	}
	seenCold := false
	for i, line := range lines {
		var s sample
		if err := json.Unmarshal([]byte(line), &s); err != nil {
			t.Fatalf("line %d: %v", i, err)
		}
		if s.SchemaVersion != schemaVersion {
			t.Fatalf("line %d schema_version = %d", i, s.SchemaVersion)
		}
		if s.Turn == 0 && s.TurnLabel != "cold" {
			t.Fatalf("line %d turn 0 label = %q, want cold", i, s.TurnLabel)
		}
		if s.Turn > 0 && s.TurnLabel != "warm" {
			t.Fatalf("line %d turn %d label = %q, want warm", i, s.Turn, s.TurnLabel)
		}
		if s.Turn == 0 {
			seenCold = true
		}
		if s.Status != "ok" {
			t.Fatalf("line %d status = %q", i, s.Status)
		}
		if s.ProviderReportedCost != nil {
			t.Fatalf("line %d provider_reported_cost must be null", i)
		}
		if s.Usage == nil {
			t.Fatalf("line %d usage is nil", i)
		}
	}
	if !seenCold {
		t.Fatal("no cold (turn 0) samples recorded")
	}
}

func TestCachedShareWeightedByInput(t *testing.T) {
	mk := func(input, read int, hasRead bool) sample {
		u := &llm.Usage{InputTokens: llm.IntPtr(input), OutputTokens: llm.IntPtr(10)}
		if hasRead {
			u.CacheReadTokens = llm.IntPtr(read)
		}
		return sample{Status: "ok", Turn: 1, Usage: llm.NormalizeUsage(u)}
	}
	// Weighted: (800 over 1000) + (0 over 100) => 800/1100, not a mean of ratios.
	samples := []sample{mk(1000, 800, true), mk(100, 0, true)}
	share, _ := cachedShare(samples)
	if share == nil {
		t.Fatal("share is nil, want weighted ratio")
	}
	want := 800.0 / 1100.0
	if diff := *share - want; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("share = %v, want %v", *share, want)
	}
	// Missing cache metrics never become zero: no eligible requests => nil share.
	missing := []sample{mk(500, 0, false)}
	share, cov := cachedShare(missing)
	if share != nil {
		t.Fatalf("share with missing metrics = %v, want nil (never zero)", *share)
	}
	if cov != 0 {
		t.Fatalf("coverage with missing metrics = %v, want 0", cov)
	}
}

func TestCompareRejectsMismatchedModelAndSettings(t *testing.T) {
	base := sample{
		SchemaVersion: 1, Scenario: "stable-long-prefix", Variant: "a",
		Provider: "openai", Model: "m1", Protocol: "chat_completions",
		Status: "ok", Turn: 1, ElapsedMs: 10,
		Usage: &llm.Usage{InputTokens: llm.IntPtr(100), OutputTokens: llm.IntPtr(5), CacheReadTokens: llm.IntPtr(50)},
	}
	other := base
	other.Model = "m2"
	if _, err := compare([]sample{base}, []sample{other}, "a", "b"); err == nil {
		t.Fatal("expected mismatch error for differing models")
	}
	other = base
	other.Settings.Thinking = "max"
	if _, err := compare([]sample{base}, []sample{other}, "a", "b"); err == nil {
		t.Fatal("expected mismatch error for differing settings")
	}
	other = base
	other.Scenario = "other-scenario"
	if _, err := compare([]sample{base}, []sample{other}, "a", "b"); err == nil {
		t.Fatal("expected mismatch error for differing scenarios")
	}
}

func TestCompareFlagsIncompleteSamples(t *testing.T) {
	ok := func(turn int) sample {
		return sample{
			SchemaVersion: 1, Scenario: "stable-long-prefix", Variant: "a",
			Provider: "openai", Model: "m1", Protocol: "chat_completions",
			Status: "ok", Turn: turn, ElapsedMs: 10,
			Usage: &llm.Usage{InputTokens: llm.IntPtr(100), OutputTokens: llm.IntPtr(5), CacheReadTokens: llm.IntPtr(50)},
		}
	}
	failed := ok(1)
	failed.Status = "error"
	failed.ErrorCategory = "http_5xx"
	failed.Usage = nil
	a := []sample{ok(0), ok(1)}
	b := []sample{ok(0), failed}
	sum, err := compare(a, b, "a", "b")
	if err != nil {
		t.Fatalf("compare: %v", err)
	}
	if len(sum.Warnings) == 0 {
		t.Fatal("expected warnings for failed samples")
	}
	if got := sum.Scenarios[0].After.Failed; got != 1 {
		t.Fatalf("after failed = %d, want 1", got)
	}
	// Failed samples are reported, never silently dropped.
	if sum.Scenarios[0].After.Samples != 2 {
		t.Fatalf("after samples = %d, want 2 (failed included)", sum.Scenarios[0].After.Samples)
	}
}

func TestOutputPrivacyNoSentinels(t *testing.T) {
	const credSentinel = "SENTINEL-CRED-77aa02"

	var bodies []string
	srv, _ := openAISSEServer(t, []string{fullUsage(100, 10, 40)}, func(r *http.Request, body []byte) {
		bodies = append(bodies, string(body))
	})

	// Build requests carrying sentinel-bearing synthetic content through the
	// real scenario path, then run and verify output.
	scens := allScenarios()
	runner := &benchRunner{
		scenarios: scens,
		opts: options{
			provider: "openai", model: "bench-model", variant: "test",
			reps: 1, gitRev: "test-rev", timeout: 10 * time.Second,
		},
		protocol: protocolFor("openai", "bench-model"),
		newProvider: func() (llm.Provider, error) {
			// Credential sentinel travels only as an Authorization header.
			return llm.New("openai", llm.Config{BaseURL: srv.URL, APIKey: credSentinel})
		},
		now: time.Now,
	}
	var buf bytes.Buffer
	if _, err := runner.run(context.Background(), &buf); err != nil {
		t.Fatalf("run: %v", err)
	}
	out := buf.String()
	// None of these may appear in output: credentials (header only),
	// model response text, replayed tool-call arguments, or prompt text.
	// Only metadata, normalized usage, and durations are recorded.
	for _, forbidden := range []string{
		credSentinel,
		"synthetic reply",            // model response text
		"aisle-01",                   // tool-call argument replayed in history
		"fictional inventory ledger", // committed synthetic prompt text
		"WO-1001",                    // synthetic prompt detail
	} {
		if strings.Contains(out, forbidden) {
			t.Fatalf("output leaks %q", forbidden)
		}
	}
	// Request bodies did carry content (proving the negative is meaningful).
	joined := strings.Join(bodies, "\n")
	if !strings.Contains(joined, "synthetic request t0") {
		t.Fatal("test setup error: requests carried no prompt content")
	}
	// Raw cache identities must never be recorded either.
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		var s sample
		if err := json.Unmarshal([]byte(line), &s); err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(s.GitRevision, "rep-") {
			t.Fatal("cache identity leaked into git_revision")
		}
	}
	// Error paths are sanitized: a failing server yields a category, never a body.
	errSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, "SENTINEL-BODY-ef0032 upstream exploded: "+credSentinel)
	}))
	defer errSrv.Close()
	errRunner := &benchRunner{
		scenarios: scens[:1],
		opts: options{
			provider: "openai", model: "bench-model", variant: "test",
			reps: 1, gitRev: "test-rev", timeout: 10 * time.Second,
		},
		protocol: protocolFor("openai", "bench-model"),
		newProvider: func() (llm.Provider, error) {
			return llm.New("openai", llm.Config{BaseURL: errSrv.URL, APIKey: credSentinel})
		},
		now: time.Now,
	}
	var ebuf bytes.Buffer
	_, _ = errRunner.run(context.Background(), &ebuf)
	for _, sentinel := range []string{"SENTINEL-BODY-ef0032", credSentinel} {
		if strings.Contains(ebuf.String(), sentinel) {
			t.Fatalf("error output leaks %q", sentinel)
		}
	}
	var first sample
	if err := json.Unmarshal([]byte(strings.Split(strings.TrimSpace(ebuf.String()), "\n")[0]), &first); err != nil {
		t.Fatal(err)
	}
	if first.Status != "error" || first.ErrorCategory == "" {
		t.Fatalf("expected sanitized error sample, got %+v", first)
	}
}

func TestDryRunMakesZeroRequests(t *testing.T) {
	srv, calls := openAISSEServer(t, []string{fullUsage(10, 5, 4)}, nil)
	_ = srv
	var stdout bytes.Buffer
	scens, err := selectScenarios("")
	if err != nil {
		t.Fatal(err)
	}
	opts := options{provider: "openai", model: "m", variant: "v", reps: 5, gitRev: "r"}
	if err := runDryRun(scens, opts, &stdout); err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if *calls != 0 {
		t.Fatalf("dry-run made %d provider requests, want 0", *calls)
	}
	if !strings.Contains(stdout.String(), "0 requests sent") {
		t.Fatalf("dry-run output missing zero-request confirmation: %q", stdout.String())
	}
	// Dry-run resolves all scenarios without contacting the endpoint even at
	// default repetition counts.
	total := 0
	for _, s := range scens {
		total += len(s.turns("m")) * opts.reps
	}
	if total == 0 {
		t.Fatal("expected nonzero planned requests")
	}
}

func TestCompareEndToEnd(t *testing.T) {
	write := func(t *testing.T, path string, samples []sample) {
		t.Helper()
		var buf bytes.Buffer
		for _, s := range samples {
			data, err := json.Marshal(s)
			if err != nil {
				t.Fatal(err)
			}
			buf.Write(data)
			buf.WriteByte('\n')
		}
		if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mk := func(variant string, read int) sample {
		return sample{
			SchemaVersion: 1, Scenario: "stable-long-prefix", Variant: variant,
			Repetition: 0, Turn: 1, TurnLabel: "warm",
			Provider: "openai", Model: "m", Protocol: "chat_completions",
			ElapsedMs: 20, Status: "ok",
			Usage: &llm.Usage{InputTokens: llm.IntPtr(1000), OutputTokens: llm.IntPtr(50), CacheReadTokens: llm.IntPtr(read)},
		}
	}
	dir := t.TempDir()
	before := filepath.Join(dir, "before.jsonl")
	after := filepath.Join(dir, "after.jsonl")
	write(t, before, []sample{mk("before", 100), mk("before", 100)})
	write(t, after, []sample{mk("after", 800), mk("after", 800)})
	var stdout, stderr bytes.Buffer
	if err := runCompare(before, after, &stdout, &stderr); err != nil {
		t.Fatalf("runCompare: %v", err)
	}
	var sum compareSummary
	if err := json.Unmarshal(stdout.Bytes(), &sum); err != nil {
		t.Fatalf("summary is not JSON: %v", err)
	}
	if len(sum.Scenarios) != 1 {
		t.Fatalf("scenarios = %d, want 1", len(sum.Scenarios))
	}
	got := sum.Scenarios[0]
	if got.CachedShareBefore == nil || got.CachedShareAfter == nil {
		t.Fatal("expected non-nil cached shares")
	}
	if *got.CachedShareAfter <= *got.CachedShareBefore {
		t.Fatalf("after share %v should exceed before %v", *got.CachedShareAfter, *got.CachedShareBefore)
	}
}
