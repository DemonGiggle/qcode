package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type doerFunc func(*http.Request) (*http.Response, error)

func (f doerFunc) Do(request *http.Request) (*http.Response, error) { return f(request) }

func TestOpenAIStreamsTextAndToolCall(t *testing.T) {
	var gotAuthorization string
	client := doerFunc(func(r *http.Request) (*http.Response, error) {
		gotAuthorization = r.Header.Get("Authorization")
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %q", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["stream"] != true {
			t.Errorf("stream = %v", body["stream"])
		}
		streamBody := strings.Join([]string{
			`data: {"choices":[{"delta":{"content":"hello "}}]}`,
			`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"abc","function":{"name":"re","arguments":"{\"pa"}}]}}]}`,
			`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"ad","arguments":"th\":\"x\"}"}}]}}]}`,
			`data: [DONE]`, "",
		}, "\n")
		return &http.Response{StatusCode: 200, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(streamBody))}, nil
	})

	provider, err := newOpenAI(Config{BaseURL: "http://provider.test/v1", APIKey: "secret", HTTP: client})
	if err != nil {
		t.Fatal(err)
	}
	var streamed strings.Builder
	response, err := provider.Complete(context.Background(), Request{Model: "test", Messages: []Message{{Role: "user", Content: "hi"}}}, func(part string) { streamed.WriteString(part) })
	if err != nil {
		t.Fatal(err)
	}
	if gotAuthorization != "Bearer secret" {
		t.Errorf("authorization = %q", gotAuthorization)
	}
	if streamed.String() != "hello " || response.Message.Content != "hello " {
		t.Errorf("content = %q / %q", streamed.String(), response.Message.Content)
	}
	if len(response.Message.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d", len(response.Message.ToolCalls))
	}
	call := response.Message.ToolCalls[0]
	if call.ID != "abc" || call.Name != "read" || string(call.Arguments) != `{"path":"x"}` {
		t.Errorf("tool call = %#v", call)
	}
}

func TestProviderNamesAreStable(t *testing.T) {
	got := strings.Join(Names(), ",")
	if got != "ollama,openai,openai-like" {
		t.Fatalf("names = %q", got)
	}
}
