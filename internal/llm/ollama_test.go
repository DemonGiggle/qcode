package llm

import (
	"context"
	"encoding/json"
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
	if streamed.String() != "1:checking 0:hi" || response.Message.Content != "hi" || response.Message.Thinking != "checking " {
		t.Errorf("message = %#v", response.Message)
	}
	if len(response.Message.ToolCalls) != 1 || response.Message.ToolCalls[0].Name != "list" {
		t.Fatalf("calls = %#v", response.Message.ToolCalls)
	}
}

func TestOllamaSendsThinkingAndNamedToolResult(t *testing.T) {
	client := doerFunc(func(r *http.Request) (*http.Response, error) {
		var payload struct {
			Messages []map[string]any `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if len(payload.Messages) != 2 {
			t.Fatalf("messages = %#v", payload.Messages)
		}
		assistant := payload.Messages[0]
		if assistant["role"] != "assistant" || assistant["thinking"] != "checking" || assistant["tool_calls"] == nil {
			t.Errorf("assistant message = %#v", assistant)
		}
		tool := payload.Messages[1]
		if tool["role"] != "tool" || tool["tool_name"] != "list" || tool["content"] != "files" {
			t.Errorf("tool message = %#v", tool)
		}
		body := `{"message":{"role":"assistant","content":"done"},"done":true}` + "\n"
		return &http.Response{StatusCode: 200, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	provider, err := newOllama(Config{BaseURL: "http://ollama.test", HTTP: client})
	if err != nil {
		t.Fatal(err)
	}
	request := Request{Model: "test", Messages: []Message{
		{Role: "assistant", Thinking: "checking", ToolCalls: []ToolCall{{Name: "list", Arguments: json.RawMessage(`{"path":"."}`)}}},
		{Role: "tool", Name: "list", Content: "files"},
	}}
	if _, err := provider.Complete(context.Background(), request, nil); err != nil {
		t.Fatal(err)
	}
}

func TestOllamaSendsImageData(t *testing.T) {
	client := doerFunc(func(r *http.Request) (*http.Response, error) {
		var payload struct {
			Messages []struct {
				Images []string `json:"images"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if len(payload.Messages) != 1 || len(payload.Messages[0].Images) != 1 || payload.Messages[0].Images[0] != "AQID" {
			t.Fatalf("messages = %#v", payload.Messages)
		}
		body := `{"message":{"role":"assistant","content":"seen"},"done":true}` + "\n"
		return &http.Response{StatusCode: 200, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	provider, err := newOllama(Config{BaseURL: "http://ollama.test", HTTP: client})
	if err != nil {
		t.Fatal(err)
	}
	request := Request{Model: "vision", Messages: []Message{{
		Role: "user", Content: "describe", Images: []Image{{MediaType: "image/png", Data: []byte{1, 2, 3}}},
	}}}
	if _, err := provider.Complete(context.Background(), request, nil); err != nil {
		t.Fatal(err)
	}
}

func TestOllamaListsLocalModels(t *testing.T) {
	client := doerFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/tags" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		body := `{"models":[{"name":"qwen:7b"},{"name":"coder:latest"}]}`
		return &http.Response{StatusCode: 200, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	provider, err := newOllama(Config{BaseURL: "http://ollama.test", HTTP: client})
	if err != nil {
		t.Fatal(err)
	}
	models, err := provider.(ModelLister).Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(models, ",") != "qwen:7b,coder:latest" {
		t.Fatalf("models = %v", models)
	}
}
