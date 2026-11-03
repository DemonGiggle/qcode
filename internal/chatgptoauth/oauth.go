// Package chatgptoauth implements the open-source Sign in with ChatGPT
// (token-sharing) flow used by `qcode login chatgpt`.
//
// It follows
// https://developers.openai.com/siwc/token-sharing-open-source/sign-in :
//
//   - first registration uses client_id=dynamic_agent_client with
//     agent_name_hint + ext_agent_host_id; the callback returns the issued
//     client_id (oaiapp_...) which is saved and reused for later sign-ins.
//   - authorize + token endpoints are /api/accounts/... (not /oauth/...).
//   - redirect is an HTTP loopback on 127.0.0.1 /auth/callback (never
//     localhost); only the port may vary.
//   - scopes include offline_access resource.invoke
//     chatgpt.tokens.use.direct and resource=https://api.openai.com/v1.
//   - fresh state, OIDC nonce and PKCE per attempt; ID token signature,
//     issuer, audience, expiry and nonce are verified against the published
//     JWKS before credentials replace the active account.
package chatgptoauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	// DynamicClientID is the first-time registration entrypoint. It is never
	// saved or used for token exchange.
	DynamicClientID = "dynamic_agent_client"
	// AgentNameHint is this app's actual name, used consistently for initial
	// dynamic registration only.
	AgentNameHint = "qcode"
	// LegacyClientID mirrors the old Codex CLI PKCE client. Files still
	// carrying it predate the open-source dynamic-registration flow and must
	// re-login.
	LegacyClientID = "app_EMoamEEZ73f0CkXaXp7hrann"

	// AuthorizeEndpoint is the browser entry point.
	AuthorizeEndpoint = "https://auth.openai.com/api/accounts/authorize"
	// TokenEndpoint exchanges and refreshes tokens.
	TokenEndpoint = "https://auth.openai.com/api/accounts/oauth/token"
	// JWKSURI publishes the ID-token signing keys.
	JWKSURI = "https://auth.openai.com/.well-known/jwks.json"
	// Issuer is the expected ID/access token issuer.
	Issuer = "https://auth.openai.com"
	// Resource is the token audience for ChatGPT plan usage.
	Resource = "https://api.openai.com/v1"
	// Scopes requests OIDC identity plus offline refresh and ChatGPT plan use.
	Scopes = "openid profile email offline_access resource.invoke chatgpt.tokens.use.direct"
	// PlanScope is the granted scope that authorizes ChatGPT plan usage.
	PlanScope = "chatgpt.tokens.use.direct"

	// RedirectHost/Path are fixed by the spec; only the port may vary.
	RedirectHost = "127.0.0.1"
	RedirectPath = "/auth/callback"
	// DefaultRedirectPort is tried first for the loopback listener.
	DefaultRedirectPort = 1455
	// RedirectURI is the default loopback callback.
	RedirectURI = "http://127.0.0.1:1455/auth/callback"

	// DefaultPath is used when chatgpt.oauth_path is unset.
	DefaultPath = "~/.config/qcode/chatgpt-oauth.json"
	// DefaultHostPath stores this host's stable ext_agent_host_id.
	DefaultHostPath = "~/.config/qcode/chatgpt-host-id"
	// HostFileName is used when deriving a host file next to an overridden
	// oauth path (keeps temp-dir tests isolated).
	HostFileName = "chatgpt-host-id"
)

// refreshMargin triggers a refresh before the access token actually dies so a
// long model call does not race expiry.
const refreshMargin = 5 * time.Minute

// maxFileSize bounds the OAuth JSON file; tokens are short.
const maxFileSize = 64 * 1024

// Doer posts token HTTP requests. *http.Client satisfies it, as does the llm
// HTTPDoer (both use *net/http.Request/Response).
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// Tokens is the persisted credential record. It is a superset of the spec's
// example (which has no expires_at/account_id) so existing runtimes keep
// working: doc-required fields are always written, expires_at/account_id are
// retained for fast expiry checks and the legacy Codex backend header.
type Tokens struct {
	Email          string   `json:"email,omitempty"`
	Issuer         string   `json:"issuer,omitempty"`
	Subject        string   `json:"subject,omitempty"`
	ClientID       string   `json:"client_id,omitempty"`
	ExtAgentHostID string   `json:"ext_agent_host_id,omitempty"`
	IDToken        string   `json:"id_token,omitempty"`
	AccessToken    string   `json:"access_token"`
	RefreshToken   string   `json:"refresh_token,omitempty"`
	TokenType      string   `json:"token_type,omitempty"`
	ExpiresIn      int64    `json:"expires_in,omitempty"`
	ExpiresAt      int64    `json:"expires_at,omitempty"`
	Scopes         []string `json:"scopes,omitempty"`
	SavedAt        string   `json:"saved_at,omitempty"`
	AccountID      string   `json:"account_id,omitempty"`
}

// IsLegacy reports files that predate dynamic registration (no issued client)
// and must sign in again.
func (t Tokens) IsLegacy() bool {
	return t.ClientID == "" || t.ClientID == LegacyClientID || t.ClientID == DynamicClientID
}

// ExpiryTime resolves when the access token dies, preferring the explicit
// expires_at and falling back to saved_at+expires_in.
func (t Tokens) ExpiryTime() (time.Time, bool) {
	if t.ExpiresAt > 0 {
		return time.Unix(t.ExpiresAt, 0), true
	}
	if t.ExpiresIn > 0 && t.SavedAt != "" {
		if saved, err := time.Parse(time.RFC3339, t.SavedAt); err == nil {
			return saved.Add(time.Duration(t.ExpiresIn) * time.Second), true
		}
		// Lenient fallback for non-RFC3339 timestamps.
		if saved, err := time.Parse("2006-01-02T15:04:05Z07:00", t.SavedAt); err == nil {
			return saved.Add(time.Duration(t.ExpiresIn) * time.Second), true
		}
	}
	return time.Time{}, false
}

// Expired reports whether the access token is unusable.
func (t Tokens) Expired(now time.Time) bool {
	if t.AccessToken == "" {
		return true
	}
	if exp, ok := t.ExpiryTime(); ok {
		return !now.Before(exp)
	}
	return false
}

// NeedsRefresh reports whether the token should be refreshed proactively.
func (t Tokens) NeedsRefresh(now time.Time) bool {
	if t.AccessToken == "" || t.RefreshToken == "" {
		return true
	}
	if exp, ok := t.ExpiryTime(); ok {
		return !now.Add(refreshMargin).Before(exp)
	}
	return true
}

// HasPlanScope reports whether the granted scopes authorize ChatGPT plan use.
func (t Tokens) HasPlanScope() bool {
	for _, s := range t.Scopes {
		if s == PlanScope {
			return true
		}
	}
	return false
}

// NewVerifier generates a PKCE code_verifier (43-128 base64url chars).
func NewVerifier() (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("generate PKCE verifier: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf[:]), nil
}

// Challenge derives the PKCE code_challenge for a verifier.
func Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// NewState generates a CSRF state value.
func NewState() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("generate OAuth state: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf[:]), nil
}

// NewNonce generates a fresh OIDC nonce for ID-token validation.
func NewNonce() (string, error) {
	return NewState()
}

// RedirectURIForPort builds the loopback callback URI for a port.
func RedirectURIForPort(port int) string {
	return "http://" + RedirectHost + fmt.Sprintf(":%d", port) + RedirectPath
}

// RegistrationAuthorizeURL builds the first-time dynamic-registration URL.
func RegistrationAuthorizeURL(verifier, state, nonce, redirectURI, hostID string) string {
	q := url.Values{}
	q.Set("client_id", DynamicClientID)
	q.Set("agent_name_hint", AgentNameHint)
	q.Set("ext_agent_host_id", hostID)
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	q.Set("scope", Scopes)
	q.Set("resource", Resource)
	q.Set("state", state)
	q.Set("nonce", nonce)
	q.Set("code_challenge", Challenge(verifier))
	q.Set("code_challenge_method", "S256")
	return AuthorizeEndpoint + "?" + q.Encode()
}

// ReauthAuthorizeURL builds a returning-account URL for an issued client.
// agent_name_hint is omitted; id_token_hint/login_hint identify the account.
func ReauthAuthorizeURL(clientID, hostID, idTokenHint, loginHint, verifier, state, nonce, redirectURI string) string {
	q := url.Values{}
	q.Set("client_id", clientID)
	q.Set("ext_agent_host_id", hostID)
	if idTokenHint != "" {
		q.Set("id_token_hint", idTokenHint)
	}
	if loginHint != "" {
		q.Set("login_hint", loginHint)
	}
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	q.Set("scope", Scopes)
	q.Set("resource", Resource)
	q.Set("state", state)
	q.Set("nonce", nonce)
	q.Set("code_challenge", Challenge(verifier))
	q.Set("code_challenge_method", "S256")
	return AuthorizeEndpoint + "?" + q.Encode()
}

// ReauthAuthorizeURLForTokens is a convenience wrapper using the saved
// registration and the current host ID.
func ReauthAuthorizeURLForTokens(saved Tokens, hostID, verifier, state, nonce, redirectURI string) string {
	return ReauthAuthorizeURL(saved.ClientID, hostID, saved.IDToken, saved.Email, verifier, state, nonce, redirectURI)
}

// CallbackResult is a parsed loopback/pasted callback.
type CallbackResult struct {
	Code     string
	State    string
	ClientID string
	Scopes   []string
}

// ParseCallbackFull extracts code/state/issued-client/scopes, validating state
// and surfacing OAuth errors before any code exchange.
func ParseCallbackFull(pasted, wantState string) (CallbackResult, error) {
	pasted = strings.TrimSpace(pasted)
	if pasted == "" {
		return CallbackResult{}, fmt.Errorf("empty URL; approve in the browser first, then paste the full callback URL")
	}
	u, err := url.Parse(pasted)
	if err != nil {
		return CallbackResult{}, fmt.Errorf("invalid URL: %w", err)
	}
	q := u.Query()
	if msg := q.Get("error"); msg != "" {
		if wantState != "" && q.Get("state") != "" && q.Get("state") != wantState {
			return CallbackResult{}, fmt.Errorf("state mismatch; the pasted URL is not from this login attempt, run the command again")
		}
		desc := q.Get("error_description")
		if desc != "" {
			return CallbackResult{}, fmt.Errorf("authorization failed: %s (%s)", msg, desc)
		}
		return CallbackResult{}, fmt.Errorf("authorization failed: %s", msg)
	}
	code := q.Get("code")
	if code == "" {
		return CallbackResult{}, fmt.Errorf("no code in URL; approve in the browser first, then paste the full callback URL")
	}
	if wantState != "" && q.Get("state") != wantState {
		return CallbackResult{}, fmt.Errorf("state mismatch; the pasted URL is not from this login attempt, run the command again")
	}
	var scopes []string
	if s := q.Get("scope"); s != "" {
		scopes = strings.Fields(s)
	}
	return CallbackResult{Code: code, State: q.Get("state"), ClientID: q.Get("client_id"), Scopes: scopes}, nil
}

// ParseCallback extracts the authorization code from the pasted address-bar
// URL. wantState must match the state query value when non-empty.
func ParseCallback(pasted, wantState string) (string, error) {
	cb, err := ParseCallbackFull(pasted, wantState)
	if err != nil {
		return "", err
	}
	return cb.Code, nil
}

// ResolvePath expands ~ and returns an absolute path. Empty means default.
func ResolvePath(path string) (string, error) {
	if path == "" {
		path = DefaultPath
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if path == "~" {
			path = home
		} else {
			path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	}
	return filepath.Abs(path)
}

// HostPathForOAuth derives the host-ID file next to an oauth file so
// overridden paths (tests, custom configs) stay isolated. The default oauth
// path maps to DefaultHostPath.
func HostPathForOAuth(oauthPath string) (string, error) {
	full, err := ResolvePath(oauthPath)
	if err != nil {
		return "", err
	}
	def, err := ResolvePath(DefaultPath)
	if err == nil && full == def {
		return ResolvePath(DefaultHostPath)
	}
	return filepath.Join(filepath.Dir(full), HostFileName), nil
}

// newHostID generates a urn:uuid:... v4 identifier.
func newHostID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	hex := func(v byte) byte {
		if v < 10 {
			return '0' + v
		}
		return 'a' + v - 10
	}
	var dst [36]byte
	di := 0
	for i, c := range b {
		if i == 4 || i == 6 || i == 8 || i == 10 {
			dst[di] = '-'
			di++
		}
		dst[di] = hex(c >> 4)
		dst[di+1] = hex(c & 0x0f)
		di += 2
	}
	return "urn:uuid:" + string(dst[:]), nil
}

// GetOrCreateHostID loads this host's stable ext_agent_host_id or creates and
// persists it (0600) before the first sign-in.
func GetOrCreateHostID(hostPath string) (string, error) {
	if hostPath == "" {
		hostPath = DefaultHostPath
	}
	full, err := ResolvePath(hostPath)
	if err != nil {
		return "", err
	}
	if data, err := os.ReadFile(full); err == nil {
		id := strings.TrimSpace(string(data))
		if id != "" {
			return id, nil
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("read host id: %w", err)
	}
	id, err := newHostID()
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(full)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("create host id directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".host-id-*")
	if err != nil {
		return "", fmt.Errorf("write host id: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.WriteString(id + "\n"); err != nil {
		tmp.Close()
		return "", fmt.Errorf("write host id: %w", err)
	}
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return "", fmt.Errorf("write host id: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("write host id: %w", err)
	}
	if err := os.Rename(tmpName, full); err != nil {
		return "", fmt.Errorf("write host id: %w", err)
	}
	return id, nil
}

// StartLoopbackListener binds 127.0.0.1 (preferred port, else ephemeral) and
// returns the listener plus the exact redirect_uri to use for this attempt.
func StartLoopbackListener(preferredPort int) (net.Listener, string, error) {
	if preferredPort <= 0 {
		preferredPort = DefaultRedirectPort
	}
	addr := fmt.Sprintf("%s:%d", RedirectHost, preferredPort)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		ln, err = net.Listen("tcp", RedirectHost+":0")
		if err != nil {
			return nil, "", fmt.Errorf("start loopback listener: %w", err)
		}
	}
	port := ln.Addr().(*net.TCPAddr).Port
	return ln, RedirectURIForPort(port), nil
}

// WaitForCallback serves one loopback callback on /auth/callback and returns
// the parsed result. It validates state and surfaces OAuth errors.
func WaitForCallback(ctx context.Context, ln net.Listener, wantState string) (CallbackResult, error) {
	if err := ctx.Err(); err != nil {
		return CallbackResult{}, err
	}
	type outcome struct {
		cb  CallbackResult
		err error
	}
	ch := make(chan outcome, 1)
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != RedirectPath {
				http.NotFound(w, r)
				return
			}
			// Rebuild a URL string for the shared parser.
			cb, err := ParseCallbackFull(r.URL.String(), wantState)
			if err != nil {
				http.Error(w, "Login failed; return to the terminal.", http.StatusBadRequest)
				select {
				case ch <- outcome{err: err}:
				default:
				}
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, "<html><body><h1>Signed in</h1><p>Return to the terminal.</p></body></html>")
			select {
			case ch <- outcome{cb: cb}:
			default:
			}
		}),
	}
	go func() {
		_ = srv.Serve(ln)
	}()
	defer srv.Close()
	select {
	case <-ctx.Done():
		return CallbackResult{}, ctx.Err()
	case out := <-ch:
		return out.cb, out.err
	}
}

func checkFile(stat os.FileInfo, path string) error {
	if stat.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("chatgpt oauth file %q must not be a symlink", path)
	}
	if !stat.Mode().IsRegular() {
		return fmt.Errorf("chatgpt oauth file %q must be a regular file", path)
	}
	if perm := stat.Mode().Perm(); perm != 0600 && perm != 0400 {
		return fmt.Errorf("chatgpt oauth file %q must have mode 0600 or 0400 (has %04o)", path, perm)
	}
	if runtime.GOOS == "linux" {
		native, ok := stat.Sys().(*syscall.Stat_t)
		if !ok {
			return fmt.Errorf("chatgpt oauth file %q ownership unknown", path)
		}
		if native.Uid != uint32(os.Getuid()) {
			return fmt.Errorf("chatgpt oauth file %q must be owned by the current user", path)
		}
	}
	if stat.Size() > maxFileSize {
		return fmt.Errorf("chatgpt oauth file %q exceeds %d bytes", path, maxFileSize)
	}
	return nil
}

// Load reads and validates the stored tokens.
func Load(path string) (Tokens, error) {
	full, err := ResolvePath(path)
	if err != nil {
		return Tokens{}, err
	}
	link, err := os.Lstat(full)
	if err != nil {
		return Tokens{}, fmt.Errorf("chatgpt oauth file: %w; run `qcode login chatgpt`", err)
	}
	if err := checkFile(link, full); err != nil {
		return Tokens{}, err
	}
	f, err := os.Open(full)
	if err != nil {
		return Tokens{}, fmt.Errorf("chatgpt oauth file: %w", err)
	}
	defer f.Close()
	live, err := f.Stat()
	if err != nil {
		return Tokens{}, fmt.Errorf("chatgpt oauth file: %w", err)
	}
	if err := checkFile(live, full); err != nil {
		return Tokens{}, err
	}
	data, err := io.ReadAll(io.LimitReader(f, maxFileSize+1))
	if err != nil {
		return Tokens{}, fmt.Errorf("chatgpt oauth file: %w", err)
	}
	var t Tokens
	if err := json.Unmarshal(data, &t); err != nil {
		return Tokens{}, fmt.Errorf("chatgpt oauth file is not valid JSON; run `qcode login chatgpt` again")
	}
	if t.AccessToken == "" {
		return Tokens{}, fmt.Errorf("chatgpt oauth file has no access token; run `qcode login chatgpt` again")
	}
	// Backfill explicit expiry from saved_at+expires_in when an old writer
	// omitted expires_at.
	if t.ExpiresAt == 0 {
		if exp, ok := t.ExpiryTime(); ok {
			t.ExpiresAt = exp.Unix()
		}
	}
	return t, nil
}

// Save atomically stores tokens with mode 0600.
func Save(path string, t Tokens) error {
	full, err := ResolvePath(path)
	if err != nil {
		return err
	}
	if link, err := os.Lstat(full); err == nil {
		if err := checkFile(link, full); err != nil {
			// Existing path violates strict rules (for example a symlink or
			// loose mode): refuse to follow/replace it blindly.
			return err
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("chatgpt oauth file: %w", err)
	}
	dir := filepath.Dir(full)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create oauth directory: %w", err)
	}
	if t.TokenType == "" {
		t.TokenType = "Bearer"
	}
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".chatgpt-oauth-*")
	if err != nil {
		return fmt.Errorf("write oauth file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return fmt.Errorf("write oauth file: %w", err)
	}
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return fmt.Errorf("write oauth file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write oauth file: %w", err)
	}
	if err := os.Rename(tmpName, full); err != nil {
		return fmt.Errorf("write oauth file: %w", err)
	}
	return nil
}

type tokenReply struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	AccountID    string `json:"account_id"`
	Email        string `json:"email"`
	IDToken      string `json:"id_token"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
}

func postForm(ctx context.Context, do Doer, form url.Values) (tokenReply, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return tokenReply{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := do.Do(req)
	if err != nil {
		return tokenReply{}, fmt.Errorf("token endpoint: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return tokenReply{}, fmt.Errorf("token endpoint returned %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	var reply tokenReply
	if err := json.Unmarshal(data, &reply); err != nil {
		return tokenReply{}, fmt.Errorf("token endpoint returned invalid JSON")
	}
	if reply.AccessToken == "" {
		return tokenReply{}, fmt.Errorf("token endpoint returned no access token")
	}
	return reply, nil
}

// IsInvalidGrant reports the retryable code-exchange failure where the caller
// must discard the code and start a fresh authorization.
func IsInvalidGrant(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "invalid_grant")
}

func scopesFromReply(scope string) []string {
	return strings.Fields(scope)
}

func tokensFromReply(reply tokenReply, clientID string, fallback Tokens) Tokens {
	now := time.Now().UTC()
	expires := now.Add(time.Duration(reply.ExpiresIn) * time.Second)
	if reply.ExpiresIn <= 0 {
		expires = now.Add(time.Hour)
	}
	t := Tokens{
		AccessToken:  reply.AccessToken,
		RefreshToken: reply.RefreshToken,
		ExpiresIn:    reply.ExpiresIn,
		ExpiresAt:    expires.Unix(),
		IDToken:      reply.IDToken,
		TokenType:    reply.TokenType,
		Scopes:       scopesFromReply(reply.Scope),
		SavedAt:      now.Format(time.RFC3339),
		ClientID:     clientID,
		AccountID:    accountIDFor(reply, fallback.AccountID),
		Email:        fallback.Email,
		Issuer:       fallback.Issuer,
		Subject:      fallback.Subject,
		ExtAgentHostID: fallback.ExtAgentHostID,
	}
	if t.ExpiresIn <= 0 {
		t.ExpiresIn = 3600
	}
	if t.TokenType == "" {
		t.TokenType = "Bearer"
	}
	if reply.Email != "" && t.Email == "" {
		t.Email = reply.Email
	}
	if t.IDToken == "" {
		t.IDToken = fallback.IDToken
	}
	if t.RefreshToken == "" {
		t.RefreshToken = fallback.RefreshToken
	}
	return t
}

// AccountIDFromAccessToken extracts the ChatGPT account identifier from the
// legacy Codex access-token JWT claims. The token endpoint sometimes omits the
// top-level account_id; the JWT itself carries it. Returns "" when the token
// is opaque or carries no recognized claim. New token-sharing access tokens
// carry client_id/scope instead and have no account claim; callers must not
// require one.
func AccountIDFromAccessToken(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	payload := parts[1]
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		// Lenient fallback: padded encoding.
		raw, err = base64.URLEncoding.DecodeString(payload)
		if err != nil {
			return ""
		}
	}
	var claims map[string]any
	if err := json.Unmarshal(raw, &claims); err != nil {
		return ""
	}
	for _, key := range []string{"chatgpt_account_id", "account_id", "accountId", "chatgpt-account-id"} {
		if v, ok := claims[key]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}
	// Some issuers nest the account under an auth object or a namespaced
	// claim URI. Search one level deep for the same keys.
	for _, v := range claims {
		obj, ok := v.(map[string]any)
		if !ok {
			continue
		}
		for _, key := range []string{"chatgpt_account_id", "account_id", "accountId"} {
			if s, ok := obj[key].(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}

// accountIDFor prefers the token endpoint's top-level account_id, then falls
// back to the access-token (and id-token) JWT claims, then to the previously
// stored value so rotations never blank a known account.
func accountIDFor(reply tokenReply, fallback string) string {
	if reply.AccountID != "" {
		return reply.AccountID
	}
	if id := AccountIDFromAccessToken(reply.AccessToken); id != "" {
		return id
	}
	if reply.IDToken != "" {
		if id := AccountIDFromAccessToken(reply.IDToken); id != "" {
			return id
		}
	}
	return fallback
}

// Exchange swaps an authorization code plus PKCE verifier for tokens. clientID
// is the issued client from the callback (never dynamic_agent_client) and
// redirectURI must exactly match the authorize request.
func Exchange(ctx context.Context, do Doer, code, verifier, redirectURI, clientID string) (Tokens, error) {
	if clientID == "" || clientID == DynamicClientID {
		return Tokens{}, fmt.Errorf("exchange requires the issued client_id from the callback, not dynamic_agent_client")
	}
	if redirectURI == "" {
		redirectURI = RedirectURI
	}
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", clientID)
	form.Set("redirect_uri", redirectURI)
	form.Set("code", code)
	form.Set("code_verifier", verifier)
	form.Set("resource", Resource)
	reply, err := postForm(ctx, do, form)
	if err != nil {
		return Tokens{}, err
	}
	return tokensFromReply(reply, clientID, Tokens{}), nil
}

// Refresh swaps a refresh token for new tokens. clientID is the issued client
// saved with this token set; resource is always sent and scope is omitted to
// retain the grant.
func Refresh(ctx context.Context, do Doer, refreshToken, clientID string) (Tokens, error) {
	if clientID == "" || clientID == DynamicClientID || clientID == LegacyClientID {
		return Tokens{}, fmt.Errorf("refresh requires the issued client_id; run `qcode login chatgpt` again")
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("client_id", clientID)
	form.Set("refresh_token", refreshToken)
	form.Set("resource", Resource)
	reply, err := postForm(ctx, do, form)
	if err != nil {
		return Tokens{}, err
	}
	return tokensFromReply(reply, clientID, Tokens{}), nil
}

// Per-path refresh serialization: the default max_concurrent=2 lets two chats
// share one OAuth file. Without it both Load the same near-expiry tokens and
// send the same rotating refresh, and a single-use server rejects the second
// with invalid_grant. Atomic rename only protects file completeness, not the
// read/refresh/store transaction, so hold a process-wide lock per resolved
// path plus a best-effort cross-process file lock during the transaction.
var (
	refreshMu    sync.Mutex
	refreshLocks = map[string]*sync.Mutex{}
)

func lockFor(resolved string) *sync.Mutex {
	refreshMu.Lock()
	defer refreshMu.Unlock()
	if mu, ok := refreshLocks[resolved]; ok {
		return mu
	}
	mu := &sync.Mutex{}
	refreshLocks[resolved] = mu
	return mu
}

// lockMutexContext acquires mu or returns ctx.Err(). Polling with TryLock keeps
// startup/task deadlines and cancellation working while another goroutine
// holds the per-path lock.
func lockMutexContext(ctx context.Context, mu *sync.Mutex) error {
	if mu.TryLock() {
		return nil
	}
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if mu.TryLock() {
				return nil
			}
		}
	}
}

// withRefreshFileLock holds an exclusive advisory lock on path+".lock" while
// fn runs. It is best-effort across processes; intra-process safety comes
// from lockFor. Non-Linux platforms skip the syscall.
func withRefreshFileLock(resolved string, fn func() error) error {
	return withRefreshFileLockContext(context.Background(), resolved, fn)
}

// withRefreshFileLockContext is the cancellable variant. It uses a
// non-blocking flock with retry so a stale holder cannot wedge startup
// (EnsureValid 30s), /models (15s), or task cancellation forever. The fast
// path in GetValidTokens avoids taking this lock at all when the stored
// token is still fresh.
func withRefreshFileLockContext(ctx context.Context, resolved string, fn func() error) error {
	if runtime.GOOS != "linux" {
		if err := ctx.Err(); err != nil {
			return err
		}
		return fn()
	}
	dir := filepath.Dir(resolved)
	if err := os.MkdirAll(dir, 0700); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fn()
	}
	f, err := os.OpenFile(resolved+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fn()
	}
	defer f.Close()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
			if err := ctx.Err(); err != nil {
				return err
			}
			return fn()
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			// Unexpected flock failure: best-effort fallback without the
			// cross-process lock, but still honor cancellation.
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fn()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func legacyFileError() error {
	return fmt.Errorf("chatgpt oauth file predates Sign in with ChatGPT; run `qcode login chatgpt` again")
}

func mergeRefresh(old, next Tokens) Tokens {
	if next.ClientID == "" {
		next.ClientID = old.ClientID
	}
	if next.ExtAgentHostID == "" {
		next.ExtAgentHostID = old.ExtAgentHostID
	}
	if next.IDToken == "" {
		next.IDToken = old.IDToken
	}
	if next.Issuer == "" {
		next.Issuer = old.Issuer
	}
	if next.Subject == "" {
		next.Subject = old.Subject
	}
	if next.Email == "" {
		next.Email = old.Email
	}
	if next.AccountID == "" {
		next.AccountID = old.AccountID
	}
	return next
}

// GetValidTokens returns usable credentials, refreshing and persisting when
// the stored token is near expiry. A lock-free fast path returns fresh tokens
// without taking any lock; only the refresh transaction re-reads the file
// under the cancellable per-path + file locks so concurrent callers share one
// refresh and startup/task deadlines stay effective.
func GetValidTokens(ctx context.Context, do Doer, path string) (Tokens, error) {
	return GetValidTokensWithSecrets(ctx, do, path, nil)
}

// GetValidTokensWithSecrets is GetValidTokens with an optional concurrency-safe
// callback invoked for loaded tokens before refresh and rotated tokens before
// persistence, including when refresh or persistence subsequently fails.
func GetValidTokensWithSecrets(ctx context.Context, do Doer, path string, register func(...string)) (Tokens, error) {
	if err := ctx.Err(); err != nil {
		return Tokens{}, err
	}
	// Fast path: no locks, so a stuck sibling cannot block fresh credentials
	// and cannot wedge EnsureValid / Models deadlines.
	if t, err := Load(path); err == nil {
		registerTokenSecrets(register, t)
		if t.IsLegacy() {
			return Tokens{}, legacyFileError()
		}
		if !t.NeedsRefresh(time.Now()) {
			return t, nil
		}
	} else {
		// Load failures (missing file, bad mode, bad JSON) need no
		// serialization: report them without acquiring locks.
		// Fall through to the locked path only when the file exists but
		// needs a refresh; re-check there. To keep it simple and preserve
		// error text, return Load errors directly unless refresh is needed.
		// We cannot tell without the file, so return the error.
		// However a concurrent refresh may have just fixed it; the locked
		// path re-reads. Distinguish: if the error is "needs refresh"-like
		// it never is — Load never reports expiry. So return directly.
		// The only case that needs the lock is a present-but-stale file,
		// which Load returns successfully. Hence any Load error is final.
		return Tokens{}, err
	}
	full, err := ResolvePath(path)
	if err != nil {
		return Tokens{}, err
	}
	mu := lockFor(full)
	if err := lockMutexContext(ctx, mu); err != nil {
		return Tokens{}, err
	}
	defer mu.Unlock()
	var out Tokens
	lockErr := withRefreshFileLockContext(ctx, full, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		t, err := Load(path)
		if err != nil {
			return err
		}
		registerTokenSecrets(register, t)
		if t.IsLegacy() {
			return legacyFileError()
		}
		if !t.NeedsRefresh(time.Now()) {
			out = t
			return nil
		}
		if t.RefreshToken == "" {
			return fmt.Errorf("chatgpt token expired and no refresh token; run `qcode login chatgpt` again")
		}
		next, err := Refresh(ctx, do, t.RefreshToken, t.ClientID)
		if err != nil {
			return fmt.Errorf("chatgpt token refresh failed; run `qcode login chatgpt` again: %w", err)
		}
		next = mergeRefresh(t, next)
		registerTokenSecrets(register, next)
		if err := Save(path, next); err != nil {
			return err
		}
		out = next
		return nil
	})
	if lockErr != nil {
		return Tokens{}, lockErr
	}
	return out, nil
}

// GetValidToken returns a usable access token, refreshing and persisting when
// the stored token is near expiry.
func GetValidToken(ctx context.Context, do Doer, path string) (string, error) {
	t, err := GetValidTokens(ctx, do, path)
	if err != nil {
		return "", err
	}
	return t.AccessToken, nil
}

// RefreshAndSaveWithStale forces a refresh (used for a 401 retry) but first
// re-reads under the cancellable per-path lock: if another goroutine already
// rotated past staleAccess, its fresh token is reused instead of spending the
// new refresh. A lock-free fast path covers that reuse case so 401 retries do
// not block on a stuck holder when the file is already fresh.
func RefreshAndSaveWithStale(ctx context.Context, do Doer, path, staleAccess string) (Tokens, error) {
	return RefreshAndSaveWithStaleWithSecrets(ctx, do, path, staleAccess, nil)
}

// RefreshAndSaveWithStaleWithSecrets is RefreshAndSaveWithStale with an optional concurrency-safe
// callback invoked for loaded tokens before refresh and rotated tokens before
// persistence, including when refresh or persistence subsequently fails.
func RefreshAndSaveWithStaleWithSecrets(ctx context.Context, do Doer, path, staleAccess string, register func(...string)) (Tokens, error) {
	if err := ctx.Err(); err != nil {
		return Tokens{}, err
	}
	if staleAccess != "" {
		if t, err := Load(path); err == nil {
			registerTokenSecrets(register, t)
			if !t.IsLegacy() && t.AccessToken != staleAccess && !t.NeedsRefresh(time.Now()) {
				return t, nil
			}
		}
	}
	full, err := ResolvePath(path)
	if err != nil {
		return Tokens{}, err
	}
	mu := lockFor(full)
	if err := lockMutexContext(ctx, mu); err != nil {
		return Tokens{}, err
	}
	defer mu.Unlock()
	var out Tokens
	lockErr := withRefreshFileLockContext(ctx, full, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		t, err := Load(path)
		if err != nil {
			return err
		}
		registerTokenSecrets(register, t)
		if t.IsLegacy() {
			return legacyFileError()
		}
		if staleAccess != "" && t.AccessToken != staleAccess && !t.NeedsRefresh(time.Now()) {
			out = t
			return nil
		}
		if t.RefreshToken == "" {
			return fmt.Errorf("no refresh token; run `qcode login chatgpt` again")
		}
		next, err := Refresh(ctx, do, t.RefreshToken, t.ClientID)
		if err != nil {
			return fmt.Errorf("chatgpt token refresh failed; run `qcode login chatgpt` again: %w", err)
		}
		next = mergeRefresh(t, next)
		registerTokenSecrets(register, next)
		if err := Save(path, next); err != nil {
			return err
		}
		out = next
		return nil
	})
	if lockErr != nil {
		return Tokens{}, lockErr
	}
	return out, nil
}

// RefreshAndSave forces a refresh regardless of expiry (used for a 401 retry).
func RefreshAndSave(ctx context.Context, do Doer, path string) (string, error) {
	t, err := RefreshAndSaveWithStale(ctx, do, path, "")
	if err != nil {
		return "", err
	}
	return t.AccessToken, nil
}

// EnsureValid is the startup preflight: it loads the file and, when the
// access token is expired or near expiry, refreshes it now instead of letting
// the first user task fail. A missing/unusable file or a failed refresh
// reports `run qcode login chatgpt` before serve starts.
func EnsureValid(ctx context.Context, do Doer, path string) (Tokens, error) {
	t, err := GetValidTokens(ctx, do, path)
	if err != nil {
		return Tokens{}, err
	}
	if t.AccessToken == "" {
		return Tokens{}, fmt.Errorf("chatgpt oauth file has no access token; run `qcode login chatgpt` again")
	}
	return t, nil
}

func registerTokenSecrets(register func(...string), t Tokens) {
	if register != nil {
		register(t.AccessToken, t.RefreshToken, t.IDToken)
	}
}
