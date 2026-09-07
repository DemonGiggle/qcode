package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

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
	want := llm.SessionUsage{InputTokens: 1400, OutputTokens: 100, TotalTokens: 1500}
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
	a.recordUsage(nil)
	want.Missing = 1
	if a.SessionUsage() != want {
		t.Fatal("missing completion lost known totals")
	}
	a.recordUsage(&llm.Usage{InputTokens: 10, OutputTokens: 5})
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
	a.recordUsage(nil)
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
