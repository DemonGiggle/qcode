package tui

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

type statusRunner struct{}

func (statusRunner) Run(context.Context, string) error   { return nil }
func (statusRunner) ToolNames() []string                 { return []string{"write", "read", "shell"} }
func (statusRunner) ToggleTool(string, bool)             {}
func (statusRunner) ToolEnabled(name string) bool        { return name != "shell" }
func (statusRunner) ContextRemaining() (int, bool, bool) { return 73, true, true }
func (statusRunner) RefreshContext(context.Context)      {}

func TestStartupToolSummary(t *testing.T) {
	var out bytes.Buffer
	u := &UI{runner: statusRunner{}, display: newHistoryWriter(&out), width: 80}
	u.printToolSummary()
	if !strings.Contains(out.String(), "Tools enabled: read, write") || !strings.Contains(out.String(), "Tools disabled: shell") {
		t.Fatal(out.String())
	}
	if got := u.contextLabel(); got != "~73% left" {
		t.Fatal(got)
	}
}

func TestContextStatusRemainsVisibleOnNarrowTerminals(t *testing.T) {
	for _, color := range []bool{false, true} {
		for _, unicode := range []bool{false, true} {
			// CTX outranks TOK/STEP: token totals drop first.
			bar := statusBar("ollama", "qwen", "/w", 50, unicode, color, "73% left", "I:1 O:2", "2/32")
			if !strings.Contains(bar, "73% left") || strings.Contains(bar, "I:1 O:2") || visibleWidth(bar) > 50 {
				t.Fatalf("bar=%q width=%d", bar, visibleWidth(bar))
			}
		}
	}
	// Extremely narrow with a long model name keeps the higher-priority
	// provider/model unit over CTX.
	narrow := statusBar("opencode-go", "a-very-long-model", "/workspace", 32, true, false, "73% left")
	if !strings.Contains(narrow, "opencode-go") || visibleWidth(narrow) > 32 {
		t.Fatalf("bar=%q width=%d", narrow, visibleWidth(narrow))
	}
}
