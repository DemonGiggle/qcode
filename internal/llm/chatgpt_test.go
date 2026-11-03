package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"qcode/internal/chatgptoauth"
)

func writeChatGPTOAuth(t *testing.T, tok chatgptoauth.Tokens) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "chatgpt-oauth.json")
	if tok.ClientID == "" {
		tok.ClientID = "oaiapp_test123"
	}
	if tok.ExtAgentHostID == "" {
		tok.ExtAgentHostID = "urn:uuid:host-test"
	}
	if tok.ExpiresAt == 0 && tok.ExpiresIn == 0 && tok.SavedAt == "" {
		tok.ExpiresAt = time.Now().Add(time.Hour).Unix()
	}
	if err := chatgptoauth.Save(path, tok); err != nil {
		t.Fatal(err)
	}
	return path
}

func responsesStream(text string) string {
	return strings.Join([]string{
		`data: {"type":"response.output_text.delta","delta":"` + text + `"}`,
		`data: {"type":"response.completed","response":{"output":[{"type":"message","content":[{"type":"output_text","text":"` + text + `"}]}]}}`,
	}, "\n") + "\n\n"
}

func TestChatGPTCompleteSendsCodexContract(t *testing.T) {
	path := writeChatGPTOAuth(t, chatgptoauth.Tokens{AccessToken: "test-access", RefreshToken: "test-refresh", AccountID: "acct-1"})
	var gotAuth, gotAccount string
	var body map[string]any
	client := doerFunc(func(r *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(r.URL.Path, "/responses") {
			t.Errorf("path = %q", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		gotAccount = r.Header.Get("ChatGPT-Account-Id")
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: 200, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(responsesStream("hi")))}, nil
	})
	provider, err := newChatGPT(Config{HTTP: client, ChatGPTOAuthPath: path})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := provider.Complete(context.Background(), Request{
		Model:    "gpt-5.6-codex",
		Messages: []Message{{Role: "system", Content: "be brief"}, {Role: "user", Content: "hello"}},
	}, func(StreamEvent) {})
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer test-access" || gotAccount != "acct-1" {
		t.Fatalf("auth headers = %q / %q", gotAuth, gotAccount)
	}
	if body["store"] != false || body["stream"] != true || body["instructions"] != "be brief" {
		t.Fatalf("codex contract body = %v", body)
	}
	input, _ := json.Marshal(body["input"])
	if strings.Contains(string(input), `"system"`) {
		t.Fatalf("system role leaked into input: %s", input)
	}
	if resp.Message.Content != "hi" {
		t.Fatalf("content = %q", resp.Message.Content)
	}
}

func TestChatGPTCompleteRefreshesOnceOn401(t *testing.T) {
	path := writeChatGPTOAuth(t, chatgptoauth.Tokens{AccessToken: "old-access", RefreshToken: "shared-refresh", AccountID: "acct-1"})
	var auths []string
	client := doerFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "auth.openai.com" {
			return &http.Response{StatusCode: 200, Status: "200 OK", Header: make(http.Header),
				Body: io.NopCloser(strings.NewReader(`{"access_token":"new-access","refresh_token":"new-refresh","expires_in":3600,"account_id":"acct-1"}`))}, nil
		}
		auths = append(auths, r.Header.Get("Authorization"))
		if len(auths) == 1 {
			return &http.Response{StatusCode: 401, Status: "401 Unauthorized", Header: make(http.Header),
				Body: io.NopCloser(strings.NewReader("unauthorized"))}, nil
		}
		return &http.Response{StatusCode: 200, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(responsesStream("ok")))}, nil
	})
	provider, err := newChatGPT(Config{HTTP: client, ChatGPTOAuthPath: path})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := provider.Complete(context.Background(), Request{Model: "gpt-5.6-codex", Messages: []Message{{Role: "user", Content: "hi"}}}, func(StreamEvent) {})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message.Content != "ok" {
		t.Fatalf("content = %q", resp.Message.Content)
	}
	if len(auths) != 2 || auths[0] != "Bearer old-access" || auths[1] != "Bearer new-access" {
		t.Fatalf("auth sequence = %v", auths)
	}
}

func TestChatGPTModelsDecodesCodexCatalog(t *testing.T) {
	path := writeChatGPTOAuth(t, chatgptoauth.Tokens{AccessToken: "a", RefreshToken: "r", AccountID: "acct-1"})
	client := doerFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer a" || r.Header.Get("ChatGPT-Account-Id") != "acct-1" {
			t.Errorf("headers = %#v", r.Header)
		}
		if got := r.URL.Query().Get("client_version"); got == "" {
			t.Errorf("client_version query missing in %q", r.URL.String())
		}
		return &http.Response{StatusCode: 200, Status: "200 OK", Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader(`{"models":[{"slug":"gpt-5.6-codex"},{"slug":"gpt-5.5-codex"}]}`))}, nil
	})
	provider, err := newChatGPT(Config{HTTP: client, ChatGPTOAuthPath: path})
	if err != nil {
		t.Fatal(err)
	}
	lister, ok := provider.(ModelLister)
	if !ok {
		t.Fatal("chatgpt provider should list models")
	}
	models, err := lister.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0] != "gpt-5.6-codex" || models[1] != "gpt-5.5-codex" {
		t.Fatalf("models = %v", models)
	}
}

func TestChatGPTDecodeLegacyFallback(t *testing.T) {
	models, err := decodeChatGPTModels([]byte(`{"data":[{"id":"gpt-5.6-codex"}]}`))
	if err != nil || len(models) != 1 || models[0] != "gpt-5.6-codex" {
		t.Fatalf("models = %v err = %v", models, err)
	}
}

func TestChatGPTRequiresLogin(t *testing.T) {
	provider, err := newChatGPT(Config{HTTP: doerFunc(func(r *http.Request) (*http.Response, error) {
		t.Error("no HTTP expected without credentials")
		return nil, nil
	}), ChatGPTOAuthPath: filepath.Join(t.TempDir(), "missing.json")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Complete(context.Background(), Request{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}}, func(StreamEvent) {}); err == nil {
		t.Fatal("expected login error")
	} else if !strings.Contains(err.Error(), "qcode login chatgpt") {
		t.Fatalf("err = %v", err)
	}
}
