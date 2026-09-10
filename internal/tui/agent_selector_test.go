package tui

import (
	"bytes"
	"strings"
	"testing"

	"qcode/internal/session"
)

func TestSelectAgentMovesAndShowsKnowledge(t *testing.T) {
	entries := []agentSelectorEntry{
		{summary: session.Summary{ID: "main", Name: "main", Model: "model", Status: session.StatusIdle}, active: true, knowledge: "main finding"},
		{summary: session.Summary{ID: "agent-1", Name: "worker", Model: "worker-model", Status: session.StatusCompleted}, knowledge: "worker finding\nchanged files: game.js"},
	}
	var output bytes.Buffer
	selected, accepted, err := selectAgent(strings.NewReader("\x1b[B\r"), &output, entries, "main", 2, 100, false)
	if err != nil || !accepted || selected != "agent-1" {
		t.Fatalf("selection = %q, accepted = %v, err = %v", selected, accepted, err)
	}
	text := output.String()
	for _, want := range []string{"Ctrl+C to leave", "main", "worker-model", "knowledge: worker finding", "changed files: game.js"} {
		if !strings.Contains(text, want) {
			t.Fatalf("selector output missing %q: %q", want, text)
		}
	}
}

func TestAgentKnowledgePreviewIsBounded(t *testing.T) {
	knowledge := strings.Join([]string{"one", "two", "three", "four", "five", "six"}, "\n")
	lines := agentKnowledgeLines(knowledge, 80)
	if len(lines) != maxAgentKnowledgeLines || lines[len(lines)-1] != "..." {
		t.Fatalf("knowledge lines = %#v", lines)
	}
	if got := agentKnowledgeLines("", 80); len(got) != 1 || got[0] != "none recorded" {
		t.Fatalf("empty knowledge = %#v", got)
	}
}

func TestSelectAgentCanCancel(t *testing.T) {
	entries := []agentSelectorEntry{{summary: session.Summary{ID: "main"}}}
	selected, accepted, err := selectAgent(strings.NewReader(string([]byte{ctrlC})), &bytes.Buffer{}, entries, "main", 1, 80, false)
	if err != nil || accepted || selected != "" {
		t.Fatalf("cancel = %q, accepted = %v, err = %v", selected, accepted, err)
	}
}
