package tui

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestOpenCodeGoBackendChanged(t *testing.T) {
	tests := []struct {
		provider, oldModel, newModel string
		want                         bool
	}{
		{"opencode-go", "minimax-m3", "muse-spark-1.3-contributor", true},
		{"opencode-go", "minimax-m3", "minimax-m2.7", false},
		{"opencode-go", "muse-spark-1.2-contributor", "muse-spark-1.3-contributor", false},
		{"opencode-go", "kimi-k3", "minimax-m3", true},
		{"opencode-go", "", "muse-spark-1.3-contributor", false},
		{"opencode-go", "minimax-m3", "minimax-m3", false},
		{"ollama", "qwen3:8b", "gemma3", false},
		{"openai", "gpt-5", "gpt-5-mini", false},
	}
	for _, test := range tests {
		if got := openCodeGoBackendChanged(test.provider, test.oldModel, test.newModel); got != test.want {
			t.Errorf("openCodeGoBackendChanged(%q,%q,%q) = %v, want %v", test.provider, test.oldModel, test.newModel, got, test.want)
		}
	}
}

func TestSetModelCommandWarnsOnBackendChange(t *testing.T) {
	var output bytes.Buffer
	runner := &configurableModelRunner{
		models: []string{"minimax-m3", "muse-spark-1.3-contributor"},
	}
	u := &UI{
		runner:  runner,
		display: newHistoryWriter(&output),
		input:   newInterruptReader(strings.NewReader("")),
		provider: "opencode-go",
		model:    "minimax-m3",
	}
	u.setModelCommand(context.Background(), []string{"/model", "muse-spark-1.3-contributor"})
	if !strings.Contains(output.String(), "History kept. This model uses a different backend") {
		t.Fatalf("cross-backend switch did not warn: %q", output.String())
	}
	output.Reset()
	u.setModelCommand(context.Background(), []string{"/model", "minimax-m3"})
	// Switching back is also cross-backend, so it warns again.
	if !strings.Contains(output.String(), "History kept.") {
		t.Fatalf("switch back did not warn: %q", output.String())
	}
}

func TestSetModelCommandQuietOnSameBackend(t *testing.T) {
	var output bytes.Buffer
	runner := &configurableModelRunner{
		models: []string{"minimax-m3", "minimax-m2.7"},
	}
	u := &UI{
		runner:  runner,
		display: newHistoryWriter(&output),
		input:   newInterruptReader(strings.NewReader("")),
		provider: "opencode-go",
		model:    "minimax-m3",
	}
	u.setModelCommand(context.Background(), []string{"/model", "minimax-m2.7"})
	if strings.Contains(output.String(), "History kept.") {
		t.Fatalf("same-backend switch warned unexpectedly: %q", output.String())
	}
}
