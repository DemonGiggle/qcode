package llm

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestOllamaStreamsTextAndToolCall(t *testing.T) {
	client := doerFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/api/chat" {
			t.Errorf("path = %q", r.URL.Path)
		}
		body := "{\"message\":{\"role\":\"assistant\",\"thinking\":\"checking \"},\"done\":false}\n" +
			"{\"message\":{\"role\":\"assistant\",\"content\":\"hi\"},\"done\":false}\n" +
			"{\"message\":{\"role\":\"assistant\",\"tool_calls\":[{\"function\":{\"name\":\"list\",\"arguments\":{\"path\":\".\"}}}]},\"done\":true}\n"
		return &http.Response{StatusCode: 200, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	provider, err := newOllama(Config{BaseURL: "http://ollama.test", HTTP: client})
	if err != nil {
		t.Fatal(err)
	}
	var streamed strings.Builder
	response, err := provider.Complete(context.Background(), Request{Model: "test", Messages: []Message{{Role: "user", Content: "hi"}}}, func(event StreamEvent) {
		fmt.Fprintf(&streamed, "%d:%s", event.Kind, event.Text)
	})
	if err != nil {
		t.Fatal(err)
	}
	if streamed.String() != "1:checking 0:hi" || response.Message.Content != "hi" {
		t.Errorf("content = %q", response.Message.Content)
	}
	if len(response.Message.ToolCalls) != 1 || response.Message.ToolCalls[0].Name != "list" {
		t.Fatalf("calls = %#v", response.Message.ToolCalls)
	}
}
