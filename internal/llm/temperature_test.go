package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestProviderTemperature(t *testing.T) {
	zero, sampled := 0.0, 0.7
	for _, adapter := range []struct {
		name     string
		factory  Factory
		model    string
		path     string
		nested   bool
		omit     bool
		metadata *ProviderModelMeta
	}{
		{name: "ollama", factory: newOllama, model: "qwen3.8:27b", path: "/api/chat", nested: true},
		{name: "openai-chat", factory: newOpenAI, model: "test", path: "/chat/completions"},
		{name: "opencode-go-chat", factory: newOpenCodeGo, model: "deepseek-v4-flash", path: "/chat/completions"},
		{
			name: "openai-reasoning", factory: newOpenAI, model: "test-reasoner", path: "/chat/completions", omit: true,
			metadata: &ProviderModelMeta{
				Provider: "openai",
				Models: map[string]ProviderModelOptions{
					"test-reasoner": {Effort: []string{"low", "high"}},
				},
			},
		},
		{name: "opencode-go-responses", factory: newOpenCodeGo, model: "gpt-5.6-luna", path: "/responses", omit: true},
		{name: "opencode-go-messages", factory: newOpenCodeGo, model: "qwen3.8-max", path: "/messages", omit: true},
	} {
		t.Run(adapter.name, func(t *testing.T) {
			for _, test := range []struct {
				name        string
				temperature *float64
			}{
				{name: "unspecified"},
				{name: "explicit-zero", temperature: &zero},
				{name: "explicit-nonzero", temperature: &sampled},
			} {
				t.Run(test.name, func(t *testing.T) {
					calls := 0
					client := doerFunc(func(request *http.Request) (*http.Response, error) {
						calls++
						if !strings.HasSuffix(request.URL.Path, adapter.path) {
							t.Errorf("path = %q, want suffix %q", request.URL.Path, adapter.path)
						}
						var body map[string]any
						if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
							t.Fatal(err)
						}
						parameters := body
						if adapter.nested {
							if _, ok := body["temperature"]; ok {
								t.Error("Ollama temperature must be inside options")
							}
							parameters, _ = body["options"].(map[string]any)
						}
						got, present := parameters["temperature"]
						wantPresent := test.temperature != nil && !adapter.omit
						if present != wantPresent {
							t.Errorf("temperature = %#v, present = %t, want present = %t", got, present, wantPresent)
						} else if wantPresent && got != *test.temperature {
							t.Errorf("temperature = %#v, want %v", got, *test.temperature)
						}
						stream := "data: [DONE]\n\n"
						if adapter.nested {
							stream = "{\"message\":{\"role\":\"assistant\",\"content\":\"ok\"},\"done\":true}\n"
						} else if adapter.path == "/messages" {
							stream = "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
						}
						return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(stream))}, nil
					})
					provider, err := adapter.factory(Config{HTTP: client, ModelMeta: adapter.metadata})
					if err != nil {
						t.Fatal(err)
					}
					if _, err := provider.Complete(context.Background(), Request{Model: adapter.model, Temperature: test.temperature}, nil); err != nil {
						t.Fatal(err)
					}
					if calls != 1 {
						t.Errorf("HTTP calls = %d, want 1", calls)
					}
				})
			}
		})
	}
}
