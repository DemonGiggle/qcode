package llm

import (
	"context"
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
