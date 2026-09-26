package remote

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/html"

	"qcode/internal/agent"
	"qcode/internal/session"
	"qcode/internal/tui"
)

type testPresentation struct {
	mu             sync.Mutex
	actor          string
	line           string
	catalog        tui.RemoteCatalog
	connections    chan string
	rejections     chan string
	exportDocument tui.ExportDocument
	exportErr      error
	exportMode     string
}

func (p *testPresentation) GenerateExport(mode string) (tui.ExportDocument, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.exportMode = mode
	return p.exportDocument, p.exportErr
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
	return authorizedTestManager(presentation).routes(), presentation
}

const testSessionKey = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func authorizedTestManager(p presentation) *Manager {
	m := New(p)
	m.auth.sessions[sha256.Sum256([]byte(testSessionKey))] = "alice@example.com"
	return m
}

func authorizeTestRequest(r *http.Request) {
	r.Header.Set(identityHeader, "alice@example.com")
	r.Header.Set("Authorization", "Bearer "+testSessionKey)
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

func TestPureWebUsesQRSessionWithoutTailscaleIdentity(t *testing.T) {
	presentation := &testPresentation{}
	manager := New(presentation)
	auth := newAuthStore(tui.RemoteModePureWeb)
	manager.auth = auth
	handler := manager.routesWithAuth(auth)
	login, err := auth.issue("http://192.168.1.10:1234")
	if err != nil {
		t.Fatal(err)
	}
	token := strings.SplitN(login.URL, "#login=", 2)[1]
	body := bytes.NewBufferString(`{"token":"` + token + `"}`)
	loginRequest := httptest.NewRequest(http.MethodPost, "/api/v1/login", body)
	loginRequest.Host = "192.168.1.10:1234"
	loginRequest.Header.Set("Origin", "http://192.168.1.10:1234")
	loginResponse := httptest.NewRecorder()
	handler.ServeHTTP(loginResponse, loginRequest)
	if loginResponse.Code != http.StatusOK {
		t.Fatalf("Pure Web login = %d %s", loginResponse.Code, loginResponse.Body.String())
	}
	var credentials struct {
		Key string `json:"session_key"`
	}
	if err := json.NewDecoder(loginResponse.Body).Decode(&credentials); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/snapshot", nil)
	request.Header.Set("Authorization", "Bearer "+credentials.Key)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"actor":"pure-web"`) {
		t.Fatalf("Pure Web snapshot = %d %s", response.Code, response.Body.String())
	}

	rejected := httptest.NewRequest(http.MethodPost, "/api/v1/actions", bytes.NewBufferString(`{"line":"hello"}`))
	rejected.Host = "192.168.1.10:1234"
	rejected.Header.Set("Authorization", "Bearer "+credentials.Key)
	rejected.Header.Set("Origin", "http://evil.example")
	rejectedResponse := httptest.NewRecorder()
	handler.ServeHTTP(rejectedResponse, rejected)
	if rejectedResponse.Code != http.StatusForbidden {
		t.Fatalf("Pure Web cross-origin status = %d", rejectedResponse.Code)
	}
}

func TestPureWebOpenAllowsDirectUnauthenticatedControl(t *testing.T) {
	presentation := &testPresentation{}
	manager := New(presentation)
	auth := newAuthStore(tui.RemoteModePureWebOpen)
	manager.auth = auth
	handler := manager.routesWithAuth(auth)
	login, err := auth.issue("http://192.168.1.10:1234")
	if err != nil {
		t.Fatal(err)
	}
	if !login.OpenAccess || login.URL != "http://192.168.1.10:1234" || login.ExpiresAt != (time.Time{}) {
		t.Fatalf("open remote link = %#v", login)
	}

	snapshot := httptest.NewRequest(http.MethodGet, "/api/v1/snapshot", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, snapshot)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"actor":"pure-web-open"`) {
		t.Fatalf("open remote snapshot = %d %s", response.Code, response.Body.String())
	}

	action := httptest.NewRequest(http.MethodPost, "/api/v1/actions", bytes.NewBufferString(`{"line":"hello"}`))
	action.Host = "192.168.1.10:1234"
	action.Header.Set("Origin", "http://192.168.1.10:1234")
	actionResponse := httptest.NewRecorder()
	handler.ServeHTTP(actionResponse, action)
	if actionResponse.Code != http.StatusAccepted {
		t.Fatalf("open remote action = %d %s", actionResponse.Code, actionResponse.Body.String())
	}
	presentation.mu.Lock()
	defer presentation.mu.Unlock()
	if presentation.actor != "pure-web-open" || presentation.line != "hello" {
		t.Fatalf("open remote action = (%q, %q)", presentation.actor, presentation.line)
	}

	page := httptest.NewRecorder()
	handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/", nil))
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "authRequired= false") {
		t.Fatalf("open remote page = %d %s", page.Code, page.Body.String())
	}
}

func TestPureWebStatusDoesNotRequireTailscaleServe(t *testing.T) {
	manager := New(&testPresentation{})
	manager.server = &http.Server{}
	manager.mode = tui.RemoteModePureWeb
	manager.url = "http://192.168.1.10:1234"
	status := manager.Status()
	if !status.Running || status.Mode != tui.RemoteModePureWeb || status.URL != "http://192.168.1.10:1234" {
		t.Fatalf("Pure Web status = %#v", status)
	}
	if manager.serve != nil || manager.ServeCommand() != "" {
		t.Fatal("Pure Web unexpectedly started tailscale serve")
	}
}

func TestSelectedLANIPv4RejectsUnknownAddress(t *testing.T) {
	if _, err := selectedLANIPv4("203.0.113.99"); err == nil {
		t.Fatal("unknown LAN address was accepted")
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
	authorizeTestRequest(request)
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
	handler := authorizedTestManager(presentation).routes()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/catalog", nil)
	authorizeTestRequest(request)
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

func TestRemotePageShowsWaitingIndicator(t *testing.T) {
	for _, fragment := range []string{
		`<div id="waiting" class="waiting"`,
		`#waiting`,
		`.waiting-cancel`,
		"spinnerFrames=['⠋'",
		"'Waiting ('",
		"' · '+waitingQueued+' queued'",
		"'/agent cancel '+active",
		"setInterval(()=>{if(!waitingRunning)return;waitingFrame++;drawWaiting()},100)",
	} {
		if !strings.Contains(indexHTML, fragment) {
			t.Fatalf("remote page is missing waiting indicator behavior %q", fragment)
		}
	}
}

func TestRemotePageHasAccessibleQueuedPromptPanel(t *testing.T) {
	for _, fragment := range []string{
		`id="queue-panel"`, `id="queue-toggle"`, `aria-expanded="false"`,
		`id="queue-list"`, `role="list"`, `tabindex="-1"`,
		`view.queued_prompts`, `row.textContent=(index+1)+'. '+String(item.prompt||'')`,
		`queuePanel.classList.toggle('expanded',expanded)`,
		`queueList.scrollTop+=`, `e.altKey&&e.key.toLowerCase()==='q'`,
	} {
		if !strings.Contains(indexHTML, fragment) {
			t.Fatalf("remote page is missing queue panel behavior %q", fragment)
		}
	}
}

func TestEventsReportConnectionLifecycle(t *testing.T) {
	presentation := &testPresentation{connections: make(chan string, 2)}
	manager := authorizedTestManager(presentation)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/events", nil).WithContext(ctx)
	authorizeTestRequest(request)
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
	handler := authorizedTestManager(presentation).routes("/qcode/session")
	request := httptest.NewRequest(http.MethodGet, "/qcode/session/api/v1/snapshot", nil)
	authorizeTestRequest(request)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"hello"`) {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
}

func TestRemotePageURLs(t *testing.T) {
	for _, test := range []struct {
		name        string
		prefix      string
		browserPath string
		backendPath string
	}{
		{"bare path", "/qcode/ab", "/qcode/ab", "/qcode/ab"},
		{"trailing slash", "/qcode/ab", "/qcode/ab/", "/qcode/ab/"},
		{"query and fragment", "/qcode/ab", "/qcode/ab?view=main#transcript", "/qcode/ab?view=main"},
		{"Serve strips bare path", "/qcode/ab", "/qcode/ab", "/"},
		{"Serve strips trailing slash", "/qcode/ab", "/qcode/ab/", "/"},
		{"Serve strips path with query", "/qcode/ab", "/qcode/ab?view=main#transcript", "/?view=main"},
		{"root", "", "/", "/"},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler := New(&testPresentation{}).routes(test.prefix)
			request := httptest.NewRequest(http.MethodGet, test.backendPath, nil)
			request.Header.Set(identityHeader, "alice@example.com")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
			}
			if location := response.Header().Get("Location"); location != "" {
				t.Fatalf("unexpected redirect to %q", location)
			}
			if !strings.Contains(response.Body.String(), "qcode remote") {
				t.Fatal("response is missing the remote page")
			}

			// Resolve relative API URLs as a browser would. Tailscale strips the
			// mount before proxying, so the backend cannot use the request path
			// to determine the page's public base URL.
			browserURL, err := url.Parse("https://host.tailnet.ts.net" + test.browserPath)
			if err != nil {
				t.Fatal(err)
			}
			baseURL := browserURL
			tokenizer := html.NewTokenizer(strings.NewReader(response.Body.String()))
			for tokenType := tokenizer.Next(); tokenType != html.ErrorToken; tokenType = tokenizer.Next() {
				if tokenType != html.StartTagToken && tokenType != html.SelfClosingTagToken {
					continue
				}
				token := tokenizer.Token()
				if token.Data != "base" {
					continue
				}
				for _, attr := range token.Attr {
					if attr.Key == "href" {
						baseURL, err = browserURL.Parse(attr.Val)
						if err != nil {
							t.Fatal(err)
						}
					}
				}
				break
			}
			for _, endpoint := range []string{"snapshot", "catalog", "events", "actions", "interactions/interaction-1/resolve"} {
				got, err := baseURL.Parse("api/v1/" + endpoint)
				if err != nil {
					t.Fatal(err)
				}
				want := "https://host.tailnet.ts.net" + test.prefix + "/api/v1/" + endpoint
				if got.String() != want {
					t.Errorf("browser API URL = %q, want %q", got, want)
				}
			}
		})
	}
}

func TestRemoteURLHasNoTrailingSlash(t *testing.T) {
	got := remoteURL("host.tailnet.ts.net.", "/qcode/ab")
	want := "https://host.tailnet.ts.net/qcode/ab"
	if got != want {
		t.Fatalf("remoteURL = %q, want %q", got, want)
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
	authorizeTestRequest(request)
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
	authorizeTestRequest(rejected)
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
	authorizeTestRequest(request)
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

func TestExportDownloadUsesSharedDocument(t *testing.T) {
	for _, mode := range []string{"", "pretty", "raw"} {
		t.Run(mode, func(t *testing.T) {
			handler, p := newTestHandler(t)
			p.exportDocument = tui.ExportDocument{Filename: "qcode-session-pretty-20260922-120000-000000001.html", Data: []byte("<!doctype html><p>Shared export</p>")}
			request := httptest.NewRequest(http.MethodGet, "/api/v1/export?mode="+mode, nil)
			authorizeTestRequest(request)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusOK || response.Body.String() != string(p.exportDocument.Data) || p.exportMode != mode {
				t.Fatalf("download: %d %s mode=%q", response.Code, response.Body.String(), p.exportMode)
			}
			if response.Header().Get("Content-Type") != "text/html; charset=utf-8" || response.Header().Get("Cache-Control") != "no-store" || !strings.Contains(response.Header().Get("Content-Disposition"), p.exportDocument.Filename) {
				t.Fatalf("headers: %v", response.Header())
			}
		})
	}
}

func TestExportDownloadErrorsAndAuthorization(t *testing.T) {
	handler, p := newTestHandler(t)
	for _, authorized := range []bool{false, true} {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/export?mode=pretty", nil)
		if authorized {
			authorizeTestRequest(request)
			p.exportErr = tui.ErrExportUsage
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		expected := http.StatusUnauthorized
		if authorized {
			expected = http.StatusBadRequest
		}
		if response.Code != expected {
			t.Fatalf("status = %d want %d", response.Code, expected)
		}
	}
	p.exportErr = errors.New("render failed")
	request := httptest.NewRequest(http.MethodGet, "/api/v1/export?mode=raw", nil)
	authorizeTestRequest(request)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("render failure: %d", response.Code)
	}
	for _, query := range []string{"path=report.html", "mode=raw&path=x", "mode=raw&mode=pretty"} {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/export?"+query, nil)
		authorizeTestRequest(request)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("query %q: %d", query, response.Code)
		}
	}
}

// Exercise the real shared renderer at the download boundary, independently
// of the browser's limited presentation snapshot.
type exportUIPresentation struct {
	*testPresentation
	ui *tui.UI
}

func (p *exportUIPresentation) GenerateExport(mode string) (tui.ExportDocument, error) {
	return p.ui.GenerateExport(mode)
}

func newExportPresentation(t *testing.T) (*exportUIPresentation, string) {
	t.Helper()
	root := t.TempDir()
	out, err := os.CreateTemp(t.TempDir(), "terminal")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { out.Close() })
	ui := tui.New(out, out, nil, "test", "test-model", root)
	writer, _ := ui.AddAgentView("main", "test", "test-model")
	writer.Write([]byte("full archive tool output\n"))
	manager := agent.NewAgentManager(context.Background(), 20)
	t.Cleanup(manager.Shutdown)
	err = manager.RestoreWorkHistory(&session.WorkHistory{NextRequestID: 1, Records: []session.WorkRecord{{RequestID: "request-1", AgentID: "main", Status: "completed", Prompt: "Journal prompt", Response: "**Journal answer**"}}})
	if err != nil {
		t.Fatal(err)
	}
	ui.SetDetachedAgentManager(manager)
	return &exportUIPresentation{testPresentation: &testPresentation{}, ui: ui}, root
}

func TestExportEndpointRendersFullHistoryWithoutWritingFiles(t *testing.T) {
	p, root := newExportPresentation(t)
	handler := authorizedTestManager(p).routes()
	for _, tc := range []struct{ mode, want, absent string }{
		{"pretty", "<strong>Journal answer</strong>", "full archive tool output"},
		{"raw", "full archive tool output", "Journal answer"},
		{"", "<strong>Journal answer</strong>", "full archive tool output"},
	} {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/export?mode="+tc.mode, nil)
		authorizeTestRequest(request)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), tc.want) || strings.Contains(response.Body.String(), tc.absent) {
			t.Fatalf("%s export: %d %s", tc.mode, response.Code, response.Body.String())
		}
	}
	for _, arg := range []string{"report.html", "pretty raw"} {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/export?mode="+url.QueryEscape(arg), nil)
		authorizeTestRequest(request)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid argument %q: %d", arg, response.Code)
		}
	}
	files, err := os.ReadDir(root)
	if err != nil || len(files) != 0 {
		t.Fatalf("download wrote host files: %v %v", files, err)
	}
}
