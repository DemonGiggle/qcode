package demo

import (
	"context"
	"errors"
	"testing"
	"time"

	"qcode/internal/llm"
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
		if executeErr != nil || result.Output == "" {
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

func TestMockedBoundariesHonorCancellation(t *testing.T) {
	session := newSession([]llm.Tool{{Name: "read"}}, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := session.Provider.Complete(ctx, llm.Request{}, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("provider error = %v", err)
	}
	if _, err := session.Tools.ExecuteDetailed(ctx, llm.ToolCall{Name: "read"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("tool error = %v", err)
	}
}
