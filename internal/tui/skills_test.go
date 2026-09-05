package tui

import (
	"bytes"
	"strings"
	"testing"

	"qcode/internal/prompt"
)

func TestSkillSelectorRefreshReplacesExistingRows(t *testing.T) {
	skills := []prompt.SkillSummary{
		{Name: "review", Description: "Review code"},
		{Name: "test", Description: "Run tests"},
	}
	var output bytes.Buffer

	renderSkillSelector(&output, skills, map[int]bool{}, 0, false)
	clearSkillSelector(&output, len(skills))
	renderSkillSelector(&output, skills, map[int]bool{1: true}, 1, false)

	got := output.String()
	wantClear := "\x1b[1A\r\x1b[2K\x1b[1A\r\x1b[2K"
	if !strings.Contains(got, wantClear) {
		t.Fatalf("refresh clear sequence = %q, want %q", got, wantClear)
	}
	if strings.Contains(got, "\x1b[1B") {
		t.Fatalf("refresh must not move downward: %q", got)
	}
	if !strings.HasSuffix(got, "> [x] test               Run tests\n") {
		t.Fatalf("refreshed selector = %q, want updated selection", got)
	}
}

func TestSelectSkillsNavigatesTogglesAndKeepsCatalogOrder(t *testing.T) {
	skills := []prompt.SkillSummary{
		{Name: "first", Description: "First skill"},
		{Name: "second", Description: "Second skill"},
		{Name: "third", Description: "Third skill"},
	}
	// Select first, move to second, select it, wrap back to first, then finish.
	input := strings.NewReader(" \x1b[B \x1b[A\r")
	var output bytes.Buffer

	names, summaries, accepted, err := selectSkills(input, &output, skills, false)
	if err != nil {
		t.Fatal(err)
	}
	if !accepted {
		t.Fatal("selection was not accepted")
	}
	if got, want := strings.Join(names, ","), "first,second"; got != want {
		t.Fatalf("names = %q, want %q", got, want)
	}
	if got, want := summaries[0].Name+","+summaries[1].Name, "first,second"; got != want {
		t.Fatalf("summaries = %q, want %q", got, want)
	}
	if got := strings.Count(output.String(), "\x1b[1A\r\x1b[2K"); got != 15 {
		t.Fatalf("clear operations = %d, want 15 (three rows after five key presses)", got)
	}
}

func TestSelectSkillsCancelsAndIgnoresUnknownKeys(t *testing.T) {
	skills := []prompt.SkillSummary{{Name: "review", Description: "Review code"}}
	// 'x', Backspace and an unknown escape sequence must not select anything.
	input := strings.NewReader("x\x7f\x1b[Z" + string([]byte{ctrlC}))
	var output bytes.Buffer

	names, summaries, accepted, err := selectSkills(input, &output, skills, false)
	if err != nil {
		t.Fatal(err)
	}
	if accepted || names != nil || summaries != nil {
		t.Fatalf("cancel result = names %v, summaries %v, accepted %v", names, summaries, accepted)
	}
	if strings.Contains(output.String(), "[x]") {
		t.Fatalf("unknown input changed selection: %q", output.String())
	}
}
