package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestOpenCodeGoResponsesUsesDedicatedRouteAndStreamsToolCall(t *testing.T) {
	client := doerFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/v1/responses" {
			t.Errorf("path = %q", request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer go-secret" || request.Header.Get("x-opencode-session") == "" {
			t.Errorf("headers = %#v", request.Header)
		}
		var body struct {
			Model string            `json:"model"`
			Input []json.RawMessage `json:"input"`
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Model != "muse-spark-1.3-contributor" || len(body.Input) != 2 || len(body.Tools) != 1 || body.Tools[0].Name != "read" {
			t.Fatalf("body = %#v", body)
		}
		stream := strings.Join([]string{
			`data: {"type":"response.output_text.delta","delta":"Checking"}`,
			`data: {"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","call_id":"call_1","name":"read","arguments":""}}`,
			`data: {"type":"response.function_call_arguments.delta","output_index":1,"delta":"{\"path\":\"README.md\"}"}`,
			`data: {"type":"response.completed","response":{"usage":{"input_tokens":10,"output_tokens":4},"output":[{"type":"message","content":[{"type":"output_text","text":"Checking"}]},{"type":"function_call","call_id":"call_1","name":"read","arguments":"{\"path\":\"README.md\"}"}]}}`,
		}, "\n") + "\n\n"
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(stream))}, nil
	})
	provider, err := newOpenCodeGo(Config{BaseURL: "http://go.test/v1", APIKey: "go-secret", HTTP: client})
	if err != nil {
		t.Fatal(err)
	}
	var streamed string
	response, err := provider.Complete(context.Background(), Request{Model: "muse-spark-1.3-contributor", Messages: []Message{{Role: "system", Content: "be helpful"}, {Role: "user", Content: "inspect"}}, Tools: []Tool{{Name: "read", Parameters: map[string]any{"type": "object"}}}}, func(event StreamEvent) { streamed += event.Text })
	if err != nil {
		t.Fatal(err)
	}
	if response.Message.Content != "Checking" || streamed != "Checking" || response.Usage == nil || response.Usage.InputTokens != 10 || len(response.Message.ToolCalls) != 1 {
		t.Fatalf("response = %#v, streamed = %q", response, streamed)
	}
	call := response.Message.ToolCalls[0]
	if call.ID != "call_1" || call.Name != "read" || string(call.Arguments) != `{"path":"README.md"}` || !json.Valid(response.Message.ReasoningDetails) {
		t.Fatalf("call = %#v, details = %s", call, response.Message.ReasoningDetails)
	}
}

func TestResponsesInputReplaysOutputAndToolResult(t *testing.T) {
	raw := json.RawMessage(`[{"type":"reasoning","encrypted_content":"opaque"},{"type":"function_call","call_id":"call_1","name":"read","arguments":"{}"}]`)
	items := responsesInput([]Message{{Role: "assistant", ReasoningDetails: raw}, {Role: "tool", ToolCallID: "call_1", Content: "file contents"}})
	encoded, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"encrypted_content":"opaque"`) || !strings.Contains(string(encoded), `"type":"function_call_output"`) || !strings.Contains(string(encoded), `"call_id":"call_1"`) {
		t.Fatalf("input = %s", encoded)
	}
}
