package tui

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"
)

type selectionRunner map[string]bool

func (r selectionRunner) ToggleTool(name string, enabled bool) { r[name] = enabled }
func (r selectionRunner) ToolEnabled(name string) bool         { return r[name] }
func (r selectionRunner) ToolNames() []string                  { return []string{"web_fetch", "web_search"} }

func TestToolSelectionRequiresApply(t *testing.T) {
	for _, tc := range []struct {
		name, input       string
		accepted, failure bool
	}{
		{"apply", " \r", true, false},
		{"cancel", " \x03", false, false},
		{"input closed", " ", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := selectionRunner{"web_fetch": false, "web_search": false}
			accepted, err := selectTools(strings.NewReader(tc.input), io.Discard, runner.ToolNames(), runner, 2, 80, false)
			if accepted != tc.accepted || (err != nil) != tc.failure {
				t.Fatalf("accepted %v, error %v", accepted, err)
			}
			if runner.ToolEnabled("web_fetch") != tc.accepted {
				t.Fatal("enablement did not match explicit apply")
			}
			if runner.ToolEnabled("web_search") {
				t.Fatal("enabled an unselected tool")
			}
		})
	}
}

func TestToolSelectionPagesThroughBoundedViewport(t *testing.T) {
	names := make([]string, 30)
	runner := selectionRunner{}
	for i := range names {
		names[i] = fmt.Sprintf("tool-%02d", i)
	}
	var output bytes.Buffer
	accepted, err := selectTools(strings.NewReader(selectorPageDown+" \r"), &output, names, runner, 5, 40, false)
	if err != nil || !accepted || !runner["tool-05"] {
		t.Fatalf("accepted = %v, enabled = %v, err = %v", accepted, runner["tool-05"], err)
	}
	if lines := strings.Count(output.String(), "\n"); lines != 12 {
		t.Fatalf("rendered lines = %d, want two bounded five-row pages with headers", lines)
	}
}

func TestToolSelectionFiltersByName(t *testing.T) {
	runner := selectionRunner{"web_fetch": false, "web_search": false, "shell": false}
	names := []string{"web_fetch", "web_search", "shell"}
	var output bytes.Buffer
	accepted, err := selectTools(strings.NewReader("search \r"), &output, names, runner, 3, 80, false)
	if err != nil || !accepted || !runner["web_search"] || runner["web_fetch"] {
		t.Fatalf("accepted = %v, runner = %v, err = %v", accepted, runner, err)
	}
	if !strings.Contains(output.String(), "Select tools (1/3) | Filter: search") {
		t.Fatalf("filtered selector = %q", output.String())
	}
}
