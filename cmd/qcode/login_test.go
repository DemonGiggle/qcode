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
	"time"

	"qcode/internal/chatgptoauth"
)

// stateEchoReader answers the callback prompt by echoing back the state from
// the already-printed authorize URL, proving the paste-back round trip. It
// includes the issued client the spec returns on new registration.
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
	url := "http://127.0.0.1:1455/auth/callback?code=TESTCODE&state=" + url.QueryEscape(state) + "&client_id=oaiapp_test123&scope=openid+profile+email\n"
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
	oldVerify := loginVerify
	defer func() { loginExchange = oldExchange; loginVerify = oldVerify }()
	loginExchange = func(ctx context.Context, do chatgptoauth.Doer, code, verifier, redirectURI, clientID string) (chatgptoauth.Tokens, error) {
		if code != "TESTCODE" || verifier == "" {
			t.Fatalf("exchange got code=%q verifier=%q", code, verifier)
		}
		if clientID != "oaiapp_test123" {
			t.Fatalf("exchange clientID=%q", clientID)
		}
		if !strings.HasPrefix(redirectURI, "http://127.0.0.1:") || !strings.HasSuffix(redirectURI, "/auth/callback") {
			t.Fatalf("redirectURI=%q", redirectURI)
		}
		return chatgptoauth.Tokens{
			AccessToken:  "a1",
			RefreshToken: "r1",
			IDToken:      "idt-1",
			ExpiresIn:    3600,
			ExpiresAt:    time.Now().Add(time.Hour).Unix(),
			TokenType:    "Bearer",
			Scopes:       []string{"openid", "profile", "email", "offline_access", "resource.invoke", "chatgpt.tokens.use.direct"},
			SavedAt:      time.Now().UTC().Format(time.RFC3339),
			ClientID:     clientID,
		}, nil
	}
	loginVerify = func(ctx context.Context, do chatgptoauth.Doer, idToken, nonce, clientID string) (chatgptoauth.Claims, error) {
		if idToken != "idt-1" || clientID != "oaiapp_test123" || nonce == "" {
			t.Fatalf("verify got idToken=%q nonce=%q client=%q", idToken, nonce, clientID)
		}
		return chatgptoauth.Claims{Issuer: chatgptoauth.Issuer, Subject: "sub-1", Email: "user@example.com"}, nil
	}
	var stdout bytes.Buffer
	stdin := &stateEchoReader{t: t, stdout: &stdout}
	path := filepath.Join(t.TempDir(), "chatgpt-oauth.json")
	// stubDoer is unused by the stubbed exchange but satisfies the signature.
	if err := chatGPTLoginFlow(path, stdin, &stdout, chatgptoauthStubDoer(t)); err != nil {
		t.Fatal(err)
	}
	out := stdout.String()
	if !strings.Contains(out, "auth.openai.com/api/accounts/authorize") || !strings.Contains(out, "Saved") {
		t.Fatalf("output = %q", out)
	}
	if strings.Contains(out, "localhost") {
		t.Fatalf("must not use localhost: %q", out)
	}
	got, err := chatgptoauth.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != "a1" || got.ClientID != "oaiapp_test123" || got.Subject != "sub-1" {
		t.Fatalf("saved = %+v", got)
	}
	if !got.HasPlanScope() {
		t.Fatalf("saved scopes = %+v", got.Scopes)
	}
	if got.ExtAgentHostID == "" || got.IDToken != "idt-1" {
		t.Fatalf("saved identity = %+v", got)
	}
}

func TestChatGPTLoginFlowRejectsDeniedCallback(t *testing.T) {
	called := false
	oldExchange := loginExchange
	defer func() { loginExchange = oldExchange }()
	loginExchange = func(ctx context.Context, do chatgptoauth.Doer, code, verifier, redirectURI, clientID string) (chatgptoauth.Tokens, error) {
		called = true
		return chatgptoauth.Tokens{}, nil
	}
	var stdout bytes.Buffer
	path := filepath.Join(t.TempDir(), "chatgpt-oauth.json")
	err := chatGPTLoginFlow(path, strings.NewReader("http://127.0.0.1:1455/auth/callback?error=access_denied\n"), &stdout, chatgptoauthStubDoer(t))
	if err == nil {
		t.Fatal("expected authorization error")
	}
	if called {
		t.Fatal("exchange must not run after denied callback")
	}
}

func TestChatGPTLoginFlowRejectsMismatchedClient(t *testing.T) {
	oldExchange := loginExchange
	oldVerify := loginVerify
	defer func() { loginExchange = oldExchange; loginVerify = oldVerify }()
	// Seed a registration for account A.
	dir := t.TempDir()
	path := filepath.Join(dir, "chatgpt-oauth.json")
	seed := chatgptoauth.Tokens{
		AccessToken: "old", RefreshToken: "old-r", IDToken: "old-idt",
		ClientID: "oaiapp_aaaa", Subject: "sub-a", Issuer: chatgptoauth.Issuer,
		Email: "a@example.com", ExtAgentHostID: "urn:uuid:host-1",
		ExpiresIn: 3600, ExpiresAt: time.Now().Add(time.Hour).Unix(),
		Scopes: []string{"openid", "chatgpt.tokens.use.direct"}, SavedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if err := chatgptoauth.Save(path, seed); err != nil {
		t.Fatal(err)
	}
	loginExchange = func(ctx context.Context, do chatgptoauth.Doer, code, verifier, redirectURI, clientID string) (chatgptoauth.Tokens, error) {
		t.Fatal("exchange must not run after mismatched client")
		return chatgptoauth.Tokens{}, nil
	}
	loginVerify = func(ctx context.Context, do chatgptoauth.Doer, idToken, nonce, clientID string) (chatgptoauth.Claims, error) {
		t.Fatal("verify must not run after mismatched client")
		return chatgptoauth.Claims{}, nil
	}
	var stdout bytes.Buffer
	stdin := &stateEchoReader{t: t, stdout: &stdout} // returns oaiapp_test123, not oaiapp_aaaa
	err := chatGPTLoginFlow(path, stdin, &stdout, chatgptoauthStubDoer(t))
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("err = %v, want client mismatch", err)
	}
}
