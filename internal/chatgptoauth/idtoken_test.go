package chatgptoauth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"strings"
	"testing"
	"time"
)

func testKey(t *testing.T) (*rsa.PrivateKey, jwk) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	n := base64.RawURLEncoding.EncodeToString(priv.PublicKey.N.Bytes())
	eb := big.NewInt(int64(priv.PublicKey.E)).Bytes()
	e := base64.RawURLEncoding.EncodeToString(eb)
	return priv, jwk{Kty: "RSA", Kid: "test-kid", Use: "sig", Alg: "RS256", N: n, E: e}
}

func TestVerifyIDTokenHappyPath(t *testing.T) {
	priv, pub := testKey(t)
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "test-kid"})
	claims := map[string]any{
		"iss": Issuer, "sub": "sub-123", "aud": "oaiapp_123",
		"nonce": "n-1", "email": "user@example.com",
		"exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(),
	}
	h := base64.RawURLEncoding.EncodeToString(header)
	payload, _ := json.Marshal(claims)
	p := base64.RawURLEncoding.EncodeToString(payload)
	// Sign with proper crypto hash.
	sig := signWith(t, priv, h+"."+p)
	token := h + "." + p + "." + sig
	set := jwks{Keys: []jwk{pub}}
	got, err := VerifyIDToken(token, "n-1", "oaiapp_123", set)
	if err != nil {
		t.Fatal(err)
	}
	if got.Subject != "sub-123" || got.Email != "user@example.com" {
		t.Fatalf("claims=%+v", got)
	}
	if _, err := VerifyIDToken(token, "wrong-nonce", "oaiapp_123", set); err == nil {
		t.Fatal("nonce mismatch accepted")
	}
	if _, err := VerifyIDToken(token, "n-1", "oaiapp_other", set); err == nil {
		t.Fatal("aud mismatch accepted")
	}
}

func signWith(t *testing.T, priv *rsa.PrivateKey, input string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(sig)
}

func TestVerifyIDTokenArrayAud(t *testing.T) {
	priv, pub := testKey(t)
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "test-kid"})
	claims := map[string]any{
		"iss": Issuer, "sub": "s", "aud": []string{"oaiapp_123", "other"},
		"nonce": "n", "exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(),
	}
	h := base64.RawURLEncoding.EncodeToString(header)
	payload, _ := json.Marshal(claims)
	p := base64.RawURLEncoding.EncodeToString(payload)
	token := h + "." + p + "." + signWith(t, priv, h+"."+p)
	if _, err := VerifyIDToken(token, "n", "oaiapp_123", jwks{Keys: []jwk{pub}}); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyRejectsBadSignature(t *testing.T) {
	_, pub := testKey(t)
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "test-kid"})
	claims := map[string]any{"iss": Issuer, "sub": "s", "aud": "oaiapp_123", "nonce": "n", "exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix()}
	h := base64.RawURLEncoding.EncodeToString(header)
	payload, _ := json.Marshal(claims)
	p := base64.RawURLEncoding.EncodeToString(payload)
	token := h + "." + p + "." + signWith(t, other, h+"."+p)
	if _, err := VerifyIDToken(token, "n", "oaiapp_123", jwks{Keys: []jwk{pub}}); err == nil {
		t.Fatal("bad signature accepted")
	}
}

func TestVerifyWithFetchRefreshesUnknownKid(t *testing.T) {
	priv, pub := testKey(t)
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "test-kid"})
	claims := map[string]any{"iss": Issuer, "sub": "s", "aud": "oaiapp_123", "nonce": "n", "exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix()}
	h := base64.RawURLEncoding.EncodeToString(header)
	payload, _ := json.Marshal(claims)
	p := base64.RawURLEncoding.EncodeToString(payload)
	token := h + "." + p + "." + signWith(t, priv, h+"."+p)
	calls := 0
	do := stubDoer(func(r *http.Request) (*http.Response, error) {
		calls++
		var set jwks
		if calls == 1 {
			set = jwks{Keys: []jwk{{Kty: "RSA", Kid: "old", N: pub.N, E: pub.E}}}
		} else {
			set = jwks{Keys: []jwk{pub}}
		}
		if !strings.Contains(r.URL.String(), "jwks.json") {
			t.Fatalf("jwks url = %q", r.URL.String())
		}
		data, _ := json.Marshal(set)
		return &http.Response{StatusCode: 200, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(data)))}, nil
	})
	if _, err := VerifyIDTokenWithFetch(context.Background(), do, token, "n", "oaiapp_123"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls=%d want 2", calls)
	}
}
