package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"qcode/internal/chatgptoauth"
)

// stateEchoReader answers the callback prompt by echoing back the state from
// the already-printed authorize URL, proving the paste-back round trip.
type stateEchoReader struct {
	t      *testing.T
	stdout *bytes.Buffer
	sent   bool
}

func (r *stateEchoReader) Read(p []byte) (int, error) {
	if r.sent {
		return 0, io.EOF
	}
	r.sent = true
	raw := r.stdout.String()
	idx := strings.Index(raw, "state=")
	if idx < 0 {
		r.t.Fatal("authorize URL not printed before prompt")
	}
	state := raw[idx+len("state="):]
	if end := strings.IndexAny(state, " \n"); end >= 0 {
		state = state[:end]
	}
	state, _ = url.QueryUnescape(state)
	url := "http://localhost:1455/auth/callback?code=TESTCODE&state=" + url.QueryEscape(state) + "\n"
	return copy(p, url), nil
}

type loginStubDoer struct{ t *testing.T }

func (d loginStubDoer) Do(*http.Request) (*http.Response, error) {
	d.t.Fatal("token endpoint must not be called")
	return nil, nil
}

func chatgptoauthStubDoer(t *testing.T) chatgptoauth.Doer { return loginStubDoer{t: t} }

func TestRunLoginUsage(t *testing.T) {
	if err := runLogin(nil, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, true); err == nil {
		t.Fatal("expected usage error")
	}
	if err := runLogin([]string{"openai"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, true); err == nil {
		t.Fatal("expected usage error")
	}
}

func TestRunLoginRequiresTerminal(t *testing.T) {
	err := runLogin([]string{"chatgpt"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, false)
	if err == nil || !strings.Contains(err.Error(), "interactive terminal") {
		t.Fatalf("err = %v", err)
	}
}

func TestChatGPTLoginFlowSavesTokens(t *testing.T) {
	oldExchange := loginExchange
	defer func() { loginExchange = oldExchange }()
	loginExchange = func(ctx context.Context, do chatgptoauth.Doer, code, verifier string) (chatgptoauth.Tokens, error) {
		if code != "TESTCODE" || verifier == "" {
			t.Fatalf("exchange got code=%q verifier=%q", code, verifier)
		}
		return chatgptoauth.Tokens{AccessToken: "a1", RefreshToken: "r1", ExpiresAt: 9999999999, AccountID: "acct-1"}, nil
	}
	var stdout bytes.Buffer
	stdin := &stateEchoReader{t: t, stdout: &stdout}
	path := filepath.Join(t.TempDir(), "chatgpt-oauth.json")
	// stubDoer is unused by the stubbed exchange but satisfies the signature.
	if err := chatGPTLoginFlow(path, stdin, &stdout, chatgptoauthStubDoer(t)); err != nil {
		t.Fatal(err)
	}
	out := stdout.String()
	if !strings.Contains(out, "auth.openai.com") || !strings.Contains(out, "Saved") {
		t.Fatalf("output = %q", out)
	}
	got, err := chatgptoauth.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != "a1" || got.AccountID != "acct-1" {
		t.Fatalf("saved = %+v", got)
	}
}

func TestChatGPTLoginFlowRejectsDeniedCallback(t *testing.T) {
	called := false
	oldExchange := loginExchange
	defer func() { loginExchange = oldExchange }()
	loginExchange = func(ctx context.Context, do chatgptoauth.Doer, code, verifier string) (chatgptoauth.Tokens, error) {
		called = true
		return chatgptoauth.Tokens{}, nil
	}
	var stdout bytes.Buffer
	path := filepath.Join(t.TempDir(), "chatgpt-oauth.json")
	err := chatGPTLoginFlow(path, strings.NewReader("http://localhost:1455/auth/callback?error=access_denied\n"), &stdout, chatgptoauthStubDoer(t))
	if err == nil {
		t.Fatal("expected authorization error")
	}
	if called {
		t.Fatal("exchange must not run after denied callback")
	}
}
