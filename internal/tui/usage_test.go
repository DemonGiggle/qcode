package tui

import (
	"qcode/internal/llm"
	"strings"
	"testing"
)

type tokenRunner struct {
	statusRunner
	usage llm.SessionUsage
}

func (r tokenRunner) SessionUsage() llm.SessionUsage { return r.usage }

func TestSessionUsageLabels(t *testing.T) {
	for _, tc := range []struct {
		usage llm.SessionUsage
		want  string
	}{
		{llm.SessionUsage{}, "I:0 O:0"},
		{llm.SessionUsage{Missing: 1}, "unknown"},
		{llm.SessionUsage{InputTokens: 1200, OutputTokens: 340, TotalTokens: 1540}, "I:1.2K O:340"},
		{llm.SessionUsage{InputTokens: 120, OutputTokens: 30, TotalTokens: 150, Missing: 1}, "I:120 O:30?"},
	} {
		u := &UI{runner: tokenRunner{usage: tc.usage}, width: 80}
		if got := u.usageLabel(); got != tc.want {
			t.Fatalf("got %q want %q", got, tc.want)
		}
		for _, color := range []bool{false, true} {
			bar := statusBar("provider", "model", "/workspace", 80, false, color, "unknown", u.usageLabel())
			if !strings.Contains(bar, tc.want) || visibleWidth(bar) > 80 {
				t.Fatal(bar)
			}
		}
	}
	u := &UI{runner: tokenRunner{usage: llm.SessionUsage{InputTokens: 120, OutputTokens: 30, TotalTokens: 150}}, width: 120}
	if !strings.Contains(u.usageLabel(), "Σ:150") {
		t.Fatal(u.usageLabel())
	}
}

func TestCompactTokenCount(t *testing.T) {
	for _, tc := range []struct {
		tokens int
		want   string
	}{
		{0, "0"}, {999, "999"}, {1000, "1K"}, {1250, "1.3K"},
		{1_000_000, "1M"}, {1_250_000, "1.3M"},
	} {
		if got := compactTokenCount(tc.tokens); got != tc.want {
			t.Errorf("compactTokenCount(%d) = %q, want %q", tc.tokens, got, tc.want)
		}
	}
}
