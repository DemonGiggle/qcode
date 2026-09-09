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

func TestSkillLocationHintListsDiscoveryPaths(t *testing.T) {
	root := "/workspace/project"
	plain := skillLocationHint(root, false)
	for _, location := range []string{
		"~/.qcode/skills/<name>/SKILL.md",
		"/workspace/project/.agents/skills/<name>/SKILL.md",
		"/workspace/project/.qcode/skills/<name>/SKILL.md",
	} {
		if !strings.Contains(plain, location) {
			t.Fatalf("plain hint = %q, missing %q", plain, location)
		}
	}
	if strings.Contains(plain, "\x1b[") {
		t.Fatalf("plain hint contains ANSI escapes: %q", plain)
	}
	colored := skillLocationHint(root, true)
	if !strings.Contains(colored, dim+"Skills are loaded from:"+reset) || !strings.Contains(colored, cyan+"/workspace/project/.qcode/skills/<name>/SKILL.md"+reset) {
		t.Fatalf("colored hint = %q", colored)
	}
}

func TestSkillLocationHintSanitizesWorkspace(t *testing.T) {
	hint := skillLocationHint("/workspace/escape\x1b[31m", false)
	if strings.Contains(hint, "\x1b[") || !strings.Contains(hint, "<ESC>") {
		t.Fatalf("sanitized hint = %q", hint)
	}
}

func TestListSkillsIncludesDescriptionAndPath(t *testing.T) {
	var output bytes.Buffer
	u := UI{
		display:    newHistoryWriter(&output),
		skillInfos: []SkillInfo{{Name: "review", Description: "Review code", Path: "/workspace/.qcode/skills/review/SKILL.md"}},
	}
	u.listSkills()
	for _, want := range []string{"Skills found:", "review  Review code", "/workspace/.qcode/skills/review/SKILL.md"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("skills output = %q, missing %q", output.String(), want)
		}
	}
}

func TestListSkillsLoadsCatalogOnDemand(t *testing.T) {
	var output bytes.Buffer
	loads := 0
	u := UI{
		display: newHistoryWriter(&output),
		skillCatalogLoader: func() ([]prompt.SkillSummary, []SkillInfo, error) {
			loads++
			return []prompt.SkillSummary{{Name: "review", Description: "Review code"}}, []SkillInfo{{Name: "review", Description: "Review code", Path: "/skills/review/SKILL.md"}}, nil
		},
	}
	u.listSkills()
	if loads != 1 || !strings.Contains(output.String(), "/skills/review/SKILL.md") {
		t.Fatalf("loads = %d, output = %q", loads, output.String())
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
