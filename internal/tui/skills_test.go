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
