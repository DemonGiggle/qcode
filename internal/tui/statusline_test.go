package tui

import (
	"bytes"
	"strings"
	"testing"
)

func TestStatuslineTwoLineAlignment(t *testing.T) {
	// Overflow wraps to a second left-aligned line: high priority first,
	// remainder second, each fitting its own width.
	got := statusBar("ollama", "qwen", "/w", 40, true, false, "73% left", "I:1 O:2", "2/32")
	lines := strings.Split(got, "\n")
	if len(lines) != 2 {
		t.Fatalf("want two lines, got %q", got)
	}
	for _, line := range lines {
		if visibleWidth(line) > 40 {
			t.Fatalf("line width = %d: %q", visibleWidth(line), line)
		}
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "  ") {
			t.Fatalf("line not left-aligned: %q", line)
		}
	}
	if !strings.Contains(lines[0], "ollama") || !strings.Contains(lines[1], "[TOK I:1 O:2]") {
		t.Fatalf("priority split wrong: %q", got)
	}
}

func TestStatuslineHiddenFiltering(t *testing.T) {
	full := statusBarWithStatuslineHidden("ollama", "qwen", "~/code", 120, true, false, false, nil, magenta, blue, "73% left", "I:1 O:2", "2/32", "PLAN", "high")
	for _, want := range []string{"ollama", "[MODEL qwen]", "[CTX 73% left]", "[WS ~/code]", "[TOK I:1 O:2]", "[STEP 2/32]", "[MODE PLAN]", "[THINK high]"} {
		if !strings.Contains(full, want) {
			t.Fatalf("full bar missing %q: %q", want, full)
		}
	}
	hidden := statusBarWithStatuslineHidden("ollama", "qwen", "~/code", 120, true, false, false, []string{"tok", "think"}, magenta, blue, "73% left", "I:1 O:2", "2/32", "PLAN", "high")
	if strings.Contains(hidden, "[TOK ") || strings.Contains(hidden, "[THINK ") {
		t.Fatalf("hidden segments remain: %q", hidden)
	}
	if !strings.Contains(hidden, "[STEP 2/32]") || !strings.Contains(hidden, "[CTX 73% left]") {
		t.Fatalf("visible segments missing: %q", hidden)
	}
	// The model toggle controls the combined provider prefix and MODEL unit.
	modelHidden := statusBarWithStatuslineHidden("ollama", "qwen", "~/code", 120, true, false, false, []string{"model"}, magenta, blue, "73% left")
	if strings.Contains(modelHidden, "ollama") || strings.Contains(modelHidden, "[MODEL ") {
		t.Fatalf("model unit remains: %q", modelHidden)
	}
}

func TestStatuslinePriorityDropsTokBeforeStep(t *testing.T) {
	// Even two lines can overflow: TOK (lowest) drops before STEP.
	got := statusBarWithStatuslineHidden("ollama", "qwen", "/w", 30, true, false, false, nil, magenta, blue, "73% left", "I:1 O:2", "2/32")
	lines := strings.Split(got, "\n")
	if len(lines) != 2 {
		t.Fatalf("priority order wrong, want two lines: %q", got)
	}
	for _, line := range lines {
		if visibleWidth(line) > 30 {
			t.Fatalf("width = %d: %q", visibleWidth(line), got)
		}
		if strings.HasPrefix(line, " ") {
			t.Fatalf("line not left-aligned: %q", line)
		}
	}
	if !strings.Contains(got, "[STEP 2/32]") || strings.Contains(got, "[TOK ") {
		t.Fatalf("priority order wrong: %q", got)
	}
}

func TestStatuslineShortensInteractiveBeforeDropping(t *testing.T) {
	wide := statusBarWithStatuslineHidden("ollama", "qwen", "~/code", 200, true, false, false, nil, magenta, blue, "73% left", "I:1 O:2", "", "INTERACTIVE")
	if !strings.Contains(wide, "[MODE INTERACTIVE]") {
		t.Fatalf("wide bar should keep full label: %q", wide)
	}
	// At 36 columns even two lines overflow with the full label, so the
	// fitter squeezes INTERACTIVE to INT (dropping TOK if needed).
	const narrowWidth = 36
	got := statusBarWithStatuslineHidden("ollama", "qwen", "~/code", narrowWidth, true, false, false, nil, magenta, blue, "73% left", "I:1 O:2", "", "INTERACTIVE")
	lines := strings.Split(got, "\n")
	for _, line := range lines {
		if visibleWidth(line) > narrowWidth {
			t.Fatalf("mode not shortened: %q width=%d want<=%d", got, visibleWidth(line), narrowWidth)
		}
		if strings.HasPrefix(line, " ") {
			t.Fatalf("line not left-aligned: %q", line)
		}
	}
	if !strings.Contains(got, "[MODE INT]") || strings.Contains(got, "INTERACTIVE") {
		t.Fatalf("mode not shortened: %q", got)
	}
}

func TestStatuslineCommandShowHideReset(t *testing.T) {
	var output bytes.Buffer
	u := &UI{display: newHistoryWriter(&output), runtimePreferences: &runtimePreferenceRecorder{}}
	u.SetStatuslineHidden(nil)

	u.handleStatuslineCommand([]string{"/statusline", "tok", "off"})
	if got := u.StatuslineHidden(); len(got) != 1 || got[0] != "tok" {
		t.Fatalf("hidden = %q", got)
	}
	bar := statusBarWithStatuslineHidden("ollama", "qwen", "~/code", 120, true, false, false, u.StatuslineHidden(), magenta, blue, "73% left", "I:1 O:2")
	if strings.Contains(bar, "[TOK ") {
		t.Fatalf("tok remains: %q", bar)
	}

	u.handleStatuslineCommand([]string{"/statusline", "show", "tok"})
	if len(u.StatuslineHidden()) != 0 {
		t.Fatalf("hidden after show = %q", u.StatuslineHidden())
	}

	u.handleStatuslineCommand([]string{"/statusline", "hide", "ctx, step"})
	hidden := statusHiddenSet(u.StatuslineHidden())
	if !hidden["ctx"] || !hidden["step"] {
		t.Fatalf("hidden after hide = %q", u.StatuslineHidden())
	}

	u.handleStatuslineCommand([]string{"/statusline", "reset"})
	if len(u.StatuslineHidden()) != 0 {
		t.Fatalf("hidden after reset = %q", u.StatuslineHidden())
	}

	u.handleStatuslineCommand([]string{"/statusline", "bogus", "off"})
	if len(u.StatuslineHidden()) != 0 {
		t.Fatalf("unknown segment changed hidden = %q", u.StatuslineHidden())
	}
	output.Reset()
	u.handleStatuslineCommand([]string{"/statusline", "show"})
	if !strings.Contains(output.String(), "Statusline hidden") {
		t.Fatalf("show output = %q", output.String())
	}
}

func TestStatuslinePreferenceMainTabOnly(t *testing.T) {
	recorder := &runtimePreferenceRecorder{}
	u := &UI{manager: &preferenceAgentController{}, activeAgent: "agent-1", display: newHistoryWriter(&bytes.Buffer{}), runtimePreferences: recorder}
	u.applyStatuslineHidden([]string{"tok"})
	if recorder.statusCalls != 0 {
		t.Fatalf("child tab persisted statusline: %+v", recorder)
	}
	u.activeAgent = "main"
	u.applyStatuslineHidden([]string{"tok"})
	if recorder.statusCalls != 1 || len(recorder.hidden) != 1 || recorder.hidden[0] != "tok" {
		t.Fatalf("main tab persistence = %+v", recorder)
	}
}

func TestFixedLayoutReservesTwoRowsForWrappedStatus(t *testing.T) {
	u, _ := layoutFixture(t)
	u.provider, u.model, u.root = "ollama", "qwen", "/w"
	u.width, u.height = 40, 24
	u.renderInput(inputPrompt, "", 0)
	first, second := u.inputScreenRows[u.height-1], u.inputScreenRows[u.height]
	if first == "" || second == "" {
		t.Fatalf("want two status rows, got %q and %q", first, second)
	}
	for _, line := range []string{first, second} {
		if visibleWidth(line) > u.width {
			t.Fatalf("status row width = %d: %q", visibleWidth(line), line)
		}
		if strings.HasPrefix(line, " ") {
			t.Fatalf("status row not left-aligned: %q", line)
		}
	}
	if !strings.Contains(first, "ollama") || !strings.Contains(second, "[TOK ") {
		t.Fatalf("priority split wrong: %q | %q", first, second)
	}
	// Single-line widths keep a single status row.
	wide, _ := layoutFixture(t)
	wide.provider, wide.model, wide.root = "ollama", "qwen", "/w"
	wide.width, wide.height = 120, 24
	wide.renderInput(inputPrompt, "", 0)
	if wide.inputScreenRows[wide.height] == "" {
		t.Fatal("want a status row")
	}
}
