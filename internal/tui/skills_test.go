package tui

import (
	"bytes"
	"fmt"
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

	matches := matchingSkillIndices(skills, "")
	renderSkillSelector(&output, skills, matches, map[int]bool{}, 0, 0, 2, 80, "", false)
	replaceSelectorRow(&output, 3, 1, renderSkillLine(skills[0], false, false, 80, false))
	replaceSelectorRow(&output, 3, 2, renderSkillLine(skills[1], true, true, 80, false))

	got := output.String()
	if clears := strings.Count(got, "\x1b[2K"); clears != 2 {
		t.Fatalf("row clears = %d, want 2: %q", clears, got)
	}
	if !strings.Contains(got, "\x1b[2K> [x] test               Run tests\x1b[u") {
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

func TestFormatSkillSelection(t *testing.T) {
	plain := formatSkillSelection("Currently enabled", []string{"review", "deploy"}, 80, false, false, "")
	if plain != "Currently enabled (2): review, deploy" {
		t.Fatalf("plain summary = %q", plain)
	}
	if got := formatSkillSelection("Skills enabled", nil, 80, false, false, ""); got != "Skills enabled: none" {
		t.Fatalf("empty summary = %q", got)
	}
	colored := formatSkillSelection("Skills enabled", []string{"review"}, 80, false, true, green)
	if !strings.Contains(colored, green+"Skills enabled"+reset) || !strings.Contains(colored, cyan+"review"+reset) {
		t.Fatalf("colored summary = %q", colored)
	}
}

func TestRenderSkillHeaderShowsEnabledNames(t *testing.T) {
	skills := []prompt.SkillSummary{{Name: "review"}, {Name: "deploy"}}
	header := renderSkillHeader(skills, []int{0, 1}, map[int]bool{1: true}, "", 80, false)
	if !strings.Contains(header, "Enabled: deploy") || !strings.Contains(header, "Filter:") {
		t.Fatalf("skill header = %q", header)
	}
}

func TestFormatSkillSelectionTruncatesToWidth(t *testing.T) {
	got := formatSkillSelection("Currently enabled", []string{"a-very-long-skill-name", "another-skill"}, 24, false, false, "")
	if visibleWidth(got) > 24 || !strings.Contains(got, "...") {
		t.Fatalf("truncated summary width = %d, value = %q", visibleWidth(got), got)
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

	names, summaries, accepted, err := selectSkills(input, &output, skills, nil, 3, 80, false)
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
	if got := strings.Count(output.String(), "\n"); got != 4 {
		t.Fatalf("rendered lines = %d, want one initial header and three-row render", got)
	}
}

func TestSelectSkillsKeepsExistingSelection(t *testing.T) {
	skills := []prompt.SkillSummary{
		{Name: "first", Description: "First skill"},
		{Name: "second", Description: "Second skill"},
	}
	var output bytes.Buffer
	names, summaries, accepted, err := selectSkills(strings.NewReader("\r"), &output, skills, map[int]bool{0: true}, 3, 80, false)
	if err != nil || !accepted {
		t.Fatalf("accepted = %v, err = %v", accepted, err)
	}
	if got := strings.Join(names, ","); got != "first" {
		t.Fatalf("names = %q, want existing selection", got)
	}
	if len(summaries) != 1 || summaries[0].Name != "first" {
		t.Fatalf("summaries = %v", summaries)
	}
}

func TestSelectSkillsPagesThroughBoundedViewport(t *testing.T) {
	skills := make([]prompt.SkillSummary, 30)
	for i := range skills {
		skills[i] = prompt.SkillSummary{Name: fmt.Sprintf("skill-%02d", i), Description: strings.Repeat("description ", 20)}
	}
	var output bytes.Buffer
	names, _, accepted, err := selectSkills(strings.NewReader(selectorPageDown+" \r"), &output, skills, nil, 5, 40, false)
	if err != nil || !accepted || len(names) != 1 || names[0] != "skill-05" {
		t.Fatalf("names = %v, accepted = %v, err = %v", names, accepted, err)
	}
	if lines := strings.Count(output.String(), "\n"); lines != 12 {
		t.Fatalf("rendered lines = %d, want two bounded five-row pages with headers", lines)
	}
}

func TestSelectSkillsFiltersByNameAndDescription(t *testing.T) {
	skills := []prompt.SkillSummary{
		{Name: "review", Description: "Review code"},
		{Name: "test", Description: "Run tests"},
		{Name: "deploy", Description: "Publish a release"},
	}
	var output bytes.Buffer
	names, _, accepted, err := selectSkills(strings.NewReader("publish \r"), &output, skills, nil, 3, 80, false)
	if err != nil || !accepted || strings.Join(names, ",") != "deploy" {
		t.Fatalf("names = %v, accepted = %v, err = %v", names, accepted, err)
	}
	if !strings.Contains(output.String(), selectorLeaveHint) || !strings.Contains(output.String(), "Select skills (1/3) | Enabled: none | Filter: publish") {
		t.Fatalf("filtered selector = %q", output.String())
	}
}

func TestSelectSkillsCancelsAndIgnoresUnknownKeys(t *testing.T) {
	skills := []prompt.SkillSummary{{Name: "review", Description: "Review code"}}
	// 'x', Backspace and an unknown escape sequence must not select anything.
	input := strings.NewReader("x\x7f\x1b[Z" + string([]byte{ctrlC}))
	var output bytes.Buffer

	names, summaries, accepted, err := selectSkills(input, &output, skills, nil, 1, 80, false)
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
