package tools

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"qcode/internal/llm"
)

// Keep production URL/redirect checks, but direct public fixture hostnames to
// the local server. Production's DNS dialer is exercised separately below.
func fixtureWeb(t *testing.T, handler http.HandlerFunc) *Registry {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	r, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r.EnableTool("web_fetch")
	r.EnableTool("web_search")
	transport := r.web.client.Transport.(*http.Transport)
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	t.Cleanup(transport.CloseIdleConnections)
	r.web.client.Transport = fixtureTransport{transport}
	return r
}

// Keep production URL checks, but direct a public fixture hostname to a TLS
// server with an untrusted certificate.
func tlsFixtureWeb(t *testing.T, insecureSkipTLSVerify bool, handler http.HandlerFunc) *Registry {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.Listener = listener
	server.StartTLS()
	t.Cleanup(server.Close)
	r, err := NewWithOptions(t.TempDir(), Options{InsecureSkipTLSVerify: insecureSkipTLSVerify})
	if err != nil {
		t.Fatal(err)
	}
	r.EnableTool("web_fetch")
	r.EnableTool("web_search")
	transport := r.web.client.Transport.(*http.Transport)
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	t.Cleanup(transport.CloseIdleConnections)
	return r
}

type fixtureTransport struct{ base *http.Transport }

func (f fixtureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme = "http"
	return f.base.RoundTrip(clone)
}

func callWeb(r *Registry, ctx context.Context, name string, args any) (string, error) {
	data, _ := json.Marshal(args)
	return r.Execute(ctx, llm.ToolCall{Name: name, Arguments: data})
}
func TestWebFetch(t *testing.T) {
	r := fixtureWeb(t, func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/redirect":
			http.Redirect(w, req, "/html", http.StatusFound)
		case "/html":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, `<html><head><title>Hidden title</title></head><body><h1>Hello &amp; 世界</h1><p>A <b>bold</b> word.</p><pre>  code\n    indentation</pre><script>secretScript()</script><style>secretStyle</style></body></html>`)
		case "/text":
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, "plain\x1b[31m text")
		case "/large":
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, strings.Repeat("世", maxOutput))
		case "/gzip":
			w.Header().Set("Content-Type", "text/plain")
			w.Header().Set("Content-Encoding", "gzip")
			z := gzip.NewWriter(w)
			io.WriteString(z, strings.Repeat("x", webBodyLimit+1))
			z.Close()
		case "/accepted":
			w.WriteHeader(202)
		case "/invalid":
			w.Header().Set("Content-Type", "text/plain")
			w.Write([]byte{0xff})
		case "/oversize":
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, strings.Repeat("x", webBodyLimit+1))
		case "/binary":
			w.Header().Set("Content-Type", "application/pdf")
			fmt.Fprint(w, "%PDF")
		case "/charset":
			w.Header().Set("Content-Type", "text/html; charset=iso-8859-1")
		case "/private":
			http.Redirect(w, req, "http://127.0.0.1/", http.StatusFound)
		case "/loop":
			http.Redirect(w, req, "/loop", http.StatusFound)
		case "/rate":
			w.WriteHeader(429)
		default:
			w.WriteHeader(500)
		}
	})
	for _, tc := range []struct {
		path, want string
		failure    bool
	}{
		{"/redirect", "Source: http://fixture.example/html", false},
		{"/html", "A bold word.", false}, {"/text", "plain[31m text", false},
		{"/large", "[truncated:", false}, {"/oversize", "2 MiB", true},
		{"/gzip", "2 MiB", true}, {"/accepted", "HTTP 202", true}, {"/invalid", "UTF-8", true},
		{"/binary", "unsupported web content type", true}, {"/charset", "charset", true},
		{"/private", "public address", true}, {"/loop", "redirect limit", true},
		{"/rate", "HTTP 429", true}, {"/error", "HTTP 500", true},
	} {
		t.Run(tc.path, func(t *testing.T) {
			out, err := callWeb(r, context.Background(), "web_fetch", map[string]any{"url": "http://fixture.example" + tc.path})
			if tc.failure {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("got %q, %v", out, err)
				}
				return
			}
			if err != nil || !strings.Contains(out, tc.want) {
				t.Fatalf("got %q, %v", out, err)
			}
			if strings.Contains(out, "secretScript") || strings.Contains(out, "secretStyle") || strings.Contains(out, "Hidden title") {
				t.Fatal(out)
			}
			if len(out) > maxOutput || !utf8.ValidString(out) {
				t.Fatal("invalid output bounds or UTF-8")
			}
		})
	}
}

func TestWebTLSVerificationOverride(t *testing.T) {
	handler := func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/fetch":
			w.Header().Set("Content-Type", "text/plain")
			io.WriteString(w, "insecure fetch")
		case "/html/":
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, ddgFixture)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}

	verified := tlsFixtureWeb(t, false, handler)
	if _, err := callWeb(verified, context.Background(), "web_fetch", map[string]any{"url": "https://fixture.example/fetch"}); err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("verified request error = %v, want certificate verification failure", err)
	}

	insecure := tlsFixtureWeb(t, true, handler)
	out, err := callWeb(insecure, context.Background(), "web_fetch", map[string]any{"url": "https://fixture.example/fetch"})
	if err != nil || !strings.Contains(out, "insecure fetch") {
		t.Fatalf("insecure fetch = %q, %v", out, err)
	}
	out, err = callWeb(insecure, context.Background(), "web_search", map[string]any{"query": "Go documentation"})
	if err != nil || !strings.Contains(out, "https://go.dev/doc/") {
		t.Fatalf("insecure search = %q, %v", out, err)
	}
}

func TestWebNetworkPolicy(t *testing.T) {
	for _, value := range []string{"127.0.0.1", "10.0.0.1", "172.16.0.1", "192.168.1.1", "169.254.169.254", "100.64.0.1", "0.0.0.1", "224.0.0.1", "240.0.0.1", "::1", "::ffff:127.0.0.1", "fc00::1", "fe80::1", "64:ff9b::a00:1", "2002:a00:1::"} {
		if publicWebIP(netip.MustParseAddr(value)) {
			t.Errorf("allowed %s", value)
		}
	}
	for _, value := range []string{"1.1.1.1", "8.8.8.8", "2606:4700:4700::1111"} {
		if !publicWebIP(netip.MustParseAddr(value)) {
			t.Errorf("rejected %s", value)
		}
	}
	for _, value := range []string{"file:///tmp/a", "ftp://example.com/a", "https://user:pass@example.com", "http://127.0.0.1", "http://[::1]", "/relative"} {
		u, _ := url.Parse(value)
		if validateWebURL(u) == nil {
			t.Errorf("allowed %s", value)
		}
	}
	if conn, err := publicDialContext(context.Background(), "tcp", "localhost:80"); err == nil {
		conn.Close()
		t.Fatal("allowed localhost DNS destination")
	}
	r, err := NewWithOptions(t.TempDir(), Options{Sandbox: true, BubblewrapPath: "/unused/bwrap"})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"web_fetch", "web_search"} {
		r.EnableTool(name)
		_, err := callWeb(r, context.Background(), name, map[string]any{map[string]string{"web_fetch": "url", "web_search": "query"}[name]: "https://example.com"})
		if err == nil || !strings.Contains(err.Error(), "sandbox networking") {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestWebCancellationAndConcurrency(t *testing.T) {
	r := fixtureWeb(t, func(w http.ResponseWriter, req *http.Request) { <-req.Context().Done() })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := callWeb(r, ctx, "web_fetch", map[string]any{"url": "http://fixture.example/slow"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
	// All request slots must be released after a failed request.
	if len(r.web.slots) != 0 {
		t.Fatal("request slot leaked")
	}
	c1, done1, err := r.beginWeb(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer done1()
	_, done2, err := r.beginWeb(c1)
	if err != nil {
		t.Fatal(err)
	}
	defer done2()
	ctx2, cancel2 := context.WithCancel(context.Background())
	cancel2()
	if _, _, err := r.beginWeb(ctx2); !errors.Is(err, context.Canceled) {
		t.Fatalf("queued request: %v", err)
	}
}

const ddgFixture = `<html><div class="result"><a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fgo.dev%2Fdoc%2F">Go <b>documentation</b></a><a class="result__snippet">Read &amp; learn Go.</a></div><div class="result"><a class="result__a" href="https://example.com/">Example</a><a class="result__snippet">Another result.</a></div></html>`

func TestDuckDuckGoParsing(t *testing.T) {
	for _, tc := range []struct {
		name, html string
		count      int
		failure    string
	}{
		{"results", ddgFixture, 2, ""},
		{"empty", `<div class="no-results__message">No results found.</div>`, 0, ""},
		{"challenge", `<form id="challenge-form" action="//duckduckgo.com/anomaly.js"><div class="anomaly-modal">Solve</div></form>`, 0, "challenge"},
		{"changed", `<html>New search interface</html>`, 0, "unrecognized"},
		{"malformed", `<a class="result__a" href="https://example.com">unfinished`, 0, "unrecognized"},
		{"unsafe", `<a class="result__a" href="javascript:alert(1)">bad</a>`, 0, "unrecognized"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			results, err := parseDuckDuckGo(context.Background(), tc.html, 5)
			if tc.failure != "" {
				if err == nil || !strings.Contains(err.Error(), tc.failure) {
					t.Fatalf("got %v, %v", results, err)
				}
				return
			}
			if err != nil || len(results) != tc.count {
				t.Fatalf("got %v, %v", results, err)
			}
			if tc.count > 0 && (results[0].URL != "https://go.dev/doc/" || results[0].Title != "Go documentation" || results[0].Snippet != "Read & learn Go.") {
				t.Fatal(results)
			}
		})
	}
}
func TestWebSearch(t *testing.T) {
	r := fixtureWeb(t, func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/html/" || req.URL.Query().Get("q") != "Go & documentation" {
			t.Errorf("request: %s", req.URL)
		}
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, ddgFixture)
	})
	out, err := callWeb(r, context.Background(), "web_search", map[string]any{"query": "Go & documentation", "max_results": 1})
	if err != nil || !strings.Contains(out, "https://go.dev/doc/") || strings.Contains(out, "Another result") {
		t.Fatalf("got %q, %v", out, err)
	}
	for _, args := range []map[string]any{{"query": " "}, {"query": "Go", "max_results": -1}, {"query": "Go", "max_results": 11}, {"query": "Go", "backend": "other"}} {
		if _, err := callWeb(r, context.Background(), "web_search", args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	for _, backend := range []string{"", "duckduckgo"} {
		r, err := NewWithOptions(t.TempDir(), Options{SearchBackend: backend})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := r.web.backend.(duckDuckGo); !ok {
			t.Fatal("wrong default backend")
		}
		r.ResetSession()
		if _, ok := r.web.backend.(duckDuckGo); !ok {
			t.Fatal("backend changed during reset")
		}
	}
	if _, err := NewWithOptions(t.TempDir(), Options{SearchBackend: "unknown"}); err == nil {
		t.Fatal("accepted unknown backend")
	}
}

// Opt-in smoke test; ordinary tests never depend on external search availability.
func TestWebLive(t *testing.T) {
	if os.Getenv("QCODE_TEST_WEB_LIVE") != "1" {
		t.Skip("set QCODE_TEST_WEB_LIVE=1 for live smoke test")
	}
	r, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"web_fetch", map[string]any{"url": "https://example.com"}},
		{"web_search", map[string]any{"query": "golang documentation"}},
	} {
		r.EnableTool(tc.name)
		out, err := callWeb(r, context.Background(), tc.name, tc.args)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		t.Log(out)
	}
}
func BenchmarkWebHTML(b *testing.B) {
	content := "<html><body>" + strings.Repeat("<p>Hello <b>world</b> &amp; Go.</p>", 32768) + "</body></html>"
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := extractWebText(context.Background(), content); err != nil {
			b.Fatal(err)
		}
	}
}
func BenchmarkDuckDuckGo(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := parseDuckDuckGo(context.Background(), ddgFixture, 5); err != nil {
			b.Fatal(err)
		}
	}
}

func TestWebTextExtraction(t *testing.T) {
	for _, tc := range []struct{ html, want string }{
		{"<p>Hello   <b>bold</b>\n world</p>", "Hello bold world"},
		{"<pre>  line\n    indent</pre>", "line\n    indent"},
		{"<p>Recover <b>unclosed", "Recover unclosed"},
	} {
		got, err := extractWebText(context.Background(), tc.html)
		if err != nil || got != tc.want {
			t.Fatalf("got %q, %v; want %q", got, err, tc.want)
		}
	}
	got, err := extractWebText(context.Background(), "<p>"+strings.Repeat("世", maxOutput)+"</p>")
	if err != nil || !strings.Contains(got, "[truncated:") || len(webOutput(got)) > maxOutput || !utf8.ValidString(webOutput(got)) {
		t.Fatalf("truncation failed: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := extractWebText(ctx, "<p>text</p>"); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

func TestWebToolsRequireSessionOptIn(t *testing.T) {
	r, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	for _, name := range []string{"web_fetch", "web_search"} {
		r.handlers[name] = func(context.Context, json.RawMessage) (string, error) { calls++; return "called", nil }
	}
	checkDisabled := func() {
		t.Helper()
		for _, name := range []string{"web_fetch", "web_search"} {
			if r.IsToolEnabled(name) {
				t.Fatalf("%s enabled without user opt-in", name)
			}
			for _, schema := range r.EnabledSchemas() {
				if schema.Name == name {
					t.Fatalf("advertised %s", name)
				}
			}
			before := calls
			if _, err := r.Execute(context.Background(), llm.ToolCall{Name: name}); err == nil || !strings.Contains(err.Error(), "disabled") {
				t.Fatalf("%s: %v", name, err)
			}
			if calls != before {
				t.Fatal("disabled tool handler executed")
			}
		}
	}
	checkDisabled()
	for _, name := range []string{"web_fetch", "web_search"} {
		r.EnableTool(name)
		if _, err := r.Execute(context.Background(), llm.ToolCall{Name: name}); err != nil {
			t.Fatal(err)
		}
		r.DisableTool(name)
		checkDisabled()
	}
	r.EnableTool("web_fetch")
	r.EnableTool("web_search")
	r.ResetSession()
	checkDisabled()
	if calls != 2 {
		t.Fatalf("handler calls = %d", calls)
	}
}
