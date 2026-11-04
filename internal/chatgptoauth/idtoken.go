package chatgptoauth

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"time"
)

// jwk is a single RSA JSON Web Key.
type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
}

// jwks is a JSON Web Key Set.
type jwks struct {
	Keys []jwk `json:"keys"`
}

// Claims are the verified ID-token identity fields qcode retains.
type Claims struct {
	Issuer        string
	Subject       string
	Email         string
	EmailVerified bool
	Name          string
	Nonce         string
	IssuedAt      time.Time
	ExpiresAt     time.Time
}

// fetchJWKS loads the published key set.
func fetchJWKS(ctx context.Context, do Doer) (jwks, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, JWKSURI, nil)
	if err != nil {
		return jwks{}, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := do.Do(req)
	if err != nil {
		return jwks{}, fmt.Errorf("fetch JWKS: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return jwks{}, fmt.Errorf("fetch JWKS returned %s", resp.Status)
	}
	var set jwks
	if err := json.Unmarshal(data, &set); err != nil {
		return jwks{}, fmt.Errorf("invalid JWKS: %w", err)
	}
	if len(set.Keys) == 0 {
		return jwks{}, fmt.Errorf("JWKS has no keys")
	}
	return set, nil
}

func (s jwks) find(kid string) (jwk, error) {
	if kid == "" {
		if len(s.Keys) == 1 {
			return s.Keys[0], nil
		}
		return jwk{}, fmt.Errorf("ID token missing key id")
	}
	for _, k := range s.Keys {
		if k.Kid == kid {
			return k, nil
		}
	}
	return jwk{}, fmt.Errorf("unknown key id %q", kid)
}

func rsaPublicKey(k jwk) (*rsa.PublicKey, error) {
	if k.Kty != "" && k.Kty != "RSA" {
		return nil, fmt.Errorf("unsupported key type %q", k.Kty)
	}
	nb, err := base64.RawURLEncoding.DecodeString(k.N)
	if err != nil {
		return nil, fmt.Errorf("invalid modulus: %w", err)
	}
	eb, err := base64.RawURLEncoding.DecodeString(k.E)
	if err != nil {
		eb, err = base64.URLEncoding.DecodeString(k.E)
		if err != nil {
			return nil, fmt.Errorf("invalid exponent: %w", err)
		}
	}
	if len(nb) == 0 || len(eb) == 0 {
		return nil, fmt.Errorf("incomplete RSA key")
	}
	n := new(big.Int).SetBytes(nb)
	e := 0
	for _, b := range eb {
		e = e<<8 + int(b)
	}
	if e <= 0 {
		return nil, fmt.Errorf("invalid exponent")
	}
	return &rsa.PublicKey{N: n, E: e}, nil
}

// verifyWithSet verifies signature and claims against a loaded set.
func verifyWithSet(idToken, expectedNonce, expectedClientID string, set jwks) (Claims, error) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return Claims{}, fmt.Errorf("malformed ID token")
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	hdrRaw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return Claims{}, fmt.Errorf("malformed ID token header")
	}
	if err := json.Unmarshal(hdrRaw, &header); err != nil {
		return Claims{}, fmt.Errorf("malformed ID token header")
	}
	if header.Alg != "" && header.Alg != "RS256" {
		return Claims{}, fmt.Errorf("unexpected ID token alg %q", header.Alg)
	}
	key, err := set.find(header.Kid)
	if err != nil {
		return Claims{}, err
	}
	pub, err := rsaPublicKey(key)
	if err != nil {
		return Claims{}, err
	}
	signingInput := parts[0] + "." + parts[1]
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return Claims{}, fmt.Errorf("malformed ID token signature")
	}
	sum := sha256.Sum256([]byte(signingInput))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig); err != nil {
		return Claims{}, fmt.Errorf("invalid ID token signature: %w", err)
	}
	payloadRaw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		// Lenient padded fallback.
		payloadRaw, err = base64.URLEncoding.DecodeString(parts[1])
		if err != nil {
			return Claims{}, fmt.Errorf("malformed ID token payload")
		}
	}
	var payload struct {
		Iss           string   `json:"iss"`
		Sub           string   `json:"sub"`
		Aud           any      `json:"aud"`
		Nonce         string   `json:"nonce"`
		Email         string   `json:"email"`
		EmailVerified *bool    `json:"email_verified"`
		Name          string   `json:"name"`
		Exp           int64    `json:"exp"`
		Iat           int64    `json:"iat"`
		AuthTime      int64    `json:"auth_time"`
		SID           any      `json:"sid"`
	}
	dec := json.NewDecoder(strings.NewReader(string(payloadRaw)))
	dec.UseNumber()
	// Unmarshal flexibly: aud may be string or array.
	var raw map[string]any
	if err := json.Unmarshal(payloadRaw, &raw); err != nil {
		return Claims{}, fmt.Errorf("malformed ID token claims")
	}
	if v, ok := raw["iss"].(string); ok {
		payload.Iss = v
	}
	if v, ok := raw["sub"].(string); ok {
		payload.Sub = v
	}
	payload.Aud = raw["aud"]
	if v, ok := raw["nonce"].(string); ok {
		payload.Nonce = v
	}
	if v, ok := raw["email"].(string); ok {
		payload.Email = v
	}
	if v, ok := raw["name"].(string); ok {
		payload.Name = v
	}
	if v, ok := raw["exp"].(float64); ok {
		payload.Exp = int64(v)
	}
	if v, ok := raw["iat"].(float64); ok {
		payload.Iat = int64(v)
	}
	// json numbers may decode as float64; handle json.Number via second pass.
	if payload.Exp == 0 {
		if n, ok := raw["exp"].(json.Number); ok {
			if i, err := n.Int64(); err == nil {
				payload.Exp = i
			}
		}
	}
	_ = dec

	if payload.Iss != Issuer {
		return Claims{}, fmt.Errorf("ID token issuer %q must be %q", payload.Iss, Issuer)
	}
	if !audContains(payload.Aud, expectedClientID) {
		return Claims{}, fmt.Errorf("ID token audience does not match issued client")
	}
	now := time.Now()
	// Small clock-skew tolerance (website sample uses 5s; allow 2 minutes for
	// laptop clock drift).
	const skew = 120 * time.Second
	if payload.Exp == 0 || now.Add(-skew).Unix() >= payload.Exp {
		return Claims{}, fmt.Errorf("ID token is expired")
	}
	if payload.Iat != 0 && time.Unix(payload.Iat, 0).After(now.Add(skew)) {
		return Claims{}, fmt.Errorf("ID token issued in the future")
	}
	if expectedNonce != "" && payload.Nonce != expectedNonce {
		return Claims{}, fmt.Errorf("ID token nonce did not match")
	}
	if strings.TrimSpace(payload.Sub) == "" {
		return Claims{}, fmt.Errorf("ID token did not contain a subject")
	}
	claims := Claims{
		Issuer:  payload.Iss,
		Subject: payload.Sub,
		Email:   payload.Email,
		Name:    payload.Name,
		Nonce:   payload.Nonce,
	}
	if payload.Exp != 0 {
		claims.ExpiresAt = time.Unix(payload.Exp, 0)
	}
	if payload.Iat != 0 {
		claims.IssuedAt = time.Unix(payload.Iat, 0)
	}
	if b, ok := raw["email_verified"].(bool); ok {
		claims.EmailVerified = b
	}
	return claims, nil
}

func audContains(aud any, want string) bool {
	switch v := aud.(type) {
	case string:
		return v == want
	case []string:
		for _, s := range v {
			if s == want {
				return true
			}
		}
	case []any:
		for _, e := range v {
			if s, ok := e.(string); ok && s == want {
				return true
			}
		}
	}
	return false
}

// VerifyIDToken verifies signature against set and checks issuer, audience,
// expiry and nonce.
func VerifyIDToken(idToken, expectedNonce, expectedClientID string, set jwks) (Claims, error) {
	if expectedClientID == "" {
		return Claims{}, fmt.Errorf("verify requires the issued client_id")
	}
	return verifyWithSet(idToken, expectedNonce, expectedClientID, set)
}

// VerifyIDTokenWithFetch verifies against the published JWKS, refetching once
// when the key id is unknown (key rotation).
func VerifyIDTokenWithFetch(ctx context.Context, do Doer, idToken, expectedNonce, expectedClientID string) (Claims, error) {
	set, err := fetchJWKS(ctx, do)
	if err != nil {
		return Claims{}, err
	}
	claims, err := verifyWithSet(idToken, expectedNonce, expectedClientID, set)
	if err != nil && strings.Contains(err.Error(), "unknown key id") {
		set, ferr := fetchJWKS(ctx, do)
		if ferr != nil {
			return Claims{}, err
		}
		return verifyWithSet(idToken, expectedNonce, expectedClientID, set)
	}
	return claims, err
}
