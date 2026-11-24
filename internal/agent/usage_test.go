package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"qcode/internal/llm"
	"qcode/internal/trace"
)

func TestSessionUsageAccumulationIsolationAndReset(t *testing.T) {
	a := New(&usageProvider{}, "test", &skillToolset{}, trace.New(io.Discard, false), io.Discard, 3)
	for range 2 {
		if err := a.Run(context.Background(), "hello"); err != nil {
			t.Fatal(err)
		}
	}
	want := llm.SessionUsage{InputTokens: 1400, OutputTokens: 100, TotalTokens: 1500, Requests: 2, InputReported: 2, OutputReported: 2}
	if got := a.SessionUsage(); got != want {
		t.Fatalf("usage = %+v", got)
	}
	a.SetModel("other")
	if a.SessionUsage() != want {
		t.Fatal("model change lost session totals")
	}
	b := New(&usageProvider{}, "other", &skillToolset{}, trace.New(io.Discard, false), io.Discard, 3)
	if b.SessionUsage() != (llm.SessionUsage{}) {
		t.Fatal("new agent inherited usage")
	}
	a.recordUsage(nil, nil)
	want.Missing = 1
	want.Requests = 3
	if a.SessionUsage() != want {
		t.Fatal("missing completion lost known totals")
	}
	a.recordUsage(&llm.Usage{InputTokens: llm.IntPtr(10), OutputTokens: llm.IntPtr(5)}, nil)
	if got := a.SessionUsage(); got.Missing != 1 || got.TotalTokens != 1515 {
		t.Fatal(got)
	}
	a.ResetSession()
	if a.SessionUsage() != (llm.SessionUsage{}) {
		t.Fatal("reset retained usage")
	}
}

func TestSessionUsageJSONAndCompaction(t *testing.T) {
	var out bytes.Buffer
	logger := trace.New(&out, true)
	logger.SetVerbose(false)
	a := New(&usageProvider{}, "test", &skillToolset{}, logger, io.Discard, 3)
	a.messages = append(a.messages, llm.Message{Role: "user", Content: "hello"})
	if _, err := a.Compact(context.Background()); err != nil {
		t.Fatal(err)
	}
	a.recordUsage(nil, nil)
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatal(out.String())
	}
	for i, line := range lines {
		var event struct {
			Event   string           `json:"event"`
			Usage   *llm.Usage       `json:"usage"`
			Session llm.SessionUsage `json:"session_usage"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		if event.Event != "usage" || event.Session.TotalTokens != 750 || event.Session.Missing != i {
			t.Fatal(line)
		}
		if (event.Usage == nil) != (i == 1) {
			t.Fatal(line)
		}
	}
}

type slowToolToolset struct {
	delay time.Duration
}

func (*slowToolToolset) Schemas() []llm.Tool        { return nil }
func (*slowToolToolset) EnabledSchemas() []llm.Tool { return nil }

func (s *slowToolToolset) ExecuteDetailed(ctx context.Context, _ llm.ToolCall) (llm.ToolResult, error) {
	select {
	case <-time.After(s.delay):
		return llm.ToolResult{Output: "ok"}, nil
	case <-ctx.Done():
		return llm.ToolResult{}, ctx.Err()
	}
}

type outputRateProvider struct {
	calls int
}

func (*outputRateProvider) Name() string { return "output-rate" }

func (p *outputRateProvider) Complete(_ context.Context, _ llm.Request, _ llm.StreamCallback) (llm.Response, error) {
	p.calls++
	time.Sleep(5 * time.Millisecond)
	if p.calls == 1 {
		return llm.Response{
			Message: llm.Message{
				Role:      "assistant",
				ToolCalls: []llm.ToolCall{{ID: "call-1", Name: "slow_tool", Arguments: json.RawMessage(`{}`)}},
			},
			Usage: llm.NewUsage(llm.IntPtr(100_000), llm.IntPtr(40), llm.IntPtr(95_000), nil, nil, llm.IntPtr(15)),
		}, nil
	}
	return llm.Response{
		Message: llm.Message{Role: "assistant", Content: "done"},
		Usage:   llm.NewUsage(nil, llm.IntPtr(60), nil, nil, nil, nil),
	}, nil
}

func TestRunWithUsageCountsOutputTokensAndExcludesToolTime(t *testing.T) {
	toolDelay := 80 * time.Millisecond
	a := New(&outputRateProvider{}, "test", &slowToolToolset{delay: toolDelay}, trace.New(io.Discard, false), io.Discard, 4)

	started := time.Now()
	outputTokens, modelDuration, err := a.RunWithUsage(context.Background(), "run slow tool")
	totalElapsed := time.Since(started)
	if err != nil {
		t.Fatal(err)
	}
	if outputTokens == nil || *outputTokens != 100 {
		t.Fatalf("outputTokens = %v, want 100", outputTokens)
	}
	if modelDuration <= 0 {
		t.Fatalf("modelDuration = %v, want > 0", modelDuration)
	}
	if modelDuration >= totalElapsed-toolDelay/2 {
		t.Fatalf("modelDuration (%v) appears to include tool execution time (total=%v, toolDelay=%v)", modelDuration, totalElapsed, toolDelay)
	}
}
