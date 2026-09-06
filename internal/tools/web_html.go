package tools

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"
	"unicode"

	"golang.org/x/net/html"
)

// Tokenize rather than build a DOM: memory does not grow with element count or
// nesting depth. The caller bounds the UTF-8 input to webBodyLimit bytes.
func webTokens(ctx context.Context, content string, visit func(html.Token) bool) error {
	z := html.NewTokenizer(strings.NewReader(content))
	z.SetMaxBuf(webBodyLimit)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if z.Next() == html.ErrorToken {
			if z.Err() == io.EOF {
				return nil
			}
			return fmt.Errorf("parse web HTML: %w", z.Err())
		}
		if !visit(z.Token()) {
			return nil
		}
	}
}

func extractWebText(ctx context.Context, content string) (string, error) {
	var out strings.Builder
	skip := ""
	truncated := false
	pre, space := false, false
	err := webTokens(ctx, content, func(t html.Token) bool {
		if skip != "" {
			if t.Type == html.EndTagToken && t.Data == skip {
				skip = ""
			}
			return true
		}
		if t.Type == html.StartTagToken {
			switch t.Data {
			case "head", "script", "style", "template", "svg":
				skip = t.Data
				return true
			}
		}
		if t.Data == "pre" {
			if t.Type == html.StartTagToken {
				pre = true
			}
			if t.Type == html.EndTagToken {
				pre = false
			}
		}
		if t.Type == html.TextToken {
			for _, r := range t.Data {
				if !pre && unicode.IsSpace(r) {
					if space {
						continue
					}
					r = ' '
				}
				space = unicode.IsSpace(r)
				out.WriteRune(r)
				if out.Len() > maxOutput {
					break
				}
			}
		}
		if t.Type == html.StartTagToken || t.Type == html.EndTagToken || t.Type == html.SelfClosingTagToken {
			switch t.Data {
			case "p", "div", "section", "article", "header", "footer", "nav", "main", "aside", "br", "hr", "li", "ul", "ol", "h1", "h2", "h3", "h4", "h5", "h6", "pre", "tr":
				out.WriteByte('\n')
				space = true
			case "td", "th":
				out.WriteByte('\t')
			}
		}
		// Keep extraction bounded even for highly repetitive markup.
		truncated = out.Len() > maxOutput
		return !truncated
	})
	if err != nil {
		return "", err
	}
	result := strings.TrimSpace(out.String())
	if truncated {
		result += "\n[truncated: web text extraction limit reached]"
	}
	return result, nil
}

type duckDuckGo struct{}

func (duckDuckGo) Search(ctx context.Context, w *webTools, query string, limit int) ([]searchResult, error) {
	page, err := w.get(ctx, "https://html.duckduckgo.com/html/?"+url.Values{"q": {query}}.Encode())
	if err != nil {
		return nil, err
	}
	if page.MediaType != "text/html" && page.MediaType != "application/xhtml+xml" {
		return nil, fmt.Errorf("DuckDuckGo returned non-HTML content")
	}
	return parseDuckDuckGo(ctx, page.Content, limit)
}

func tokenAttr(t html.Token, name string) string {
	for _, a := range t.Attr {
		if a.Key == name {
			return a.Val
		}
	}
	return ""
}
func tokenClass(t html.Token, name string) bool {
	for _, c := range strings.Fields(tokenAttr(t, "class")) {
		if c == name {
			return true
		}
	}
	return false
}
func searchURL(href string) string {
	base, _ := url.Parse("https://html.duckduckgo.com")
	u, err := url.Parse(href)
	if err != nil || href == "" {
		return ""
	}
	u = base.ResolveReference(u)
	if (u.Hostname() == "duckduckgo.com" || u.Hostname() == "html.duckduckgo.com") && strings.HasPrefix(u.Path, "/l/") {
		u, err = url.Parse(u.Query().Get("uddg"))
		if err != nil {
			return ""
		}
	}
	if validateWebURL(u) != nil {
		return ""
	}
	return u.String()
}

func parseDuckDuckGo(ctx context.Context, content string, limit int) ([]searchResult, error) {
	var results []searchResult
	seen := map[string]bool{}
	noResults, blocked := false, false
	capture, tag, href := "", "", ""
	depth, current := 0, -1
	var text strings.Builder
	err := webTokens(ctx, content, func(t html.Token) bool {
		if t.Type == html.StartTagToken || t.Type == html.SelfClosingTagToken {
			if tokenClass(t, "no-results") || tokenClass(t, "no-results__message") {
				noResults = true
			}
			if strings.Contains(tokenAttr(t, "id"), "anomaly") || strings.Contains(tokenAttr(t, "class"), "anomaly") || strings.Contains(tokenAttr(t, "action"), "anomaly.js") {
				blocked = true
			}
		}
		if capture != "" {
			if t.Type == html.TextToken && text.Len() < 4096 {
				text.WriteString(t.Data[:min(len(t.Data), 4096-text.Len())])
			}
			if t.Type == html.StartTagToken && t.Data == tag {
				depth++
			}
			if t.Type == html.EndTagToken && t.Data == tag {
				depth--
				if depth == 0 {
					value := strings.Join(strings.Fields(strings.ToValidUTF8(text.String(), "")), " ")
					if capture == "title" {
						current = -1
						if href != "" && value != "" && !seen[href] && len(results) < limit {
							seen[href] = true
							results = append(results, searchResult{Title: value, URL: href})
							current = len(results) - 1
						}
					} else if current >= 0 {
						results[current].Snippet = value
					}
					capture = ""
				}
			}
			return true
		}
		if t.Type == html.StartTagToken {
			if t.Data == "a" && tokenClass(t, "result__a") {
				capture, href = "title", searchURL(tokenAttr(t, "href"))
			} else if tokenClass(t, "result__snippet") {
				capture = "snippet"
			}
			if capture != "" {
				tag, depth = t.Data, 1
				text.Reset()
			}
		}
		return true
	})
	if err != nil {
		return nil, err
	}
	if blocked {
		return nil, fmt.Errorf("DuckDuckGo blocked the request with a challenge; try again later")
	}
	if len(results) == 0 && !noResults {
		return nil, fmt.Errorf("DuckDuckGo returned an unrecognized search page (possibly blocked or changed markup)")
	}
	return results, nil
}
