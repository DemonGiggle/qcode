package remote

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"qcode/internal/tui"
)

const identityHeader = "Tailscale-User-Login"

type presentation interface {
	RemotePresentation() tui.RemotePresentation
	RemoteCatalog(context.Context) tui.RemoteCatalog
	SubscribePresentation(context.Context) <-chan struct{}
	SubmitRemote(string, string) error
	ResolveRemoteInteraction(string, string, []byte) error
}

type remoteConnectionLogger interface {
	RemoteConnection(string, bool)
}

type remoteRequestLogger interface {
	RemoteRequestRejected(string, string, string)
}

type Manager struct {
	ui presentation

	lifecycleMu sync.Mutex
	mu          sync.Mutex
	server      *http.Server
	listener    net.Listener
	serve       *exec.Cmd
	serveDone   chan struct{}
	command     string
	ip          string
	url         string
	clients     int
	auth        *authStore
}

func New(ui presentation) *Manager { return &Manager{ui: ui, auth: newAuthStore()} }

func (m *Manager) Start(ctx context.Context) (string, error) {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	m.mu.Lock()
	if m.server != nil {
		url := m.url
		m.mu.Unlock()
		return url, nil
	}
	m.mu.Unlock()

	dnsName, tailscaleIP, err := tailscaleEndpoint(ctx)
	if err != nil {
		return "", err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("listen on loopback: %w", err)
	}
	id, err := randomID()
	if err != nil {
		_ = listener.Close()
		return "", err
	}
	prefix := "/qcode/" + id
	auth := newAuthStore()
	started := false
	defer func() {
		if !started {
			auth.close()
		}
	}()
	server := &http.Server{Handler: m.routesWithAuth(auth, prefix), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	go func() { _ = server.Serve(listener) }()
	target := "http://" + listener.Addr().String()
	cmd := exec.CommandContext(ctx, "tailscale", "serve", "--https=443", "--set-path="+prefix, target)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = server.Close()
		_ = listener.Close()
		return "", err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		_ = server.Close()
		_ = listener.Close()
		return "", fmt.Errorf("start tailscale serve: %w", annotateTailscaleError(err))
	}
	ready := make(chan error, 1)
	go watchServeOutput(stdout, ready)
	select {
	case err := <-ready:
		if err != nil {
			_ = cmd.Process.Kill()
			_, _ = io.Copy(io.Discard, stdout)
			_ = cmd.Wait()
			_ = server.Close()
			_ = listener.Close()
			return "", annotateTailscaleError(err)
		}
	case <-time.After(15 * time.Second):
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = server.Close()
		_ = listener.Close()
		return "", errors.New("tailscale serve did not become ready; run `tailscale serve` once to complete HTTPS setup")
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = server.Close()
		_ = listener.Close()
		return "", ctx.Err()
	}

	url := remoteURL(dnsName, prefix)
	m.mu.Lock()
	if m.server != nil {
		m.mu.Unlock()
		_ = cmd.Process.Signal(os.Interrupt)
		_ = cmd.Wait()
		_ = server.Close()
		_ = listener.Close()
		return m.url, nil
	}
	done := make(chan struct{})
	if m.auth != nil {
		m.auth.close()
	}
	m.auth = auth
	started = true
	m.server, m.listener, m.serve, m.serveDone, m.command, m.ip, m.url = server, listener, cmd, done, strings.Join(cmd.Args, " "), tailscaleIP, url
	m.mu.Unlock()
	go func() {
		defer close(done)
		_ = cmd.Wait()
		m.mu.Lock()
		if m.serve == cmd {
			auth.close()
			staleServer, staleListener := m.server, m.listener
			m.serve = nil
			m.server, m.listener, m.serveDone, m.command, m.ip, m.url = nil, nil, nil, "", "", ""
			m.mu.Unlock()
			_ = staleServer.Close()
			_ = staleListener.Close()
			return
		}
		m.mu.Unlock()
	}()
	return url, nil
}

// remoteURL is the browser-facing address for the Serve mount. The web page
// sets its own base URL so relative API paths work without a trailing slash.
func remoteURL(dnsName, prefix string) string {
	return "https://" + strings.TrimSuffix(dnsName, ".") + prefix
}

func watchServeOutput(reader io.Reader, ready chan<- error) {
	scanner := bufio.NewScanner(reader)
	var lines []string
	for scanner.Scan() {
		line := scanner.Text()
		lines = append(lines, line)
		if strings.Contains(line, "Available within your tailnet") {
			ready <- nil
			return
		}
	}
	message := strings.TrimSpace(strings.Join(lines, "\n"))
	if message == "" {
		message = "tailscale serve exited before becoming ready"
	}
	ready <- errors.New(message)
}

func tailscaleEndpoint(ctx context.Context) (string, string, error) {
	command := exec.CommandContext(ctx, "tailscale", "status", "--json")
	output, err := command.CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if detail == "" {
			detail = err.Error()
		}
		return "", "", fmt.Errorf("tailscale is unavailable or disconnected: %w", annotateTailscaleError(errors.New(detail)))
	}
	var status struct {
		BackendState string
		Self         struct {
			DNSName      string
			TailscaleIPs []string
		}
	}
	if err := json.Unmarshal(output, &status); err != nil {
		return "", "", fmt.Errorf("decode tailscale status: %w", err)
	}
	if status.BackendState != "Running" {
		return "", "", fmt.Errorf("tailscale is not connected (state %q)", status.BackendState)
	}
	if status.Self.DNSName == "" {
		return "", "", errors.New("tailscale status did not report a MagicDNS name")
	}
	for _, ip := range status.Self.TailscaleIPs {
		if !strings.Contains(ip, ":") {
			return status.Self.DNSName, ip, nil
		}
	}
	return status.Self.DNSName, "", nil
}

const tailscaleOperatorHint = "Tailscale needs permission to enable remote control. Run `sudo tailscale set --operator=$USER` once, then retry `/remote`."

func annotateTailscaleError(err error) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	lower := strings.ToLower(message)
	if !strings.Contains(lower, "access denied") &&
		!strings.Contains(lower, "permission") &&
		!strings.Contains(lower, "not authorized") &&
		!strings.Contains(lower, "unauthorized") &&
		!strings.Contains(lower, "must be root") &&
		!strings.Contains(lower, "operator") {
		return err
	}
	return errors.New(tailscaleOperatorHint)
}

func randomID() (string, error) {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	var id strings.Builder
	id.Grow(2)
	for range 2 {
		value, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			return "", fmt.Errorf("create remote path: %w", err)
		}
		id.WriteByte(alphabet[value.Int64()])
	}
	return id.String(), nil
}

func (m *Manager) Stop() error {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	m.mu.Lock()
	server, listener, cmd, done := m.server, m.listener, m.serve, m.serveDone
	if m.auth != nil {
		m.auth.close()
	}
	m.server, m.listener, m.serve, m.serveDone, m.command, m.ip, m.url = nil, nil, nil, nil, "", "", ""
	m.mu.Unlock()
	if server == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = server.Shutdown(ctx)
	if listener != nil {
		_ = listener.Close()
	}
	if cmd != nil && cmd.Process != nil {
		if err := cmd.Process.Signal(os.Interrupt); err != nil {
			_ = cmd.Process.Kill()
		}
		if done != nil {
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				_ = cmd.Process.Kill()
				<-done
			}
		}
	}
	return nil
}

func (m *Manager) Status() (bool, string, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.server != nil && m.serve != nil, m.url, m.clients
}

// ServeCommand returns the Tailscale command that backs the active remote-control session.
func (m *Manager) ServeCommand() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.command
}

// TailscaleIP returns this node's IPv4 Tailscale address while remote control is active.
func (m *Manager) TailscaleIP() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ip
}

func (m *Manager) routes(prefix ...string) http.Handler {
	m.mu.Lock()
	auth := m.auth
	m.mu.Unlock()
	return m.routesWithAuth(auth, prefix...)
}

func (m *Manager) routesWithAuth(auth *authStore, prefix ...string) http.Handler {
	base := ""
	if len(prefix) > 0 {
		base = strings.TrimSuffix(prefix[0], "/")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		m.index(w, base+"/")
	})
	mux.HandleFunc("POST /api/v1/login", auth.exchange)
	api := http.NewServeMux()
	api.HandleFunc("GET /api/v1/snapshot", m.snapshot)
	api.HandleFunc("GET /api/v1/catalog", m.catalog)
	api.HandleFunc("GET /api/v1/events", m.events)
	api.HandleFunc("POST /api/v1/actions", m.action)
	api.HandleFunc("POST /api/v1/interactions/{id}/resolve", m.resolveInteraction)
	mux.Handle("/api/v1/", auth.requireSession(m, api))
	handler := m.authenticate(mux)
	if base == "" {
		return handler
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == base {
			r = r.Clone(r.Context())
			r.URL.Path, r.URL.RawPath = "/", ""
		}
		if strings.HasPrefix(r.URL.Path, base+"/") {
			http.StripPrefix(base, handler).ServeHTTP(w, r)
			return
		}
		handler.ServeHTTP(w, r)
	})
}

func (m *Manager) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity := strings.TrimSpace(r.Header.Get(identityHeader))
		if identity == "" {
			m.reportRejectedRequest(r, "missing Tailscale-User-Login")
			http.Error(w, "a named Tailscale user identity is required", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodGet {
			origin := r.Header.Get("Origin")
			if origin != "" && origin != "https://"+r.Host {
				m.reportRejectedRequest(r, "cross-origin request")
				http.Error(w, "cross-origin request rejected", http.StatusForbidden)
				return
			}
		}
		r.Header.Set("X-Qcode-Actor", identity)
		next.ServeHTTP(w, r)
	})
}

func (m *Manager) reportRejectedRequest(r *http.Request, reason string) {
	logger, ok := m.ui.(remoteRequestLogger)
	if ok {
		logger.RemoteRequestRejected(r.Method, r.URL.Path, reason)
	}
}

func (m *Manager) index(w http.ResponseWriter, basePath string) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'")
	w.Header().Set("Cache-Control", "no-store")
	_ = indexTemplate.Execute(w, basePath)
}

func (m *Manager) snapshot(w http.ResponseWriter, r *http.Request) {
	presentation := m.ui.RemotePresentation()
	writeJSON(w, http.StatusOK, map[string]any{
		"runtime":      map[string]any{"sequence": presentation.Sequence, "agents": presentation.Agents, "interactions": presentation.Interactions},
		"presentation": presentation, "actor": r.Header.Get("X-Qcode-Actor"),
	})
}

func (m *Manager) catalog(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, m.ui.RemoteCatalog(ctx))
}

func (m *Manager) action(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Line string `json:"line"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		http.Error(w, "invalid action", http.StatusBadRequest)
		return
	}
	if err := m.ui.SubmitRemote(r.Header.Get("X-Qcode-Actor"), request.Line); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"accepted": true})
}

func (m *Manager) resolveInteraction(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Value json.RawMessage `json:"value"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || len(request.Value) == 0 || !json.Valid(request.Value) {
		http.Error(w, "invalid interaction resolution", http.StatusBadRequest)
		return
	}
	err := m.ui.ResolveRemoteInteraction(r.Header.Get("X-Qcode-Actor"), r.PathValue("id"), request.Value)
	if err != nil && strings.Contains(err.Error(), "already resolved") {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if err != nil && strings.Contains(err.Error(), "not found") {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"resolved": true})
}

func (m *Manager) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	m.mu.Lock()
	m.clients++
	m.mu.Unlock()
	defer func() { m.mu.Lock(); m.clients--; m.mu.Unlock() }()
	if logger, ok := m.ui.(remoteConnectionLogger); ok {
		actor := r.Header.Get("X-Qcode-Actor")
		logger.RemoteConnection(actor, true)
		defer logger.RemoteConnection(actor, false)
	}
	ctx := r.Context()
	presentation := m.ui.SubscribePresentation(ctx)
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	_, _ = io.WriteString(w, "event: refresh\ndata: {}\n\n")
	flusher.Flush()
	for {
		select {
		case _, ok := <-presentation:
			if !ok {
				return
			}
			_, _ = io.WriteString(w, "event: refresh\ndata: {}\n\n")
		case <-ticker.C:
			_, _ = io.WriteString(w, ": heartbeat\n\n")
		case <-ctx.Done():
			return
		}
		flusher.Flush()
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
