package llm

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

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
