package remote

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"qcode/internal/tui"
)

const loginLifetime = 3 * time.Minute

// Each listener owns an independent store. Closing it revokes credentials and
// cancels authenticated requests, including long-lived event streams.
type authStore struct {
	mu       sync.Mutex
	now      func() time.Time
	ctx      context.Context
	cancel   context.CancelFunc
	closed   bool
	login    [32]byte
	expires  time.Time
	used     bool
	sessions map[[32]byte]string
}

func newAuthStore() *authStore {
	ctx, cancel := context.WithCancel(context.Background())
	return &authStore{now: time.Now, ctx: ctx, cancel: cancel, sessions: make(map[[32]byte]string)}
}

func newCredential() (string, error) {
	var data [32]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data[:]), nil
}

func (a *authStore) issue(baseURL string) (tui.RemoteLogin, error) {
	token, err := newCredential()
	if err != nil {
		return tui.RemoteLogin{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return tui.RemoteLogin{}, errors.New("remote control is off")
	}
	a.login, a.expires, a.used = sha256.Sum256([]byte(token)), a.now().Add(loginLifetime), false
	return tui.RemoteLogin{URL: baseURL + "#login=" + token, ExpiresAt: a.expires}, nil
}

func (a *authStore) state() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case a.closed || a.expires.IsZero():
		return "none"
	case a.used:
		return "used"
	case !a.now().Before(a.expires):
		return "expired"
	default:
		return "available"
	}
}

func (a *authStore) close() {
	a.mu.Lock()
	a.closed = true
	a.login = [32]byte{}
	a.sessions = nil
	a.cancel()
	a.mu.Unlock()
}

func authError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}

func (a *authStore) exchange(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Token string `json:"token"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || len(request.Token) != 43 || decoder.Decode(new(any)) != io.EOF {
		authError(w, http.StatusUnauthorized, "invalid_link")
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed || a.expires.IsZero() || sha256.Sum256([]byte(request.Token)) != a.login {
		authError(w, http.StatusUnauthorized, "invalid_link")
		return
	}
	if a.used {
		authError(w, http.StatusGone, "used_link")
		return
	}
	if !a.now().Before(a.expires) {
		authError(w, http.StatusGone, "expired_link")
		return
	}
	key, err := newCredential()
	if err != nil {
		authError(w, http.StatusInternalServerError, "login_failed")
		return
	}
	a.used = true
	a.sessions[sha256.Sum256([]byte(key))] = r.Header.Get("X-Qcode-Actor")
	writeJSON(w, http.StatusOK, map[string]string{"session_key": key})
}

func (a *authStore) requireSession(m *Manager, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		a.mu.Lock()
		identity, found := a.sessions[sha256.Sum256([]byte(key))]
		valid := ok && len(key) == 43 && found && !a.closed && identity == r.Header.Get("X-Qcode-Actor")
		a.mu.Unlock()
		if !valid {
			m.reportRejectedRequest(r, "invalid remote session")
			authError(w, http.StatusUnauthorized, "invalid_session")
			return
		}
		ctx, cancel := context.WithCancel(r.Context())
		stop := context.AfterFunc(a.ctx, cancel)
		defer stop()
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (m *Manager) IssueLogin(ctx context.Context) (tui.RemoteLogin, error) {
	if err := ctx.Err(); err != nil {
		return tui.RemoteLogin{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.server == nil || m.auth == nil {
		return tui.RemoteLogin{}, errors.New("remote control is off")
	}
	return m.auth.issue(m.url)
}

func (m *Manager) LoginState() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.auth == nil {
		return "none"
	}
	return m.auth.state()
}
