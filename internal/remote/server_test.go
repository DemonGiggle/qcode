package remote

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"qcode/internal/tui"
)

type testPresentation struct {
	mu    sync.Mutex
	actor string
	line  string
}

func (p *testPresentation) RemotePresentation() tui.RemotePresentation {
	return tui.RemotePresentation{Active: "main", Views: []tui.RemoteAgentView{{ID: "main", Name: "main", Lines: []string{"hello"}}}}
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
