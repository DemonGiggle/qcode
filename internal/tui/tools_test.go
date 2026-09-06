package tui

import (
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
			accepted, err := selectTools(strings.NewReader(tc.input), io.Discard, runner.ToolNames(), runner, false)
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
