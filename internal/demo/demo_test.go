package demo

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"qcode/internal/agent"
	"qcode/internal/llm"
	"qcode/internal/tools"
	"qcode/internal/trace"
	"qcode/internal/tui"
)

func TestSessionCallsEveryToolThenFinishes(t *testing.T) {
	schemas := []llm.Tool{{Name: "read"}, {Name: "write"}, {Name: "future_tool"}}
	session := newSession(schemas, 0)
	request := llm.Request{Tools: schemas, Messages: []llm.Message{{Role: "user", Content: "demo"}}}

	first, err := session.Provider.Complete(context.Background(), request, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Message.ToolCalls) != len(schemas) {
		t.Fatalf("tool calls = %d, want %d", len(first.Message.ToolCalls), len(schemas))
	}
	for index, call := range first.Message.ToolCalls {
		if call.Name != schemas[index].Name {
			t.Errorf("tool call %d = %q, want %q", index, call.Name, schemas[index].Name)
		}
		result, executeErr := session.Tools.ExecuteDetailed(context.Background(), call)
		if call.Name == "read" {
			if executeErr == nil || !strings.Contains(executeErr.Error(), "mocked read failed") {
				t.Errorf("read error = %v, want mocked failure", executeErr)
			}
			result.Output = "ERROR: " + executeErr.Error()
		} else if executeErr != nil || result.Output == "" {
			t.Errorf("execute %q = %#v, %v", call.Name, result, executeErr)
		}
		request.Messages = append(request.Messages, llm.Message{Role: "tool", Name: call.Name, Content: result.Output})
	}

	final, err := session.Provider.Complete(context.Background(), request, nil)
	if err != nil {
		t.Fatal(err)
	}
	if final.Message.Content == "" || len(final.Message.ToolCalls) != 0 {
		t.Fatalf("final response = %#v", final.Message)
	}
	if !strings.Contains(final.Message.Content, "| Feature | Demonstration | Safety |") ||
		!strings.Contains(final.Message.Content, "| :--- | :---: | ---: |") {
		t.Fatalf("final response has no demonstration table:\n%s", final.Message.Content)
	}
}

func TestSessionRespondsDirectlyToQueuedFollowUp(t *testing.T) {
	schemas := []llm.Tool{{Name: "read"}}
	session := newSession(schemas, 0)
	request := llm.Request{
		Tools: schemas,
		Messages: []llm.Message{
			{Role: "system", Content: "system"},
			{Role: "user", Content: "first prompt"},
			{Role: "assistant", Content: "first response"},
			{Role: "user", Content: "queued prompt"},
		},
	}

	response, err := session.Provider.Complete(context.Background(), request, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Message.ToolCalls) != 0 || !strings.Contains(response.Message.Content, "FIFO order") {
		t.Fatalf("queued response = %#v, want a direct FIFO response", response.Message)
	}
}

func TestModelsReturnsLargeSearchableCatalog(t *testing.T) {
	session := newSession(nil, 0)
	models, err := session.Provider.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != demoModelCount+1 {
		t.Fatalf("models = %d, want %d", len(models), demoModelCount+1)
	}
	if models[0] != Model || models[len(models)-1] != "demo-coder-240" {
		t.Fatalf("model bounds = %q / %q", models[0], models[len(models)-1])
	}
}

func TestFakeWindowYieldsRealContextPercentages(t *testing.T) {
	root := t.TempDir()
	registry, err := tools.New(root)
	if err != nil {
		t.Fatal(err)
	}
	session := newSession(registry.Schemas(), 0)
	runner := agent.New(session.Provider, Model, session.Tools, trace.New(io.Discard, false), io.Discard, 4)
	// The scripted provider reports the fake capacity without a completion, so
	// the status bar shows a percentage instead of CONTEXT unknown.
	runner.RefreshContext(context.Background())
	left, known, estimated := runner.ContextRemaining()
	if !known {
		t.Fatal("demo context is unknown; the scripted provider must report its fake window")
	}
	if !estimated {
		t.Fatal("demo usage must stay estimated; the scripted provider reports no token usage")
	}
	if left <= 0 || left >= 100 {
		t.Fatalf("fresh demo session = %d%% left; want a partial window", left)
	}
	if err := runner.Run(context.Background(), "show me how qcode works"); err != nil {
		t.Fatal(err)
	}
	if grown, _, _ := runner.ContextRemaining(); grown >= left {
		t.Fatalf("context percentage did not move during the demo: %d%% before, %d%% after", left, grown)
	}
}

func TestWriteAndEditReturnMockCodeDiffs(t *testing.T) {
	session := newSession([]llm.Tool{{Name: "write"}, {Name: "edit"}}, 0)
	for _, name := range []string{"write", "edit"} {
		result, err := session.Tools.ExecuteDetailed(context.Background(), llm.ToolCall{Name: name})
		if err != nil {
			t.Fatal(err)
		}
		if result.Diff == "" {
			t.Errorf("%s returned no diff", name)
		}
	}
}

func TestFullDemoProducesColoredPageableShowcaseWithoutSideEffects(t *testing.T) {
	root := t.TempDir()
	registry, err := tools.New(root)
	if err != nil {
		t.Fatal(err)
	}
	session := newSession(registry.Schemas(), 0)
	var output, events bytes.Buffer
	writer := tui.NewMarkdownWriter(&output, true, 80)
	writer.EnableDiffs()
	runner := agent.New(session.Provider, Model, session.Tools, trace.New(&events, false), writer, 4)
	if err := runner.Run(context.Background(), "show everything"); err != nil {
		t.Fatal(err)
	}

	rendered := output.String()
	for _, expected := range []string{"Added demo/greeter.go", "Edited demo/greeter.go", "┌", "Demo report", "\x1b["} {
		if !strings.Contains(rendered, expected) {
			t.Errorf("showcase output missing %q:\n%s", expected, rendered)
		}
	}
	if lines := strings.Count(rendered, "\n"); lines < 24 {
		t.Errorf("showcase has only %d lines; it may not exercise paging", lines)
	}
	if _, err := os.Stat(filepath.Join(root, "demo", "greeter.go")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("demo created its mocked file: %v", err)
	}
}

func TestMockedBoundariesHonorCancellation(t *testing.T) {
	session := newSession([]llm.Tool{{Name: "read"}}, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := session.Provider.Complete(ctx, llm.Request{}, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("provider error = %v", err)
	}
	if _, err := session.Provider.Models(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("model discovery error = %v", err)
	}
	if _, err := session.Tools.ExecuteDetailed(ctx, llm.ToolCall{Name: "read"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("tool error = %v", err)
	}
}
