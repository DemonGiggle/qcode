package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"qcode/internal/llm"
	"qcode/internal/tools"
	"qcode/internal/trace"
)

type fakeProvider struct{ calls int }

type lifecycleWriter struct {
	bytes.Buffer
	begins int
	ends   int
}

func (w *lifecycleWriter) BeginResponse() { w.begins++ }
func (w *lifecycleWriter) EndResponse()   { w.ends++ }

type responseProvider struct{}

type repeatingProvider struct{ calls int }

func (p *responseProvider) Name() string { return "response" }
func (p *responseProvider) Complete(_ context.Context, _ llm.Request, onText func(string)) (llm.Response, error) {
	onText("finished")
	return llm.Response{Message: llm.Message{Role: "assistant", Content: "finished"}}, nil
}

func (p *repeatingProvider) Name() string { return "repeating" }
func (p *repeatingProvider) Complete(_ context.Context, _ llm.Request, _ func(string)) (llm.Response, error) {
	p.calls++
	return llm.Response{Message: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{
		ID: "repeat", Name: "read", Arguments: json.RawMessage(`{"path":"same.txt"}`),
	}}}}, nil
}

func (p *fakeProvider) Name() string { return "fake" }
func (p *fakeProvider) Complete(_ context.Context, request llm.Request, onText func(string)) (llm.Response, error) {
	p.calls++
	if p.calls == 1 {
		return llm.Response{Message: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "one", Name: "write", Arguments: json.RawMessage(`{"path":"result.txt","content":"done"}`)}}}}, nil
	}
	last := request.Messages[len(request.Messages)-1]
	if last.Role != "tool" {
		panic("missing tool result")
	}
	onText("finished")
	return llm.Response{Message: llm.Message{Role: "assistant", Content: "finished"}}, nil
}

func TestAgentMarksResponseBoundaries(t *testing.T) {
	registry, err := tools.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	provider := &responseProvider{}
	var output lifecycleWriter
	var events bytes.Buffer
	runner := New(provider, "test", registry, trace.New(&events, false), &output, 1)
	if err := runner.Run(context.Background(), "respond"); err != nil {
		t.Fatal(err)
	}
	if output.begins != 1 || output.ends != 1 {
		t.Fatalf("boundaries = %d/%d", output.begins, output.ends)
	}
	if output.String() != "finished\n" {
		t.Fatalf("output = %q", output.String())
	}
}

func TestAgentRunsToolsUntilFinalResponse(t *testing.T) {
	registry, err := tools.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	provider := &fakeProvider{}
	var output, events bytes.Buffer
	runner := New(provider, "test", registry, trace.New(&events, false), &output, 4)
	if err := runner.Run(context.Background(), "create it"); err != nil {
		t.Fatal(err)
	}
	if provider.calls != 2 {
		t.Fatalf("provider calls = %d", provider.calls)
	}
	if output.String() != "finished\n" {
		t.Fatalf("output = %q", output.String())
	}
	for _, expected := range []string{"start llm fake", "end llm fake", "start tool write", "end tool write"} {
		if !bytes.Contains(events.Bytes(), []byte(expected)) {
			t.Errorf("events missing %q:\n%s", expected, events.String())
		}
	}
}

func TestAgentStopsRepeatedIdenticalToolCallsEarly(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "same.txt"), []byte("same"), 0o644); err != nil {
		t.Fatal(err)
	}
	registry, err := tools.New(root)
	if err != nil {
		t.Fatal(err)
	}
	provider := &repeatingProvider{}
	var output, events bytes.Buffer
	runner := New(provider, "test", registry, trace.New(&events, false), &output, 32)
	err = runner.Run(context.Background(), "repeat forever")
	if err == nil || !strings.Contains(err.Error(), `tool "read" was requested unchanged 3 times`) {
		t.Fatalf("error = %v", err)
	}
	if provider.calls != 3 {
		t.Fatalf("provider calls = %d", provider.calls)
	}
	if bytes.Count(events.Bytes(), []byte("start tool read")) != 2 {
		t.Fatalf("tool events:\n%s", events.String())
	}
}
