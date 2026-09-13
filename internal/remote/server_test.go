package remote

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"qcode/internal/tui"
)

type testPresentation struct {
	mu          sync.Mutex
	actor       string
	line        string
	catalog     tui.RemoteCatalog
	connections chan string
	rejections  chan string
}

func (p *testPresentation) RemotePresentation() tui.RemotePresentation {
	return tui.RemotePresentation{Active: "main", Views: []tui.RemoteAgentView{{ID: "main", Name: "main", Lines: []string{"hello"}}}}
}
func (p *testPresentation) RemoteCatalog(context.Context) tui.RemoteCatalog {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.catalog
}
func (p *testPresentation) SubscribePresentation(ctx context.Context) <-chan struct{} {
	ch := make(chan struct{})
	go func() { <-ctx.Done(); close(ch) }()
	return ch
}
func (p *testPresentation) SubmitRemote(actor, line string) error {
	p.mu.Lock()
	p.actor, p.line = actor, line
	p.mu.Unlock()
	return nil
}
func (p *testPresentation) ResolveRemoteInteraction(actor, id string, value []byte) error {
	p.mu.Lock()
	p.actor, p.line = actor, id+":"+string(value)
	p.mu.Unlock()
	return nil
}
func (p *testPresentation) RemoteConnection(actor string, connected bool) {
	if p.connections == nil {
		return
	}
	event := "disconnect"
	if connected {
		event = "connect"
	}
	p.connections <- event + ":" + actor
}
func (p *testPresentation) RemoteRequestRejected(method, path, reason string) {
	if p.rejections == nil {
		return
	}
	p.rejections <- method + " " + path + ":" + reason
}

func newTestHandler(t *testing.T) (http.Handler, *testPresentation) {
	t.Helper()
	presentation := &testPresentation{}
	return New(presentation).routes(), presentation
}

func TestRequiresNamedTailscaleIdentity(t *testing.T) {
	handler, _ := newTestHandler(t)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/snapshot", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
}

func TestReportsRejectedRemoteRequest(t *testing.T) {
	presentation := &testPresentation{rejections: make(chan string, 1)}
	handler := New(presentation).routes()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/events", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
	select {
	case rejection := <-presentation.rejections:
		want := "GET /api/v1/events:missing Tailscale-User-Login"
		if rejection != want {
			t.Fatalf("rejection = %q, want %q", rejection, want)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for rejected request")
	}
}

func TestSnapshotIncludesRuntimeAndPresentation(t *testing.T) {
	handler, _ := newTestHandler(t)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/snapshot", nil)
	request.Header.Set(identityHeader, "alice@example.com")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"actor":"alice@example.com"`) || !strings.Contains(response.Body.String(), `"hello"`) {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
}

func TestCatalogIncludesSelectorData(t *testing.T) {
	presentation := &testPresentation{catalog: tui.RemoteCatalog{
		Models:   []string{"model-a"},
		Thinking: map[string]tui.RemoteThinkingState{"model-a": {Levels: []string{"low", "high"}, Current: "low"}},
		Skills:   []tui.RemoteSkillState{{Name: "review", Selected: true}},
		Sessions: []tui.RemoteSessionState{{ID: "session-1", Preview: "Review the release"}},
	}}
	handler := New(presentation).routes()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/catalog", nil)
	request.Header.Set(identityHeader, "alice@example.com")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
	for _, fragment := range []string{`"model-a"`, `"low"`, `"review"`, `"selected":true`, `"session-1"`} {
		if !strings.Contains(response.Body.String(), fragment) {
			t.Fatalf("catalog response = %s, missing %s", response.Body.String(), fragment)
		}
	}
}

func TestRemotePageHasCatalogBackedSelectorControls(t *testing.T) {
	for _, fragment := range []string{
		"api/v1/catalog",
		"data.thinking",
		"Select thinking level",
		"Select skills",
		"Resume session",
		"Switch agent",
		"Type to filter",
		"No matching entries.",
		"submitLines",
		"e.key==='Escape'",
		"viewport-fit=cover",
		"100dvh",
		"@media (max-width: 480px)",
		"grid-template-columns: 1fr",
	} {
		if !strings.Contains(indexHTML, fragment) {
			t.Fatalf("remote page is missing selector behavior %q", fragment)
		}
	}
}

func TestEventsReportConnectionLifecycle(t *testing.T) {
	presentation := &testPresentation{connections: make(chan string, 2)}
	manager := New(presentation)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/events", nil).WithContext(ctx)
	request.Header.Set(identityHeader, "alice@example.com")
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		manager.routes().ServeHTTP(response, request)
		close(done)
	}()

	select {
	case event := <-presentation.connections:
		if event != "connect:alice@example.com" {
			t.Fatalf("connect event = %q", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for connect event")
	}
	cancel()
	select {
	case event := <-presentation.connections:
		if event != "disconnect:alice@example.com" {
			t.Fatalf("disconnect event = %q", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for disconnect event")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("events handler did not stop")
	}
}

func TestPrefixedServePathRoutesToAPI(t *testing.T) {
	presentation := &testPresentation{}
	handler := New(presentation).routes("/qcode/session")
	request := httptest.NewRequest(http.MethodGet, "/qcode/session/api/v1/snapshot", nil)
	request.Header.Set(identityHeader, "alice@example.com")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"hello"`) {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
}

func TestPrefixedServePathRedirectsMissingTrailingSlash(t *testing.T) {
	handler := New(&testPresentation{}).routes("/qcode/session")
	request := httptest.NewRequest(http.MethodGet, "/qcode/session", nil)
	request.Header.Set(identityHeader, "alice@example.com")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusPermanentRedirect {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusPermanentRedirect)
	}
	if location := response.Header().Get("Location"); location != "/qcode/session/" {
		t.Fatalf("Location = %q, want %q", location, "/qcode/session/")
	}
}

func TestWatchServeOutput(t *testing.T) {
	ready := make(chan error, 1)
	watchServeOutput(strings.NewReader("Available within your tailnet:\nhttps://host.example.ts.net\n"), ready)
	if err := <-ready; err != nil {
		t.Fatal(err)
	}
	failed := make(chan error, 1)
	watchServeOutput(strings.NewReader("serve needs setup\n"), failed)
	if err := <-failed; err == nil || !strings.Contains(err.Error(), "needs setup") {
		t.Fatalf("error = %v", err)
	}
}

func TestAnnotateTailscaleErrorAddsOperatorSetupHint(t *testing.T) {
	annotated := annotateTailscaleError(errors.New("Access denied: serve config denied\n\nUse 'sudo tailscale serve --https=443 --set-path=/qcode/abc http://127.0.0.1:1234'."))
	if annotated.Error() != tailscaleOperatorHint {
		t.Fatalf("annotated error = %q, want %q", annotated, tailscaleOperatorHint)
	}

	unchanged := annotateTailscaleError(errors.New("tailscale is disconnected"))
	if strings.Contains(unchanged.Error(), tailscaleOperatorHint) {
		t.Fatalf("unrelated error received operator hint: %q", unchanged)
	}

	alreadyHinted := annotateTailscaleError(errors.New("permission denied; run tailscale set --operator=alice"))
	if alreadyHinted.Error() != tailscaleOperatorHint {
		t.Fatalf("operator error = %q, want %q", alreadyHinted, tailscaleOperatorHint)
	}
}

func TestRandomIDIsTwoLowercaseLettersOrDigits(t *testing.T) {
	id, err := randomID()
	if err != nil {
		t.Fatal(err)
	}
	if len(id) != 2 {
		t.Fatalf("ID length = %d, want 2", len(id))
	}
	for _, char := range id {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') {
			t.Fatalf("ID is not lowercase letters or digits: %q", id)
		}
	}
}

func TestActionIsAttributedAndSameOrigin(t *testing.T) {
	handler, presentation := newTestHandler(t)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/actions", bytes.NewBufferString(`{"line":"fix it"}`))
	request.Host = "host.tailnet.ts.net"
	request.Header.Set(identityHeader, "alice@example.com")
	request.Header.Set("Origin", "https://host.tailnet.ts.net")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
	presentation.mu.Lock()
	if presentation.actor != "alice@example.com" || presentation.line != "fix it" {
		t.Fatalf("action = (%q, %q)", presentation.actor, presentation.line)
	}
	presentation.mu.Unlock()

	rejected := httptest.NewRequest(http.MethodPost, "/api/v1/actions", bytes.NewBufferString(`{"line":"fix it"}`))
	rejected.Host = "host.tailnet.ts.net"
	rejected.Header.Set(identityHeader, "alice@example.com")
	rejected.Header.Set("Origin", "https://evil.example")
	rejectedResponse := httptest.NewRecorder()
	handler.ServeHTTP(rejectedResponse, rejected)
	if rejectedResponse.Code != http.StatusForbidden {
		t.Fatalf("cross-origin status = %d", rejectedResponse.Code)
	}
}

func TestRemoteResolvesSharedInteraction(t *testing.T) {
	handler, presentation := newTestHandler(t)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/interactions/interaction-1/resolve", bytes.NewBufferString(`{"value":["Postgres"]}`))
	request.Host = "host.tailnet.ts.net"
	request.Header.Set(identityHeader, "alice@example.com")
	request.Header.Set("Origin", "https://host.tailnet.ts.net")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
	presentation.mu.Lock()
	defer presentation.mu.Unlock()
	if presentation.actor != "alice@example.com" || presentation.line != `interaction-1:["Postgres"]` {
		t.Fatalf("resolution = (%q, %q)", presentation.actor, presentation.line)
	}
}
