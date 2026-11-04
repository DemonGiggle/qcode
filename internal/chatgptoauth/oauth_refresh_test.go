package chatgptoauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type stubDoer func(*http.Request) (*http.Response, error)

func (f stubDoer) Do(r *http.Request) (*http.Response, error) { return f(r) }

func issuedTokens(tok Tokens) Tokens {
	if tok.ClientID == "" {
		tok.ClientID = "oaiapp_test123"
	}
	if tok.ExtAgentHostID == "" {
		tok.ExtAgentHostID = "urn:uuid:host-test"
	}
	if tok.Scopes == nil {
		tok.Scopes = []string{"openid", "profile", "email", "offline_access", "resource.invoke", "chatgpt.tokens.use.direct"}
	}
	if tok.SavedAt == "" {
		tok.SavedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if tok.ExpiresIn == 0 && tok.ExpiresAt == 0 {
		tok.ExpiresIn = 3600
	}
	return tok
}

func writeTokens(t *testing.T, path string, tok Tokens) {
	t.Helper()
	tok = issuedTokens(tok)
	full, err := ResolvePath(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(tok)
	if err := os.WriteFile(full, append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
}

func jwtWithAccount(account string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256"}`))
	payload, _ := json.Marshal(map[string]string{"account_id": account})
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

func TestAccountIDFromAccessToken(t *testing.T) {
	if got := AccountIDFromAccessToken(jwtWithAccount("acct-123")); got != "acct-123" {
		t.Fatalf("got %q", got)
	}
	if got := AccountIDFromAccessToken("opaque"); got != "" {
		t.Fatalf("opaque should be empty, got %q", got)
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{}`))
	inner, _ := json.Marshal(map[string]any{"https://api.openai.com/auth": map[string]any{"account_id": "nested-1"}})
	tok := header + "." + base64.RawURLEncoding.EncodeToString(inner) + ".sig"
	if got := AccountIDFromAccessToken(tok); got != "nested-1" {
		t.Fatalf("nested got %q", got)
	}
}

func TestExchangeFillsAccountFromJWT(t *testing.T) {
	access := jwtWithAccount("jwt-acct")
	do := stubDoer(func(r *http.Request) (*http.Response, error) {
		if got := r.URL.String(); !strings.Contains(got, "api/accounts/oauth/token") {
			t.Fatalf("endpoint = %q", got)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.PostForm.Get("resource") != Resource || r.PostForm.Get("client_id") != "oaiapp_test123" {
			t.Fatalf("form = %v", r.PostForm)
		}
		if r.PostForm.Get("redirect_uri") == "" || strings.Contains(r.PostForm.Get("redirect_uri"), "localhost") {
			t.Fatalf("redirect_uri = %q", r.PostForm.Get("redirect_uri"))
		}
		body := `{"access_token":"` + access + `","refresh_token":"r1","expires_in":3600,"scope":"openid profile email offline_access resource.invoke chatgpt.tokens.use.direct"}`
		return &http.Response{StatusCode: 200, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	tok, err := Exchange(context.Background(), do, "code", "verifier", RedirectURI, "oaiapp_test123")
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccountID != "jwt-acct" {
		t.Fatalf("account = %q", tok.AccountID)
	}
	if tok.ClientID != "oaiapp_test123" || !tok.HasPlanScope() {
		t.Fatalf("tok = %+v", tok)
	}
	if _, err := Exchange(context.Background(), do, "code", "verifier", RedirectURI, DynamicClientID); err == nil {
		t.Fatal("dynamic client must be rejected")
	}
}

func TestExchangeUsesNewEndpoints(t *testing.T) {
	if AuthorizeEndpoint != "https://auth.openai.com/api/accounts/authorize" {
		t.Fatalf("authorize = %q", AuthorizeEndpoint)
	}
	if TokenEndpoint != "https://auth.openai.com/api/accounts/oauth/token" {
		t.Fatalf("token = %q", TokenEndpoint)
	}
}

func TestSaveLoadRoundTripKeeps0600(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "oauth.json")
	want := issuedTokens(Tokens{AccessToken: "a1", RefreshToken: "r1", ExpiresAt: time.Now().Add(time.Hour).Unix(), AccountID: "a9", IDToken: "idt", Subject: "sub-1", Issuer: Issuer})
	if err := Save(path, want); err != nil {
		t.Fatal(err)
	}
	full, _ := ResolvePath(path)
	st, err := os.Stat(full)
	if err != nil {
		t.Fatal(err)
	}
	if perm := st.Mode().Perm(); perm != 0600 {
		t.Fatalf("mode = %04o, want 0600", perm)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != want.AccessToken || got.RefreshToken != want.RefreshToken || got.AccountID != want.AccountID {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}
	if got.ClientID != want.ClientID || got.Subject != want.Subject {
		t.Fatalf("round trip lost identity: %+v", got)
	}
}

func TestGetValidTokensSerializesConcurrentRefresh(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "oauth.json")
	expired := Tokens{AccessToken: "old-access", RefreshToken: "shared-refresh", ExpiresAt: time.Now().Add(-time.Hour).Unix()}
	writeTokens(t, path, expired)
	var calls atomic.Int32
	do := stubDoer(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if err := r.ParseForm(); err == nil {
			if r.PostForm.Get("grant_type") != "refresh_token" || r.PostForm.Get("resource") != Resource {
				t.Errorf("refresh form = %v", r.PostForm)
			}
		}
		// Simulate single-use rotating refresh: small delay to force overlap.
		time.Sleep(50 * time.Millisecond)
		body := `{"access_token":"new-access","refresh_token":"new-refresh","expires_in":3600,"scope":"openid profile email offline_access resource.invoke chatgpt.tokens.use.direct","account_id":"a1"}`
		return &http.Response{StatusCode: 200, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	var wg sync.WaitGroup
	errs := make([]error, 2)
	toks := make([]Tokens, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tok, err := GetValidTokens(context.Background(), do, path)
			toks[i] = tok
			errs[i] = err
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatalf("refresh err: %v", err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("concurrent refresh sent %d token calls, want 1", calls.Load())
	}
	if toks[0].AccessToken != "new-access" || toks[1].AccessToken != "new-access" {
		t.Fatalf("tokens = %+v", toks)
	}
	if toks[0].ClientID == "" || toks[0].ExtAgentHostID == "" {
		t.Fatalf("merged = %+v", toks[0])
	}
}

func TestEnsureValidFailsFastWithoutRefresh(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "oauth.json")
	expired := Tokens{AccessToken: "old-access", ExpiresAt: time.Now().Add(-time.Hour).Unix()}
	writeTokens(t, path, expired)
	do := stubDoer(func(r *http.Request) (*http.Response, error) {
		t.Error("token endpoint must not be called without refresh token")
		return &http.Response{StatusCode: 500, Status: "500", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	// Remove refresh token to force fast failure.
	full, _ := ResolvePath(path)
	data, _ := os.ReadFile(full)
	var tok Tokens
	_ = json.Unmarshal(data, &tok)
	tok.RefreshToken = ""
	raw, _ := json.Marshal(tok)
	_ = os.WriteFile(full, append(raw, '\n'), 0600)
	if _, err := EnsureValid(context.Background(), do, path); err == nil {
		t.Fatal("expected login error")
	} else if !strings.Contains(err.Error(), "qcode login chatgpt") {
		t.Fatalf("err = %v", err)
	}
}

func TestLegacyFileRequiresLogin(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "oauth.json")
	legacy := Tokens{AccessToken: "a", RefreshToken: "r", ExpiresAt: time.Now().Add(time.Hour).Unix(), ClientID: LegacyClientID}
	full, _ := ResolvePath(path)
	_ = os.MkdirAll(filepath.Dir(full), 0700)
	data, _ := json.Marshal(legacy)
	_ = os.WriteFile(full, append(data, '\n'), 0600)
	do := stubDoer(func(r *http.Request) (*http.Response, error) {
		t.Fatal("must not refresh legacy file")
		return nil, nil
	})
	if _, err := GetValidTokens(context.Background(), do, path); err == nil || !strings.Contains(err.Error(), "qcode login chatgpt") {
		t.Fatalf("err = %v", err)
	}
}

func TestRefreshAndSaveWithStaleReusesFresh(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "oauth.json")
	fresh := Tokens{AccessToken: "fresh-access", RefreshToken: "fresh-refresh", ExpiresAt: time.Now().Add(time.Hour).Unix(), AccountID: "a9"}
	writeTokens(t, path, fresh)
	called := false
	do := stubDoer(func(r *http.Request) (*http.Response, error) {
		called = true
		return &http.Response{StatusCode: 200, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})
	got, err := RefreshAndSaveWithStale(context.Background(), do, path, "stale-access")
	if err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("fresh file should avoid token endpoint")
	}
	if got.AccessToken != "fresh-access" {
		t.Fatalf("got %q", got.AccessToken)
	}
}

func TestSecretHookBeforeSaveFailure(t *testing.T) {
	for _, force := range []bool{false, true} {
		t.Run(fmt.Sprintf("force=%t", force), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "oauth.json")
			writeTokens(t, path, Tokens{AccessToken: "old-access", RefreshToken: "old-refresh", ExpiresAt: time.Now().Add(-time.Hour).Unix()})
			seen := map[string]bool{}
			register := func(values ...string) {
				for _, value := range values {
					seen[value] = true
				}
			}
			do := stubDoer(func(r *http.Request) (*http.Response, error) {
				if !seen["old-access"] || !seen["old-refresh"] {
					t.Fatal("refresh started before registration")
				}
				// An unsafe destination makes Save fail after a successful refresh.
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
				return &http.Response{StatusCode: 200, Status: "200 OK", Body: io.NopCloser(strings.NewReader(`{"access_token":"rotated-access","refresh_token":"rotated-refresh"}`))}, nil
			})
			var err error
			if force {
				_, err = RefreshAndSaveWithStaleWithSecrets(context.Background(), do, path, "old-access", register)
			} else {
				_, err = GetValidTokensWithSecrets(context.Background(), do, path, register)
			}
			if err == nil {
				t.Fatal("expected Save failure")
			}
			if !seen["rotated-access"] || !seen["rotated-refresh"] {
				t.Fatal("rotated tokens not registered before Save failure")
			}
		})
	}
}

func TestSecretHookRegistersReusedTokens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oauth.json")
	writeTokens(t, path, Tokens{AccessToken: "sibling-access", RefreshToken: "sibling-refresh", ExpiresAt: time.Now().Add(time.Hour).Unix()})
	seen := map[string]bool{}
	do := stubDoer(func(*http.Request) (*http.Response, error) { t.Fatal("unexpected HTTP request"); return nil, nil })
	_, err := RefreshAndSaveWithStaleWithSecrets(context.Background(), do, path, "stale-access", func(values ...string) {
		for _, value := range values {
			seen[value] = true
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if !seen["sibling-access"] || !seen["sibling-refresh"] {
		t.Fatal("reused tokens not registered")
	}
}
