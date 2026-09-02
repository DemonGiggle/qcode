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
	if !strings.Contains(output.String(), "Select model (2/3)  Search: code") {
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
