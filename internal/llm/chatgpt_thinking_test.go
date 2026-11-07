package llm

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"qcode/internal/chatgptoauth"
)

func TestChatGPTBundledThinking(t *testing.T) {
	p := &chatGPTProvider{}
	for _, tc := range []struct{ model, levels, def string }{
		{"gpt-6-luna", "low,medium,high,xhigh,max", "medium"},
		{"gpt-5.6-luna", "low,medium,high,xhigh,max", "medium"},
		{"gpt-6.1-sol", "low,medium,high,xhigh,max,ultra", "low"},
		{"gpt-6.1-terra", "low,medium,high,xhigh,max,ultra", "low"},
		{"gpt-6-sol", "low,medium,high,xhigh,max,ultra", "medium"},
		{"gpt-5.5", "low,medium,high,xhigh", "medium"},
	} {
		t.Run(tc.model, func(t *testing.T) {
			c := p.ThinkingCapability(tc.model)
			if strings.Join(c.Levels, ",") != tc.levels || c.Default != tc.def || !c.Adjustable {
				t.Fatalf("capability = %+v", c)
			}
			c.Levels[0] = "changed"
			if p.ThinkingCapability(tc.model).Levels[0] == "changed" {
				t.Fatal("mutable catalog levels escaped")
			}
		})
	}
	if c := p.ThinkingCapability("future-model"); c.Supported || c.Adjustable || c.Default != "" {
		t.Fatalf("invented capability: %+v", c)
	}
	if _, err := p.Complete(context.Background(), Request{Model: "gpt-6-luna", Thinking: "minimal"}, nil); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("invalid effort should fail before authentication: %v", err)
	}
}

func TestChatGPTLiveThinkingMetadata(t *testing.T) {
	p := &chatGPTProvider{baseURL: "https://example.test", client: doerFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"models":[
   {"slug":"gpt-6-luna","supported_reasoning_levels":[{"effort":"low"},{"effort":"high"}],"default_reasoning_level":"high"},
   {"id":"future-model","supported_reasoning_levels":[{"effort":"new-tier"},{"effort":"max"}],"default_reasoning_level":"new-tier"},
   {"slug":"gpt-5.5","supported_reasoning_levels":[]},
   {"slug":"gpt-6.1-sol"}
  ]}`))}, nil
	})}
	if _, err := p.fetchModels(context.Background(), chatgptoauth.Tokens{}); err != nil {
		t.Fatal(err)
	}
	c := p.ThinkingCapability("gpt-6-luna")
	if strings.Join(c.Levels, ",") != "low,high" || c.Default != "high" {
		t.Fatalf("live override = %+v", c)
	}
	if c := p.ThinkingCapability("future-model"); !c.Adjustable || c.Default != "new-tier" {
		t.Fatalf("future metadata = %+v", c)
	}
	if c := p.ThinkingCapability("gpt-5.5"); c.Supported {
		t.Fatalf("empty metadata did not override bundle: %+v", c)
	}
	if c := p.ThinkingCapability("gpt-6.1-sol"); !containsChoice(c.Levels, "ultra") {
		t.Fatalf("missing metadata lost fallback: %+v", c)
	}
}
