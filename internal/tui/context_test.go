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
			// Narrow widths wrap to two left-aligned lines instead of
			// dropping CTX.
			bar := statusBar("ollama", "qwen", "/w", 50, unicode, color, "73% left", "I:1 O:2", "2/32")
			lines := strings.Split(bar, "\n")
			if len(lines) != 2 {
				t.Fatalf("bar=%q want two lines", bar)
			}
			for _, line := range lines {
				if visibleWidth(line) > 50 {
					t.Fatalf("bar=%q width=%d", bar, visibleWidth(line))
				}
				if strings.HasPrefix(line, " ") {
					t.Fatalf("bar line not left-aligned: %q", line)
				}
			}
			if !strings.Contains(bar, "73% left") {
				t.Fatalf("bar=%q missing ctx", bar)
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
