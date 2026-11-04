package chatgptoauth

import (
	"strings"
	"testing"
	"time"
)

func TestPKCEChallengeVector(t *testing.T) {
	// RFC 7636 Appendix B vector.
	if got := Challenge("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"); got != "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM" {
		t.Fatalf("challenge mismatch: %s", got)
	}
	v, err := NewVerifier()
	if err != nil {
		t.Fatal(err)
	}
	if len(v) < 43 || len(v) > 128 {
		t.Fatalf("verifier length %d out of range", len(v))
	}
	s, err := NewState()
	if err != nil {
		t.Fatal(err)
	}
	if s == "" {
		t.Fatal("empty state")
	}
	n, err := NewNonce()
	if err != nil || n == "" {
		t.Fatalf("nonce err=%v", err)
	}
}

func TestAuthorizeURLRoundTrip(t *testing.T) {
	u := RegistrationAuthorizeURL("verifier123", "state123", "nonce123", RedirectURI, "urn:uuid:host-1")
	for _, want := range []string{
		"code_challenge_method=S256",
		"client_id=" + DynamicClientID,
		"agent_name_hint=" + AgentNameHint,
		"ext_agent_host_id=",
		"response_type=code",
		"nonce=nonce123",
		"resource=",
		"127.0.0.1",
		"%2Fauth%2Fcallback",
	} {
		// URL-encoded checks: compare against encoded or raw host.
		if want == "127.0.0.1" {
			if !strings.Contains(u, want) {
				t.Fatalf("missing %q: %s", want, u)
			}
			continue
		}
		if !strings.Contains(u, want) {
			t.Fatalf("missing %q: %s", want, u)
		}
	}
	if strings.Contains(u, "localhost") {
		t.Fatalf("must not use localhost: %s", u)
	}
	if !strings.HasPrefix(u, AuthorizeEndpoint+"?") {
		t.Fatalf("endpoint = %q", u)
	}
	reauth := ReauthAuthorizeURL("oaiapp_123", "urn:uuid:host-1", "idt", "user@example.com", "verifier123", "state123", "nonce123", RedirectURI)
	if !strings.Contains(reauth, "client_id=oaiapp_123") || strings.Contains(reauth, "agent_name_hint") {
		t.Fatalf("reauth url = %q", reauth)
	}
	if !strings.Contains(reauth, "id_token_hint=") || !strings.Contains(reauth, "login_hint=") {
		t.Fatalf("reauth hints missing: %q", reauth)
	}
}

func TestParseCallback(t *testing.T) {
	code, err := ParseCallback("http://127.0.0.1:1455/auth/callback?code=ABC&state=XYZ", "XYZ")
	if err != nil || code != "ABC" {
		t.Fatalf("parse=%q err=%v", code, err)
	}
	if _, err := ParseCallback("http://127.0.0.1:1455/auth/callback?code=ABC&state=OTHER", "XYZ"); err == nil {
		t.Fatal("state mismatch accepted")
	}
	if _, err := ParseCallback("http://127.0.0.1:1455/auth/callback?error=access_denied", ""); err == nil {
		t.Fatal("error param accepted")
	}
	if _, err := ParseCallback("http://127.0.0.1:1455/auth/callback?state=XYZ", "XYZ"); err == nil {
		t.Fatal("missing code accepted")
	}
}

func TestParseCallbackFullCapturesIssuedClient(t *testing.T) {
	cb, err := ParseCallbackFull("http://127.0.0.1:1455/auth/callback?code=C&state=S&client_id=oaiapp_abc&scope=openid+profile", "S")
	if err != nil {
		t.Fatal(err)
	}
	if cb.Code != "C" || cb.ClientID != "oaiapp_abc" || len(cb.Scopes) != 2 {
		t.Fatalf("callback=%+v", cb)
	}
}

func TestTokensExpiry(t *testing.T) {
	now := time.Now()
	fresh := Tokens{AccessToken: "a", RefreshToken: "r", ExpiresAt: now.Add(time.Hour).Unix()}
	if fresh.NeedsRefresh(now) || fresh.Expired(now) {
		t.Fatal("fresh token flagged")
	}
	soon := Tokens{AccessToken: "a", RefreshToken: "r", ExpiresAt: now.Add(2 * time.Minute).Unix()}
	if !soon.NeedsRefresh(now) {
		t.Fatal("near-expiry token flagged")
	}
	empty := Tokens{}
	if !empty.NeedsRefresh(now) || !empty.Expired(now) {
		t.Fatal("empty token not flagged")
	}
	// saved_at+expires_in fallback.
	saved := Tokens{AccessToken: "a", RefreshToken: "r", ExpiresIn: 3600, SavedAt: now.Add(-time.Minute).Format("2006-01-02T15:04:05Z07:00")}
	if saved.NeedsRefresh(now) || saved.Expired(now) {
		t.Fatal("saved_at fallback flagged fresh")
	}
}

func TestLegacyDetection(t *testing.T) {
	if !(Tokens{}.IsLegacy()) {
		t.Fatal("empty must be legacy")
	}
	if !(Tokens{ClientID: LegacyClientID}.IsLegacy()) {
		t.Fatal("legacy client must be legacy")
	}
	if !(Tokens{ClientID: DynamicClientID}.IsLegacy()) {
		t.Fatal("dynamic must be legacy")
	}
	if (Tokens{ClientID: "oaiapp_123"}.IsLegacy()) {
		t.Fatal("issued must not be legacy")
	}
	if !(Tokens{ClientID: "oaiapp_123", Scopes: []string{"chatgpt.tokens.use.direct"}}.HasPlanScope()) {
		t.Fatal("plan scope missing")
	}
}

func TestDefaultPathIsQcode(t *testing.T) {
	if !strings.Contains(DefaultPath, "qcode") {
		t.Fatalf("DefaultPath %q should point at qcode config dir", DefaultPath)
	}
}
