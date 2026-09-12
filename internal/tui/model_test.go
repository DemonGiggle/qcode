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
