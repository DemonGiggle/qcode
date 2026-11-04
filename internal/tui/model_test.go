package tui

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestSelectModelFiltersThenSelects(t *testing.T) {
	models := []string{"alpha-code", "beta-chat", "gamma-code"}
	input := strings.NewReader("code" + arrowDownSequence + "\r")
	var output bytes.Buffer

	selected, accepted, err := selectModel(input, &output, models, "alpha-code", 3, 80, false)
	if err != nil {
		t.Fatal(err)
	}
	if !accepted || selected != "gamma-code" {
		t.Fatalf("selection = %q, %v", selected, accepted)
	}
	if !strings.Contains(output.String(), selectorLeaveHint) || !strings.Contains(output.String(), "Select model (2/3) | Up/Down, PgUp/PgDn | Search: code") {
		t.Fatalf("selector did not show filtered count: %q", output.String())
	}
}

func TestSelectModelSearchHandlesLargeCatalog(t *testing.T) {
	models := make([]string, 500)
	for index := range models {
		models[index] = fmt.Sprintf("model-%03d", index)
	}
	var output bytes.Buffer
	selected, accepted, err := selectModel(strings.NewReader("model-499\r"), &output, models, "", 10, 80, false)
	if err != nil {
		t.Fatal(err)
	}
	if !accepted || selected != "model-499" {
		t.Fatalf("selection = %q, %v", selected, accepted)
	}
	if lines := strings.Count(output.String(), "\n"); lines > 110 {
		t.Fatalf("selector rendered %d lines for a 10-row viewport", lines)
	}
}

func TestSelectModelCanCancel(t *testing.T) {
	selected, accepted, err := selectModel(strings.NewReader(string([]byte{ctrlC})), &bytes.Buffer{}, []string{"one"}, "", 1, 80, false)
	if err != nil || accepted || selected != "" {
		t.Fatalf("selection = %q, %v, %v", selected, accepted, err)
	}
}

func TestSelectModelSupportsPageNavigation(t *testing.T) {
	models := make([]string, 30)
	for i := range models {
		models[i] = fmt.Sprintf("model-%02d", i)
	}
	var output bytes.Buffer
	selected, accepted, err := selectModel(strings.NewReader(selectorPageDown+"\r"), &output, models, "", 5, 80, false)
	if err != nil || !accepted || selected != "model-05" {
		t.Fatalf("selection = %q, accepted = %v, err = %v", selected, accepted, err)
	}
	if lines := strings.Count(output.String(), "\n"); lines != 12 {
		t.Fatalf("rendered lines = %d, want two bounded six-row renders", lines)
	}
}

func TestSelectThinkingShowsOnlyThinkingLevels(t *testing.T) {
	var output bytes.Buffer
	selected, accepted, err := selectThinking(strings.NewReader(arrowDownSequence+"\r"), &output, []string{"low", "high"}, "low", 2, 80, false)
	if err != nil || !accepted || selected != "high" {
		t.Fatalf("selection = %q, accepted = %v, err = %v", selected, accepted, err)
	}
	if got := output.String(); !strings.Contains(got, "Select thinking level (2/2)") || strings.Contains(got, "Search:") {
		t.Fatalf("thinking selector output = %q", got)
	}
}

func TestSelectThinkingEscapeReturnsToParent(t *testing.T) {
	selected, accepted, back, err := selectThinkingWithBack(strings.NewReader("\x1b"), &bytes.Buffer{}, []string{"low", "high"}, "low", 2, 80, false)
	if err != nil || accepted || !back || selected != "" {
		t.Fatalf("selection = %q, accepted = %v, back = %v, err = %v", selected, accepted, back, err)
	}
}

func TestSelectModelBackPreservesQueryAndSelection(t *testing.T) {
	models := []string{"alpha-code", "beta-code", "gamma-chat"}
	var output bytes.Buffer
	selected, result, query, err := selectModelWithQuery(strings.NewReader("code"+arrowDownSequence+"\x1b"), &output, models, "alpha-code", "", 3, 80, false)
	if err != nil || result != selectorBack || selected != "beta-code" || query != "code" {
		t.Fatalf("back result = %q, %v, query %q, err %v", selected, result, query, err)
	}

	selected, result, query, err = selectModelWithQuery(strings.NewReader("\r"), &output, models, selected, query, 3, 80, false)
	if err != nil || result != selectorAccepted || selected != "beta-code" || query != "code" {
		t.Fatalf("restored result = %q, %v, query %q, err %v", selected, result, query, err)
	}
}

func TestSelectorHeaderKeepsLeaveHintVisible(t *testing.T) {
	header := selectorHeader("Select model (2/3) | Up/Down, PgUp/PgDn | Search: a very long query", 40)
	if visibleWidth(header) > 40 || !strings.HasSuffix(header, selectorLeaveHint) {
		t.Fatalf("header = %q, width = %d", header, visibleWidth(header))
	}
}

func TestSelectOptionWithDisabledSkipsUnselectable(t *testing.T) {
	options := []string{"alpha", "beta (Need Setup First)", "gamma"}
	disabled := map[string]bool{"beta (Need Setup First)": true}
	var output bytes.Buffer
	// From alpha, Down must skip disabled beta and land on gamma.
	selected, accepted, err := selectOptionWithDisabled(strings.NewReader(arrowDownSequence+"\r"), &output, options, disabled, "alpha", 3, 80, false, "provider", false)
	if err != nil || !accepted || selected != "gamma" {
		t.Fatalf("down skipped = %q, %v, %v", selected, accepted, err)
	}
	// Up from alpha wraps to gamma, skipping disabled.
	output.Reset()
	selected, accepted, err = selectOptionWithDisabled(strings.NewReader(arrowUpSequence+"\r"), &output, options, disabled, "alpha", 3, 80, false, "provider", false)
	if err != nil || !accepted || selected != "gamma" {
		t.Fatalf("up wrap = %q, %v, %v", selected, accepted, err)
	}
	// Enter on a fully disabled list is ignored; Esc then leaves.
	output.Reset()
	selected, result, err := selectOptionWithResultAndDisabled(strings.NewReader("\r\x1b"), &output, []string{"a (Need Setup First)"}, map[string]bool{"a (Need Setup First)": true}, "", 1, 80, false, "provider", false)
	if err != nil || result != selectorBack || selected != "" {
		t.Fatalf("disabled enter = %q, %v, %v", selected, result, err)
	}
	// Disabled rows render without the active highlight.
	output.Reset()
	line := renderModelLineWithDisabled(options, []int{0, 1, 2}, disabled, 1, false, 80, false)
	if !strings.Contains(line, "beta (Need Setup First)") {
		t.Fatalf("disabled line = %q", line)
	}
	// Disabled rows show grey when color is enabled.
	greyLine := renderModelLineWithDisabled(options, []int{0, 1, 2}, disabled, 1, false, 80, true)
	if !strings.Contains(greyLine, gray) || !strings.Contains(greyLine, "beta (Need Setup First)") {
		t.Fatalf("disabled grey line = %q", greyLine)
	}
	// Enabled selection keeps the active highlight, not grey.
	activeLine := renderModelLineWithDisabled(options, []int{0, 1, 2}, disabled, 2, true, 80, true)
	if strings.Contains(activeLine, gray) {
		t.Fatalf("enabled line should not be grey = %q", activeLine)
	}
}
