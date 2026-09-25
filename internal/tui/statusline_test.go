package tui

import (
	"bytes"
	"strings"
	"testing"
)

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
	got := statusBarWithStatuslineHidden("ollama", "qwen", "/w", 60, true, false, false, nil, magenta, blue, "73% left", "I:1 O:2", "2/32")
	if !strings.Contains(got, "[STEP 2/32]") || strings.Contains(got, "[TOK ") {
		t.Fatalf("priority order wrong: %q", got)
	}
	if visibleWidth(got) > 60 {
		t.Fatalf("width = %d: %q", visibleWidth(got), got)
	}
}

func TestStatuslineShortensInteractiveBeforeDropping(t *testing.T) {
	wide := statusBarWithStatuslineHidden("ollama", "qwen", "~/code", 200, true, false, false, nil, magenta, blue, "73% left", "I:1 O:2", "", "INTERACTIVE")
	if !strings.Contains(wide, "[MODE INTERACTIVE]") {
		t.Fatalf("wide bar should keep full label: %q", wide)
	}
	// At a width where workspace shortening alone cannot close the gap, the
	// fitter squeezes MODE before dropping whole segments.
	fullWidth := visibleWidth(wide)
	narrowWidth := fullWidth - 8
	if narrowWidth <= 0 {
		t.Skip("bar too short to test shortening")
	}
	got := statusBarWithStatuslineHidden("ollama", "qwen", "~/code", narrowWidth, true, false, false, nil, magenta, blue, "73% left", "I:1 O:2", "", "INTERACTIVE")
	if !strings.Contains(got, "[MODE INT]") || visibleWidth(got) > narrowWidth {
		t.Fatalf("mode not shortened: %q width=%d want<=%d", got, visibleWidth(got), narrowWidth)
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
