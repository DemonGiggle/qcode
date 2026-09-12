package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

var uuidV4Pattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestOpenCodeGoUsesDedicatedEndpointAndIdentity(t *testing.T) {
	client := doerFunc(func(request *http.Request) (*http.Response, error) {
		if got := request.URL.String(); got != openCodeGoBaseURL+"/chat/completions" {
			t.Errorf("URL = %q", got)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer go-secret" {
			t.Errorf("authorization = %q", got)
		}
		if got := request.Header.Get("User-Agent"); got != "qcode" {
			t.Errorf("user agent = %q", got)
		}
		if got := request.Header.Get("x-opencode-session"); !uuidV4Pattern.MatchString(got) {
			t.Errorf("x-opencode-session = %q, want UUIDv4", got)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("data: [DONE]\n\n")),
		}, nil
	})
	provider, err := newOpenCodeGo(Config{APIKey: "go-secret", HTTP: client})
	if err != nil {
		t.Fatal(err)
	}
	if provider.Name() != "opencode-go" {
		t.Fatalf("name = %q", provider.Name())
	}
	if _, err := provider.Complete(context.Background(), Request{Model: "kimi-k3"}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestOpenCodeGoThinkingUsesOnlyModelValidEncoding(t *testing.T) {
	tests := []struct {
		name, model, level, effort, thinking string
	}{
		{"deepseek effort", "deepseek-v4-flash", "max", "max", ""},
		{"deepseek v4.1 effort", "deepseek-v4.1-flash", "low", "low", ""},
		{"hy no-think", "hy4-preview", "none", "none", ""},
		{"deepseek off", "deepseek-v4-flash", "off", "", "disabled"},
		{"glm toggle", "glm-5", "on", "", "enabled"},
		{"unknown omits", "future-model", "max", "", ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := doerFunc(func(request *http.Request) (*http.Response, error) {
				var body map[string]json.RawMessage
				if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				var effort string
				_ = json.Unmarshal(body["reasoning_effort"], &effort)
				if effort != test.effort {
					t.Errorf("reasoning_effort = %q, want %q", effort, test.effort)
				}
				var thinking struct {
					Type string `json:"type"`
				}
				_ = json.Unmarshal(body["thinking"], &thinking)
				if thinking.Type != test.thinking {
					t.Errorf("thinking.type = %q, want %q", thinking.Type, test.thinking)
				}
				if test.effort != "" && test.thinking != "" {
					t.Fatal("test setup permits mutually exclusive fields")
				}
				return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader("data: [DONE]\n\n"))}, nil
			})
			provider, err := newOpenCodeGo(Config{HTTP: client})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := provider.Complete(context.Background(), Request{Model: test.model, Thinking: test.level}, nil); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestOpenCodeGoReplaysReasoningContentOnToolContinuation(t *testing.T) {
	requests := 0
	client := doerFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		if requests == 2 {
			var body struct {
				Messages []struct {
					Role             string  `json:"role"`
					ReasoningContent *string `json:"reasoning_content"`
				} `json:"messages"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if len(body.Messages) != 1 || body.Messages[0].Role != "assistant" || body.Messages[0].ReasoningContent == nil || *body.Messages[0].ReasoningContent != "check first" {
				t.Fatalf("replayed messages = %#v", body.Messages)
			}
		}
		stream := "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"check first\"}}]}\n\ndata: [DONE]\n\n"
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(stream))}, nil
	})
	provider, err := newOpenCodeGo(Config{HTTP: client})
	if err != nil {
		t.Fatal(err)
	}
	first, err := provider.Complete(context.Background(), Request{Model: "deepseek-v4-flash", Thinking: "high"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.Message.Thinking != "check first" {
		t.Fatalf("thinking = %q", first.Message.Thinking)
	}
	if _, err := provider.Complete(context.Background(), Request{Model: "deepseek-v4-flash", Thinking: "high", Messages: []Message{first.Message}}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestOpenCodeGoReusesSessionIDForAllRequests(t *testing.T) {
	var sessionID string
	client := doerFunc(func(request *http.Request) (*http.Response, error) {
		got := request.Header.Get("x-opencode-session")
		if !uuidV4Pattern.MatchString(got) {
			t.Errorf("x-opencode-session = %q, want UUIDv4", got)
		}
		if sessionID == "" {
			sessionID = got
		} else if got != sessionID {
			t.Errorf("x-opencode-session = %q, want %q", got, sessionID)
		}
		body := "data: [DONE]\n\n"
		if strings.HasSuffix(request.URL.Path, "/models") {
			body = `{"data":[{"id":"kimi-k3"}]}`
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	})
	provider, err := newOpenCodeGo(Config{HTTP: client})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Complete(context.Background(), Request{Model: "kimi-k3"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.(ModelLister).Models(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestOpenCodeGoRestoresSessionIdentity(t *testing.T) {
	first, err := newOpenCodeGo(Config{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := newOpenCodeGo(Config{})
	if err != nil {
		t.Fatal(err)
	}
	a, b := first.(*openAIProvider), second.(*openAIProvider)
	if err := b.RestoreSessionIdentity(a.SessionIdentity()); err != nil {
		t.Fatal(err)
	}
	if b.SessionIdentity() != a.SessionIdentity() {
		t.Fatal("routing identity changed")
	}
	if err := b.RestoreSessionIdentity("invalid\r\nheader"); err == nil {
		t.Fatal("invalid identity accepted")
	}
}

func TestOpenCodeGoAllowsBaseURLOverride(t *testing.T) {
	provider, err := newOpenCodeGo(Config{BaseURL: "http://go.test/custom/"})
	if err != nil {
		t.Fatal(err)
	}
	got := provider.(*openAIProvider).baseURL
	if got != "http://go.test/custom" {
		t.Fatalf("base URL = %q", got)
	}
}

func TestOpenCodeGoHidesAndRejectsModelsOnUnsupportedEndpoints(t *testing.T) {
	client := doerFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"data":[{"id":"glm-5.2"},{"id":"gpt-5.6-luna"},{"id":"qwen3.8-max"}]}`)),
		}, nil
	})
	provider, err := newOpenCodeGo(Config{HTTP: client})
	if err != nil {
		t.Fatal(err)
	}
	models, err := provider.(ModelLister).Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(models, ",") != "glm-5.2" {
		t.Fatalf("models = %v", models)
	}
	if err := provider.(ModelValidator).ValidateModel("gpt-5.6-luna"); err == nil || !strings.Contains(err.Error(), "Responses") {
		t.Fatalf("luna validation error = %v", err)
	}
	if err := provider.(ModelValidator).ValidateModel("glm-5.2"); err != nil {
		t.Fatalf("chat model validation error = %v", err)
	}
}
