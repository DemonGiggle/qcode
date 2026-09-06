package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	webBodyLimit = 2 * 1024 * 1024
	webTimeout   = 20 * time.Second
)

type searchResult struct{ Title, URL, Snippet string }
type searchBackend interface {
	Search(context.Context, *webTools, string, int) ([]searchResult, error)
}
type webTools struct {
	client  *http.Client
	backend searchBackend
	slots   chan struct{}
}

func newWebTools(backend string) (*webTools, error) {
	if backend != "" && backend != "duckduckgo" {
		return nil, fmt.Errorf("unsupported web_search.backend %q; supported backend: duckduckgo", backend)
	}
	transport := &http.Transport{
		// Direct connections ensure destination checks cannot be bypassed by a proxy.
		DialContext:  publicDialContext,
		MaxIdleConns: 2, MaxIdleConnsPerHost: 2, MaxConnsPerHost: 2,
		IdleConnTimeout: 30 * time.Second, TLSHandshakeTimeout: 10 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second, MaxResponseHeaderBytes: 32 * 1024,
	}
	client := &http.Client{Transport: transport, Timeout: webTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 5 {
				return fmt.Errorf("web redirect limit exceeded (5)")
			}
			return validateWebURL(req.URL)
		},
	}
	return &webTools{client: client, backend: duckDuckGo{}, slots: make(chan struct{}, 2)}, nil
}

// Resolve and dial the checked IP itself, so a second DNS lookup cannot change
// the destination. URL validation is also applied on every redirect.
func publicDialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	if len(addresses) == 0 {
		return nil, fmt.Errorf("web destination has no addresses")
	}
	for _, ip := range addresses {
		if !publicWebIP(ip) {
			return nil, fmt.Errorf("web destination must resolve only to public addresses")
		}
	}
	dialer := net.Dialer{Timeout: 10 * time.Second}
	for _, ip := range addresses {
		var conn net.Conn
		conn, err = dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
	}
	return nil, err
}

var nonPublicWebRanges = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001::/23"), netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
}

func publicWebIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.Zone() != "" || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	// Only currently allocated global IPv6 unicast; excludes local translation ranges.
	if ip.Is6() && !netip.MustParsePrefix("2000::/3").Contains(ip) {
		return false
	}
	for _, prefix := range nonPublicWebRanges {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}
func validateWebURL(u *url.URL) error {
	if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || len(u.String()) > 8192 {
		return fmt.Errorf("web URL must be HTTP(S), have a host, and contain no credentials (maximum 8192 bytes)")
	}
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil && !publicWebIP(ip) {
		return fmt.Errorf("web destination must be a public address")
	}
	return nil
}

func (r *Registry) beginWeb(ctx context.Context) (context.Context, context.CancelFunc, error) {
	if r.sandbox != nil && !r.sandbox.allowNetwork {
		return nil, nil, fmt.Errorf("web tools are unavailable: sandbox networking is disabled")
	}
	ctx, cancel := context.WithTimeout(ctx, webTimeout)
	select {
	case r.web.slots <- struct{}{}:
		return ctx, func() { <-r.web.slots; cancel() }, nil
	case <-ctx.Done():
		cancel()
		return nil, nil, ctx.Err()
	}
}

type webPage struct{ URL, MediaType, Content string }

func (w *webTools) get(ctx context.Context, address string) (webPage, error) {
	u, err := url.Parse(address)
	if err != nil {
		return webPage{}, fmt.Errorf("invalid web URL: %w", err)
	}
	if err := validateWebURL(u); err != nil {
		return webPage{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return webPage{}, err
	}
	req.Header.Set("User-Agent", "qcode/1.0")
	req.Header.Set("Accept", "text/html, application/xhtml+xml, text/plain;q=0.9, text/*;q=0.8")
	resp, err := w.client.Do(req)
	if err != nil {
		return webPage{}, fmt.Errorf("web request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return webPage{}, fmt.Errorf("web request returned HTTP %d", resp.StatusCode)
	}
	if resp.StatusCode == http.StatusAccepted {
		return webPage{}, fmt.Errorf("web request returned HTTP 202; content may be blocked or pending")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, webBodyLimit+1))
	if err != nil {
		return webPage{}, fmt.Errorf("read web response: %w", err)
	}
	if len(body) > webBodyLimit {
		return webPage{}, fmt.Errorf("web response exceeds 2 MiB limit")
	}
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = http.DetectContentType(body)
	}
	media, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return webPage{}, fmt.Errorf("invalid web content type")
	}
	if media != "application/xhtml+xml" && !strings.HasPrefix(media, "text/") {
		return webPage{}, fmt.Errorf("unsupported web content type %q; expected HTML or text", media)
	}
	charset := strings.ToLower(params["charset"])
	if charset != "" && charset != "utf-8" && charset != "us-ascii" {
		return webPage{}, fmt.Errorf("unsupported web charset %q; expected UTF-8", charset)
	}
	if !utf8.Valid(body) {
		return webPage{}, fmt.Errorf("web content is not valid UTF-8")
	}
	if err := ctx.Err(); err != nil {
		return webPage{}, err
	}
	return webPage{resp.Request.URL.String(), media, string(body)}, nil
}

func (r *Registry) webFetch(ctx context.Context, arguments json.RawMessage) (string, error) {
	var args struct {
		URL string `json:"url"`
	}
	if err := decode(arguments, &args); err != nil {
		return "", err
	}
	ctx, done, err := r.beginWeb(ctx)
	if err != nil {
		return "", err
	}
	defer done()
	page, err := r.web.get(ctx, args.URL)
	if err != nil {
		return "", err
	}
	content := page.Content
	if page.MediaType == "text/html" || page.MediaType == "application/xhtml+xml" {
		content, err = extractWebText(ctx, content)
		if err != nil {
			return "", err
		}
	}
	return webOutput("Source: " + page.URL + "\nContent-Type: " + page.MediaType + "\n[Untrusted web content]\n\n" + content), nil
}
func (r *Registry) webSearch(ctx context.Context, arguments json.RawMessage) (string, error) {
	var args struct {
		Query      string `json:"query"`
		MaxResults int    `json:"max_results"`
	}
	if err := decode(arguments, &args); err != nil {
		return "", err
	}
	args.Query = strings.TrimSpace(args.Query)
	if args.Query == "" || len(args.Query) > 2048 {
		return "", fmt.Errorf("query must contain 1–2048 bytes")
	}
	if args.MaxResults == 0 {
		args.MaxResults = 5
	}
	if args.MaxResults < 1 || args.MaxResults > 10 {
		return "", fmt.Errorf("max_results must be between 1 and 10")
	}
	ctx, done, err := r.beginWeb(ctx)
	if err != nil {
		return "", err
	}
	defer done()
	results, err := r.web.backend.Search(ctx, r.web, args.Query, args.MaxResults)
	if err != nil {
		return "", err
	}
	var output strings.Builder
	output.WriteString("[Untrusted web search results; backend: duckduckgo]\n")
	if len(results) == 0 {
		output.WriteString("No results found.\n")
	}
	for i, result := range results {
		fmt.Fprintf(&output, "\n%d. %s\n%s\n%s\n", i+1, result.Title, result.URL, result.Snippet)
	}
	return webOutput(output.String()), nil
}

// Strip terminal controls and preserve UTF-8 when enforcing the tool output cap.
func webOutput(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, s)
	const marker = "\n[truncated: web output limit reached]"
	if len(s) <= maxOutput {
		return s
	}
	end := maxOutput - len(marker)
	for !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end] + marker
}
