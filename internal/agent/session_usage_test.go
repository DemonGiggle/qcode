package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"

	"qcode/internal/llm"
	"qcode/internal/trace"
)

// Issue #77: session usage aggregation, persistence versioning, and legacy
// migration. Per-request Usage comes normalized from #74-#76; this package
// only aggregates it faithfully.

func TestSessionUsageAggregationContract(t *testing.T) {
	a := New(&usageProvider{}, "test", &skillToolset{}, trace.New(io.Discard, false), io.Discard, 3)

	a.recordUsage(llm.NewUsage(llm.IntPtr(1000), llm.IntPtr(100), llm.IntPtr(800), nil, nil, nil))
	a.recordUsage(llm.NewUsage(llm.IntPtr(500), nil, nil, nil, nil, nil))
	a.recordUsage(nil)

	got := a.SessionUsage()
	if got.InputTokens != 1500 || got.OutputTokens != 100 || got.TotalTokens != 1600 {
		t.Fatalf("sums = %+v", got)
	}
	if got.Requests != 3 {
		t.Fatalf("requests = %+v", got)
	}
	if got.InputReported != 2 || got.OutputReported != 1 {
		t.Fatalf("coverage = %+v", got)
	}
	if got.Missing != 1 {
		t.Fatalf("missing = %+v", got)
	}
	if got.CacheReadReported != 1 || got.CacheReadTokens != 800 {
		t.Fatalf("cache read = %+v", got)
	}
	if got.CacheRatioRequests != 1 || got.CacheRatioInputTokens != 1000 || got.CacheRatioReadTokens != 800 {
		t.Fatalf("ratio = %+v", got)
	}
	if ratio, ok := got.CacheHitRatio(); !ok || ratio != 0.8 {
		t.Fatalf("hit ratio = %v %v", ratio, ok)
	}
}

func TestSessionUsageCacheReadOnlySkipsRatio(t *testing.T) {
	a := New(&usageProvider{}, "test", &skillToolset{}, trace.New(io.Discard, false), io.Discard, 3)

	// Cache-read-only: no usable input, so coverage grows but the ratio
	// denominator must not.
	a.recordUsage(llm.NewUsage(nil, nil, llm.IntPtr(50), nil, nil, nil))
	got := a.SessionUsage()
	if got.Requests != 1 || got.CacheReadReported != 1 || got.CacheReadTokens != 50 {
		t.Fatalf("cache-only = %+v", got)
	}
	if got.CacheRatioRequests != 0 || got.CacheRatioInputTokens != 0 || got.CacheRatioReadTokens != 0 {
		t.Fatalf("ratio must stay empty: %+v", got)
	}
	if _, ok := got.CacheHitRatio(); ok {
		t.Fatal("ratio must be unavailable")
	}
}

func TestSessionUsageSingleAggregationPerComplete(t *testing.T) {
	p := &snapshotProvider{}
	a := New(p, "test", &skillToolset{}, trace.New(io.Discard, false), io.Discard, 3)
	if err := a.Run(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	if p.snapshots != 3 {
		t.Fatalf("snapshots = %d", p.snapshots)
	}
	got := a.SessionUsage()
	if got.Requests != 1 || got.InputReported != 1 || got.OutputReported != 1 {
		t.Fatalf("one Complete must aggregate once: %+v", got)
	}
	if got.InputTokens != 1000 || got.OutputTokens != 100 {
		t.Fatalf("sums = %+v", got)
	}
}

func TestSessionUsageFailedCallsCountMissing(t *testing.T) {
	a := New(&usageProvider{}, "test", &skillToolset{}, trace.New(io.Discard, false), io.Discard, 3)

	// Failed call with no usable usage.
	a.recordUsage(nil)
	// Failed call that still carried valid usage.
	a.recordUsage(&llm.Usage{InputTokens: llm.IntPtr(20), OutputTokens: llm.IntPtr(5)})
	got := a.SessionUsage()
	if got.Requests != 2 || got.Missing != 1 {
		t.Fatalf("failed calls = %+v", got)
	}
	if got.InputTokens != 20 || got.OutputTokens != 5 {
		t.Fatalf("sums = %+v", got)
	}
}

func TestSessionUsageRoundTripPreservesVersionedState(t *testing.T) {
	a := New(&usageProvider{}, "model", &managerToolset{}, trace.New(io.Discard, false), io.Discard, 4)
	a.recordUsage(llm.NewUsage(llm.IntPtr(1000), llm.IntPtr(100), llm.IntPtr(800), nil, nil, nil))
	a.recordUsage(nil)
	before := a.SessionUsage()
	data := *a.checkpoint.Load()

	var saved SavedState
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.UsageVersion != llm.UsageSchemaVersion {
		t.Fatalf("usage version = %d", saved.UsageVersion)
	}
	if saved.Usage != before {
		t.Fatalf("checkpoint usage = %+v, live = %+v", saved.Usage, before)
	}

	restored := New(&usageProvider{}, "model", &managerToolset{}, trace.New(io.Discard, false), io.Discard, 4)
	if err := restored.RestoreState(data); err != nil {
		t.Fatal(err)
	}
	if restored.SessionUsage() != before {
		t.Fatalf("restored = %+v, want %+v", restored.SessionUsage(), before)
	}
	if restored.SessionUsage().LegacyUncertain {
		t.Fatal("versioned restore must not mark uncertainty")
	}
	// Continue accumulating after a versioned restore.
	restored.recordUsage(llm.NewUsage(llm.IntPtr(10), nil, nil, nil, nil, nil))
	if got := restored.SessionUsage(); got.InputTokens != before.InputTokens+10 || got.Requests != before.Requests+1 {
		t.Fatalf("post-restore accumulation = %+v", got)
	}
	if restored.SessionUsage().LegacyUncertain {
		t.Fatal("versioned accumulation must stay certain")
	}
}

func TestSessionUsageLegacyMigration(t *testing.T) {
	mkAgent := func() *Agent {
		return New(&usageProvider{}, "model", &managerToolset{}, trace.New(io.Discard, false), io.Discard, 4)
	}
	// Legacy nonzero state: no usage_version, old numeric totals, context usage.
	legacy := SavedState{
		Provider: "usage", Model: "model", MaxSteps: 4, Remaining: 100,
		Messages:     []SavedMessage{{Message: llm.Message{Role: "system", Content: "x"}}},
		Usage:        llm.SessionUsage{InputTokens: 500, OutputTokens: 80, TotalTokens: 580, Missing: 1},
		ContextUsage: &llm.Usage{InputTokens: llm.IntPtr(300), OutputTokens: llm.IntPtr(50)},
	}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	a := mkAgent()
	if err := a.RestoreState(data); err != nil {
		t.Fatal(err)
	}
	got := a.SessionUsage()
	if got.InputTokens != 500 || got.OutputTokens != 80 || got.TotalTokens != 580 || got.Missing != 1 {
		t.Fatalf("legacy totals lost: %+v", got)
	}
	if !got.LegacyUncertain {
		t.Fatal("legacy nonzero restore must mark uncertainty")
	}
	if got.InputReported != 0 || got.OutputReported != 0 || got.CacheReadReported != 0 || got.CacheWriteReported != 0 {
		t.Fatalf("legacy coverage must start at zero: %+v", got)
	}
	if got.CacheReadTokens != 0 || got.CacheWriteTokens != 0 || got.CacheRatioRequests != 0 {
		t.Fatalf("legacy cache sums must start at zero: %+v", got)
	}
	if a.contextUsage != nil {
		t.Fatal("legacy context usage must be invalidated")
	}
	// New requests accumulate normally; uncertainty remains until /new.
	a.recordUsage(llm.NewUsage(llm.IntPtr(10), llm.IntPtr(5), nil, nil, nil, nil))
	got = a.SessionUsage()
	if got.InputTokens != 510 || got.TotalTokens != 595 || got.Requests != 1 {
		t.Fatalf("legacy accumulation = %+v", got)
	}
	if !got.LegacyUncertain {
		t.Fatal("uncertainty must remain after accumulation")
	}
	// /new resets every sum/counter/flag.
	a.ResetSession()
	if a.SessionUsage() != (llm.SessionUsage{}) {
		t.Fatalf("reset retained usage: %+v", a.SessionUsage())
	}

	// Legacy zero state stays clean.
	empty := SavedState{
		Provider: "usage", Model: "model", MaxSteps: 4, Remaining: 100,
		Messages: []SavedMessage{{Message: llm.Message{Role: "system", Content: "x"}}},
	}
	emptyData, err := json.Marshal(empty)
	if err != nil {
		t.Fatal(err)
	}
	b := mkAgent()
	if err := b.RestoreState(emptyData); err != nil {
		t.Fatal(err)
	}
	if b.SessionUsage() != (llm.SessionUsage{}) {
		t.Fatalf("legacy zero = %+v", b.SessionUsage())
	}
}

func TestSessionUsageCompactionPreservesTotals(t *testing.T) {
	p := &compactingProvider{}
	a := NewWithSystem(p, "test", &skillToolset{}, trace.New(io.Discard, false), io.Discard, 3, "system instructions")
	a.recordUsage(llm.NewUsage(llm.IntPtr(1000), llm.IntPtr(100), nil, nil, nil, nil))
	before := a.SessionUsage()
	a.messages = append(a.messages,
		llm.Message{Role: "user", Content: "old decision"},
		llm.Message{Role: "assistant", Content: "old response"},
	)
	if _, err := a.Compact(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := a.SessionUsage()
	if got.InputTokens != before.InputTokens || got.OutputTokens != before.OutputTokens {
		t.Fatalf("compaction lost totals: %+v was %+v", got, before)
	}
	if got.Requests != before.Requests+1 {
		t.Fatalf("compaction must aggregate its own call: %+v was %+v", got, before)
	}
}

func TestSessionUsageModelSwitchPreservesAndAgentsIsolated(t *testing.T) {
	a := New(&usageProvider{}, "test", &skillToolset{}, trace.New(io.Discard, false), io.Discard, 3)
	a.recordUsage(llm.NewUsage(llm.IntPtr(1000), llm.IntPtr(100), nil, nil, nil, nil))
	before := a.SessionUsage()
	a.SetModel("other")
	if a.SessionUsage() != before {
		t.Fatal("model switch lost session totals")
	}
	b := New(&usageProvider{}, "other", &skillToolset{}, trace.New(io.Discard, false), io.Discard, 3)
	if b.SessionUsage() != (llm.SessionUsage{}) {
		t.Fatal("new agent inherited usage")
	}
}

func TestSessionUsageTraceKeepsProviderModelIdentity(t *testing.T) {
	var out bytes.Buffer
	logger := trace.New(&out, true)
	logger.SetVerbose(false)
	a := New(&usageProvider{}, "trace-model", &skillToolset{}, logger, io.Discard, 3)
	a.recordUsage(llm.NewUsage(llm.IntPtr(10), llm.IntPtr(5), nil, nil, nil, nil))
	line := strings.TrimSpace(out.String())
	var event struct {
		Event   string           `json:"event"`
		Name    string           `json:"name"`
		Model   string           `json:"model"`
		Session llm.SessionUsage `json:"session_usage"`
	}
	if err := json.Unmarshal([]byte(line), &event); err != nil {
		t.Fatal(line)
	}
	// Cross-model aggregates must not be misrepresented as model-specific
	// results: every usage event carries its own provider/model identity.
	if event.Event != "usage" || event.Name != "usage" || event.Model != "trace-model" {
		t.Fatalf("usage event lost identity: %s", line)
	}
	if event.Session.InputTokens != 10 {
		t.Fatalf("session snapshot = %+v", event.Session)
	}
	_ = a
}

func TestSessionUsageConcurrentReads(t *testing.T) {
	a := New(&usageProvider{}, "test", &skillToolset{}, trace.New(io.Discard, false), io.Discard, 3)
	a.recordUsage(llm.NewUsage(llm.IntPtr(100), llm.IntPtr(50), llm.IntPtr(20), nil, nil, nil))
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = a.SessionUsage()
			}
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			a.recordUsage(llm.NewUsage(llm.IntPtr(1), llm.IntPtr(1), nil, nil, nil, nil))
		}()
	}
	wg.Wait()
}

// snapshotProvider emits several stream snapshots but returns one usage.
type snapshotProvider struct{ snapshots int }

func (*snapshotProvider) Name() string { return "snapshot" }
func (p *snapshotProvider) Complete(_ context.Context, _ llm.Request, cb llm.StreamCallback) (llm.Response, error) {
	for _, text := range []string{"a", "b", "c"} {
		p.snapshots++
		cb(llm.StreamEvent{Kind: llm.StreamOutput, Text: text})
	}
	return llm.Response{
		Message: llm.Message{Role: "assistant", Content: "abc"},
		Usage:   &llm.Usage{InputTokens: llm.IntPtr(1000), OutputTokens: llm.IntPtr(100)},
	}, nil
}
