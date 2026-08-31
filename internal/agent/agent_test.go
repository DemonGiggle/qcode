package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"qcode/internal/llm"
	"qcode/internal/tools"
	"qcode/internal/trace"
)

type fakeProvider struct{ calls int }

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
