package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func loginToken(t *testing.T, a *authStore) string {
	t.Helper()
	login, err := a.issue("https://host.tailnet.ts.net/qcode/ab")
	if err != nil {
		t.Fatal(err)
	}
	return strings.SplitN(login.URL, "#login=", 2)[1]
}

func redeem(handler http.Handler, token, identity string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]string{"token": token})
	r := httptest.NewRequest(http.MethodPost, "https://host.tailnet.ts.net/api/v1/login", bytes.NewReader(body))
	r.Header.Set(identityHeader, identity)
	r.Header.Set("Origin", "https://host.tailnet.ts.net")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func sessionFromLogin(t *testing.T, handler http.Handler, token string) string {
	t.Helper()
	w := redeem(handler, token, "alice@example.com")
	if w.Code != http.StatusOK {
		t.Fatalf("login: %d %s", w.Code, w.Body.String())
	}
	var response struct {
		Key string `json:"session_key"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Key) != 43 {
		t.Fatalf("key length = %d", len(response.Key))
	}
	return response.Key
}

func TestLoginExpiryAndConsumption(t *testing.T) {
	for _, tc := range []struct {
		name    string
		advance time.Duration
		want    int
	}{
		{"before deadline", loginLifetime - time.Nanosecond, http.StatusOK},
		{"at deadline", loginLifetime, http.StatusGone},
		{"after deadline", loginLifetime + time.Second, http.StatusGone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := New(&testPresentation{})
			now := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
			m.auth.now = func() time.Time { return now }
			token := loginToken(t, m.auth)
			now = now.Add(tc.advance)
			h := m.routes()
			w := redeem(h, token, "alice@example.com")
			if w.Code != tc.want {
				t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
			}
			w = redeem(h, token, "alice@example.com")
			code := "expired_link"
			if tc.want == http.StatusOK {
				code = "used_link"
			}
			if w.Code != http.StatusGone || !strings.Contains(w.Body.String(), code) {
				t.Fatalf("repeat: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestConcurrentLoginIsSingleUse(t *testing.T) {
	m := New(&testPresentation{})
	token, h := loginToken(t, m.auth), m.routes()
	var wg sync.WaitGroup
	results := make(chan int, 20)
	for i := 0; i < cap(results); i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- redeem(h, token, "alice@example.com").Code }()
	}
	wg.Wait()
	close(results)
	successes := 0
	for code := range results {
		if code == http.StatusOK {
			successes++
		} else if code != http.StatusGone {
			t.Fatalf("status = %d", code)
		}
	}
	if successes != 1 {
		t.Fatalf("successful redemptions = %d", successes)
	}
}

func TestAllRemoteAPIsRequireSession(t *testing.T) {
	m := New(&testPresentation{})
	h := m.routes()
	key := sessionFromLogin(t, h, loginToken(t, m.auth))
	for _, route := range []struct{ method, path string }{
		{"GET", "/api/v1/snapshot"}, {"GET", "/api/v1/catalog"}, {"GET", "/api/v1/events"},
		{"POST", "/api/v1/actions"}, {"POST", "/api/v1/interactions/interaction-1/resolve"},
	} {
		for _, credentials := range []struct{ name, key, identity string }{
			{"missing key", "", "alice@example.com"}, {"unknown key", testSessionKey, "alice@example.com"},
			{"wrong identity", key, "bob@example.com"}, {"missing identity", key, ""},
		} {
			t.Run(route.path+"/"+credentials.name, func(t *testing.T) {
				r := httptest.NewRequest(route.method, route.path, strings.NewReader(`{}`))
				r.Header.Set(identityHeader, credentials.identity)
				if credentials.key != "" {
					r.Header.Set("Authorization", "Bearer "+credentials.key)
				}
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != http.StatusUnauthorized {
					t.Fatalf("status = %d", w.Code)
				}
				if strings.Contains(w.Body.String(), "hello") {
					t.Fatal("unauthorized transcript disclosure")
				}
			})
		}
	}
}

func TestLoginRenewalAndRevocation(t *testing.T) {
	m := New(&testPresentation{})
	h := m.routes()
	old := loginToken(t, m.auth)
	fresh := loginToken(t, m.auth)
	if w := redeem(h, old, "alice@example.com"); w.Code != http.StatusUnauthorized {
		t.Fatal("superseded link worked")
	}
	key := sessionFromLogin(t, h, fresh)
	_ = loginToken(t, m.auth)
	m.auth.now = func() time.Time { return time.Now().Add(24 * time.Hour) }
	check := func(handler http.Handler, want int) {
		r := httptest.NewRequest("GET", "/api/v1/snapshot", nil)
		r.Header.Set(identityHeader, "alice@example.com")
		r.Header.Set("Authorization", "Bearer "+key)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("session status = %d, want %d", w.Code, want)
		}
	}
	check(h, http.StatusOK)
	if err := m.Stop(); err != nil {
		t.Fatal(err)
	}
	check(h, http.StatusUnauthorized)
	check(m.routesWithAuth(newAuthStore()), http.StatusUnauthorized)
	if _, err := m.auth.issue("https://host"); err == nil {
		t.Fatal("closed store issued a login")
	}
}

func TestRevocationClosesEventStream(t *testing.T) {
	p := &testPresentation{connections: make(chan string, 2)}
	m := New(p)
	h := m.routes()
	key := sessionFromLogin(t, h, loginToken(t, m.auth))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	r := httptest.NewRequest("GET", "/api/v1/events", nil).WithContext(ctx)
	r.Header.Set(identityHeader, "alice@example.com")
	r.Header.Set("Authorization", "Bearer "+key)
	done := make(chan struct{})
	go func() { h.ServeHTTP(httptest.NewRecorder(), r); close(done) }()
	select {
	case <-p.connections:
	case <-ctx.Done():
		t.Fatal("stream did not connect")
	}
	m.Stop()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("revoked stream did not close")
	}
}

func TestLoginShellAndInvalidLinks(t *testing.T) {
	m := New(&testPresentation{})
	h := m.routes("/qcode/ab")
	token := loginToken(t, m.auth)
	for _, target := range []string{"/", "/qcode/ab", "/qcode/ab/"} {
		r := httptest.NewRequest("GET", target, nil)
		r.Header.Set(identityHeader, "alice@example.com")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 || strings.Contains(w.Body.String(), token) || strings.Contains(w.Body.String(), `"hello"`) {
			t.Fatal("login shell exposed session data")
		}
	}
	if m.auth.state() != "available" {
		t.Fatal("opening shell consumed token")
	}
	for _, invalid := range []string{"", "short", strings.Repeat("!", 43)} {
		if w := redeem(h, invalid, "alice@example.com"); w.Code != http.StatusUnauthorized {
			t.Fatalf("invalid token status = %d", w.Code)
		}
	}
	if w := redeem(h, token, ""); w.Code != http.StatusUnauthorized {
		t.Fatal("anonymous login accepted")
	}
	body, _ := json.Marshal(map[string]string{"token": token})
	r := httptest.NewRequest("POST", "https://host.tailnet.ts.net/api/v1/login", bytes.NewReader(body))
	r.Header.Set(identityHeader, "alice@example.com")
	r.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatal("cross-origin login accepted")
	}
	_ = sessionFromLogin(t, h, token)
}
