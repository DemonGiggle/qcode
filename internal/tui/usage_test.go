package tui

import (
	"os"
	"qcode/internal/llm"
	"strings"
	"testing"
	"time"
)

func TestStatusBarRefreshesWhileIdleWithoutManager(t *testing.T) {
	out, err := os.CreateTemp(t.TempDir(), "status")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	u := &UI{out: out, runner: tokenRunner{}, width: 120, height: 24, statusActive: true}
	u.drawStatusBar()
	initial, _ := out.Stat()
	u.refreshStatusBar()
	unchanged, _ := out.Stat()
	if unchanged.Size() != initial.Size() {
		t.Fatal("unchanged status was repainted")
	}
	u.runner = tokenRunner{usage: llm.SessionUsage{InputTokens: 1200, OutputTokens: 340, TotalTokens: 1540}}
	stop := u.watchTaskIndicator()
	defer stop()
	deadline := time.Now().Add(3 * time.Second)
	for {
		u.screenMu.Lock()
		updated := strings.Contains(u.statusBarText, "I:1.2K O:340")
		u.screenMu.Unlock()
		if updated {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("status did not refresh without a UI event")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestFixedStatusRefreshUpdatesFooterWithoutRebuildingHistory(t *testing.T) {
	u, _ := layoutFixture(t)
	u.runner = tokenRunner{}
	history := &countedHistory{historyDisplay: u.display}
	u.display = history
	u.renderInput(inputPrompt, "draft", 2)
	previous := append([]string(nil), u.inputScreenRows...)
	history.snapshots = 0
	u.runner = tokenRunner{usage: llm.SessionUsage{InputTokens: 1200, OutputTokens: 340, TotalTokens: 1540}}
	u.refreshStatusBar()
	if history.snapshots != 0 {
		t.Fatal("status refresh rebuilt history")
	}
	if !strings.Contains(u.statusBarText, "I:1.2K O:340") || !strings.Contains(strings.Join(u.inputScreenRows, "\n"), "I:1.2K O:340") {
		t.Fatal("status update missing from the screen cache")
	}
	for row := 1; row <= u.height-statusBarLineCount(u.statusBarText); row++ {
		if previous[row] != u.inputScreenRows[row] {
			t.Fatalf("status update changed row %d above the footer", row)
		}
	}
	data, err := os.ReadFile(u.out.Name())
	if err != nil {
		t.Fatal(err)
	}
	screen := newFixedScreen(u.width, u.height)
	screen.feed(string(data))
	var rendered strings.Builder
	for _, row := range screen.rows {
		rendered.WriteString(string(row))
		rendered.WriteByte('\n')
	}
	if !strings.Contains(rendered.String(), "I:1.2K O:340") {
		t.Fatal("status update missing from the terminal")
	}
}

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
		{llm.SessionUsage{}, "I:0 O:0 T:0"},
		{llm.SessionUsage{Missing: 1}, "unknown"},
		{llm.SessionUsage{InputTokens: 1200, OutputTokens: 340, TotalTokens: 1540}, "I:1.2K O:340 T:1.5K"},
		{llm.SessionUsage{InputTokens: 120, OutputTokens: 30, TotalTokens: 150, Missing: 1}, "I:120 O:30? T:150"},
	} {
		u := &UI{runner: tokenRunner{usage: tc.usage}, width: 80}
		if got := u.usageLabel(); got != tc.want {
			t.Fatalf("got %q want %q", got, tc.want)
		}
		for _, color := range []bool{false, true} {
			bar := statusBar("provider", "model", "/workspace", 80, false, color, "unknown", u.usageLabel())
			if !strings.Contains(bar, tc.want) {
				t.Fatal(bar)
			}
		}
	}
	u := &UI{runner: tokenRunner{usage: llm.SessionUsage{InputTokens: 120, OutputTokens: 30, TotalTokens: 150}}, width: 120, unicode: true}
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
