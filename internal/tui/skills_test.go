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
	src := skillContextSource{sys: skillSysTokens()}
	renderSkillSelector(&output, skills, matches, map[int]int{}, 0, 0, 2, 80, "", false, false, src)
	replaceSelectorRow(&output, skillSelectorRows(2), 1, renderSkillLine(skills[0], skillOff, false, 80, false, false))
	replaceSelectorRow(&output, skillSelectorRows(2), 2, renderSkillLine(skills[1], skillLazy, true, 80, false, false))

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
	header := renderSkillHeader(skills, []int{0, 1}, map[int]int{1: skillLazy}, "", 120, false, false)
	if !strings.Contains(header, "Enabled: deploy") || !strings.Contains(header, "Filter:") {
		t.Fatalf("skill header = %q", header)
	}
	if !strings.Contains(header, "L1") || !strings.Contains(header, "A0") {
		t.Fatalf("skill header missing lazy/always counts: %q", header)
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

	names, always, summaries, accepted, err := selectSkills(input, &output, skills, nil, 3, 80, false, false, skillContextSource{sys: skillSysTokens()})
	if err != nil {
		t.Fatal(err)
	}
	if !accepted {
		t.Fatal("selection was not accepted")
	}
	if got, want := strings.Join(names, ","), "first,second"; got != want {
		t.Fatalf("names = %q, want %q", got, want)
	}
	if len(always) != 0 {
		t.Fatalf("always = %q, want none for single Space presses", always)
	}
	if got, want := summaries[0].Name+","+summaries[1].Name, "first,second"; got != want {
		t.Fatalf("summaries = %q, want %q", got, want)
	}
	if got := strings.Count(output.String(), "\n"); got != skillSelectorRows(3) {
		t.Fatalf("rendered lines = %d, want header + list + footer", got)
	}
	if !strings.Contains(output.String(), "Context ") {
		t.Fatalf("missing footer context bar: %q", output.String())
	}
}

func TestSelectSkillsKeepsExistingSelection(t *testing.T) {
	skills := []prompt.SkillSummary{
		{Name: "first", Description: "First skill"},
		{Name: "second", Description: "Second skill"},
	}
	var output bytes.Buffer
	names, _, summaries, accepted, err := selectSkills(strings.NewReader("\r"), &output, skills, map[int]int{0: skillLazy}, 3, 80, false, false, skillContextSource{sys: skillSysTokens()})
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

func TestSelectSkillsThreeStateCycle(t *testing.T) {
	skills := []prompt.SkillSummary{{Name: "review", Description: "Review code"}}
	// Space three times: off->lazy->always->off, then Space once more to lazy and accept.
	input := strings.NewReader("   \r")
	var output bytes.Buffer
	names, always, summaries, accepted, err := selectSkills(input, &output, skills, nil, 1, 80, false, false, skillContextSource{sys: skillSysTokens()})
	if err != nil || !accepted {
		t.Fatalf("accepted = %v, err = %v", accepted, err)
	}
	if len(names) != 0 || len(always) != 0 || len(summaries) != 0 {
		t.Fatalf("three Spaces should cycle back to off: names=%v always=%v", names, always)
	}
	input = strings.NewReader("  \r")
	output.Reset()
	names, always, summaries, accepted, err = selectSkills(input, &output, skills, nil, 1, 80, false, false, skillContextSource{sys: skillSysTokens()})
	if err != nil || !accepted {
		t.Fatalf("accepted = %v, err = %v", accepted, err)
	}
	if got := strings.Join(names, ","); got != "review" {
		t.Fatalf("names = %q, want review", got)
	}
	if got := strings.Join(always, ","); got != "review" {
		t.Fatalf("always = %q, want review after two Spaces", got)
	}
	if len(summaries) != 1 || !summaries[0].AlwaysLoad {
		t.Fatalf("summaries = %+v, want AlwaysLoad", summaries)
	}
	if !strings.Contains(output.String(), "[*]") {
		t.Fatalf("selector output missing always box: %q", output.String())
	}
}

func TestRenderSkillLineShowsAutoloadAndSize(t *testing.T) {
	skill := prompt.SkillSummary{Name: "review", Description: "Review code", Autoload: true, SizeBytes: 2048}
	line := renderSkillLine(skill, skillLazy, false, 80, false, false)
	if !strings.Contains(line, "[x]") || !strings.Contains(line, "(auto)") || !strings.Contains(line, "2.0k") {
		t.Fatalf("lazy autoload line = %q", line)
	}
	line = renderSkillLine(skill, skillAlways, false, 80, false, false)
	if !strings.Contains(line, "[*]") || !strings.Contains(line, "(auto-always)") {
		t.Fatalf("always autoload line = %q", line)
	}
	plain := prompt.SkillSummary{Name: "test", Description: "Run tests"}
	line = renderSkillLine(plain, skillLazy, false, 80, false, false)
	if !strings.Contains(line, "> [x]") && !strings.Contains(line, "[x]") {
		t.Fatalf("plain lazy line = %q", line)
	}
	unicodeLine := renderSkillLine(plain, skillAlways, false, 80, false, true)
	if !strings.Contains(unicodeLine, "★") {
		t.Fatalf("unicode always line = %q", unicodeLine)
	}
}

func TestRenderSkillContextBar(t *testing.T) {
	selected := []prompt.SkillSummary{
		{Name: "a", Description: "A", AlwaysLoad: true, SizeBytes: 2048},
		{Name: "b", Description: "B"},
	}
	_, _, total := prompt.ContextEstimates(prompt.System, selected)
	bar := renderSkillContextBar(selected, total, 16, false)
	if !strings.Contains(bar, "#") || !strings.Contains(bar, "=") || !strings.Contains(bar, "total unknown") {
		t.Fatalf("ascii bar = %q", bar)
	}
	unicodeBar := renderSkillContextBar(selected, total, 16, true)
	if !strings.Contains(unicodeBar, "█") || !strings.Contains(unicodeBar, "░") {
		t.Fatalf("unicode bar = %q", unicodeBar)
	}
}

func TestSkillFooterShowsTotalComposition(t *testing.T) {
	sys := skillSysTokens()
	src := skillContextSource{total: 128000, known: true, remainingPct: 90, other: 2000, sys: sys}
	selected := []prompt.SkillSummary{
		{Name: "a", Description: "A", AlwaysLoad: true, SizeBytes: 8000},
		{Name: "b", Description: "B"},
	}
	summaries := selectedSummariesForBreakdown(selected, map[int]int{0: skillAlways, 1: skillLazy})
	bd := skillBreakdownForSelected(summaries, src)
	if !bd.known || bd.total != 128000 || bd.sys != sys || bd.must <= 0 || bd.lazy <= 0 || bd.other != 2000 || bd.free <= 0 {
		t.Fatalf("breakdown = %+v", bd)
	}
	if bd.used+bd.free != bd.total {
		t.Fatalf("used+free must equal total: %+v", bd)
	}
	bar := renderSkillContextBarV2(bd, 20, false, false)
	for _, want := range []string{"#", "M", "=", "~", "-", "/"} {
		if !strings.Contains(bar, want) {
			t.Fatalf("total bar missing %q: %q", want, bar)
		}
	}
	legend := renderSkillLegend(bd, 120, false, false)
	for _, want := range []string{"sys", "must", "lazy", "other", "free", "total"} {
		if !strings.Contains(legend, want) {
			t.Fatalf("legend missing %q: %q", want, legend)
		}
	}
	unicodeBar := renderSkillContextBarV2(bd, 20, true, false)
	for _, want := range []string{"█", "▓", "░", "▒", "·"} {
		if !strings.Contains(unicodeBar, want) {
			t.Fatalf("unicode bar missing %q: %q", want, unicodeBar)
		}
	}
}

func TestSkillFooterIgnoresTotalWhenUnknown(t *testing.T) {
	src := skillContextSource{sys: skillSysTokens()}
	selected := []prompt.SkillSummary{{Name: "a", Description: "A", AlwaysLoad: true, SizeBytes: 2048}}
	summaries := selectedSummariesForBreakdown(selected, map[int]int{0: skillAlways})
	bd := skillBreakdownForSelected(summaries, src)
	if bd.known || bd.free != 0 {
		t.Fatalf("unknown total should have no free segment: %+v", bd)
	}
	bar := renderSkillContextBarV2(bd, 16, false, false)
	if !strings.Contains(bar, "total unknown") {
		t.Fatalf("bar should note unknown total: %q", bar)
	}
	legend := renderSkillLegend(bd, 120, false, false)
	if !strings.Contains(legend, "total unknown") || strings.Contains(legend, "free") {
		t.Fatalf("legend should omit free when unknown: %q", legend)
	}
}

func TestSkillListScrollbar(t *testing.T) {
	if got := skillScrollbarChar(0, 0, 5, 30, false); got == "" {
		t.Fatalf("scrollable list should show scrollbar")
	}
	if got := skillScrollbarChar(0, 0, 5, 5, false); got != "" {
		t.Fatalf("non-scrollable list should not reserve scrollbar: %q", got)
	}
	line := renderSkillLineWithScrollbar("  [ ] skill", 0, 0, 5, 30, 40, false)
	if visibleWidth(line) != 40 {
		t.Fatalf("scrollbar row must keep width: %q width=%d", line, visibleWidth(line))
	}
}

func TestParseSkillToken(t *testing.T) {
	for _, tc := range []struct {
		raw    string
		name   string
		always bool
	}{
		{"review", "review", false},
		{"review!", "review", true},
		{"review:always", "review", true},
		{"review:always-load", "review", true},
		{"review:lazy", "review", false},
		{"none", "none", false},
	} {
		name, always := parseSkillToken(tc.raw)
		if name != tc.name || always != tc.always {
			t.Fatalf("parse %q = (%q,%v), want (%q,%v)", tc.raw, name, always, tc.name, tc.always)
		}
	}
}

func TestSelectSkillsPagesThroughBoundedViewport(t *testing.T) {
	skills := make([]prompt.SkillSummary, 30)
	for i := range skills {
		skills[i] = prompt.SkillSummary{Name: fmt.Sprintf("skill-%02d", i), Description: strings.Repeat("description ", 20)}
	}
	var output bytes.Buffer
	names, _, _, accepted, err := selectSkills(strings.NewReader(selectorPageDown+" \r"), &output, skills, nil, 5, 40, false, false, skillContextSource{sys: skillSysTokens()})
	if err != nil || !accepted || len(names) != 1 || names[0] != "skill-05" {
		t.Fatalf("names = %v, accepted = %v, err = %v", names, accepted, err)
	}
	if lines := strings.Count(output.String(), "\n"); lines != 2*skillSelectorRows(5) {
		t.Fatalf("rendered lines = %d, want two bounded pages with headers+lists+footers", lines)
	}
	if !strings.Contains(output.String(), "Context ") {
		t.Fatalf("missing footer in paged render: %q", output.String())
	}
	// Paged list must keep its own scroll region with a visible scrollbar.
	if !strings.Contains(output.String(), "│") && !strings.Contains(output.String(), "|") && !strings.Contains(output.String(), "┃") && !strings.Contains(output.String(), "#") {
		t.Fatalf("missing list scrollbar: %q", output.String())
	}
}

func TestSelectSkillsFiltersByNameAndDescription(t *testing.T) {
	skills := []prompt.SkillSummary{
		{Name: "review", Description: "Review code"},
		{Name: "test", Description: "Run tests"},
		{Name: "deploy", Description: "Publish a release"},
	}
	var output bytes.Buffer
	names, _, _, accepted, err := selectSkills(strings.NewReader("publish \r"), &output, skills, nil, 3, 120, false, false, skillContextSource{sys: skillSysTokens()})
	if err != nil || !accepted || strings.Join(names, ",") != "deploy" {
		t.Fatalf("names = %v, accepted = %v, err = %v", names, accepted, err)
	}
	got := output.String()
	if !strings.Contains(got, selectorLeaveHint) || !strings.Contains(got, "Select skills (1/3)") || !strings.Contains(got, "Enabled: none") || !strings.Contains(got, "Filter: publish") {
		t.Fatalf("filtered selector = %q", got)
	}
}

func TestSelectSkillsCancelsAndIgnoresUnknownKeys(t *testing.T) {
	skills := []prompt.SkillSummary{{Name: "review", Description: "Review code"}}
	// 'x', Backspace and an unknown escape sequence must not select anything.
	input := strings.NewReader("x\x7f\x1b[Z" + string([]byte{ctrlC}))
	var output bytes.Buffer

	names, always, summaries, accepted, err := selectSkills(input, &output, skills, nil, 1, 80, false, false, skillContextSource{sys: skillSysTokens()})
	if err != nil {
		t.Fatal(err)
	}
	if accepted || names != nil || summaries != nil || always != nil {
		t.Fatalf("cancel result = names %v, always %v, summaries %v, accepted %v", names, always, summaries, accepted)
	}
	if strings.Contains(output.String(), "[x]") {
		t.Fatalf("unknown input changed selection: %q", output.String())
	}
}

func TestSelectSkillsEscapeLeavesSelector(t *testing.T) {
	skills := []prompt.SkillSummary{{Name: "review", Description: "Review code"}}
	names, always, summaries, accepted, err := selectSkills(strings.NewReader("\x1b"), &bytes.Buffer{}, skills, nil, 1, 80, false, false, skillContextSource{sys: skillSysTokens()})
	if err != nil || accepted || names != nil || summaries != nil || always != nil {
		t.Fatalf("escape result = names %v, always %v, summaries %v, accepted %v, err %v", names, always, summaries, accepted, err)
	}
}

func TestSkillsPreferenceMainTabOnly(t *testing.T) {
	recorder := &runtimePreferenceRecorder{}
	u := &UI{manager: &preferenceAgentController{}, activeAgent: "agent-1", display: newHistoryWriter(&bytes.Buffer{}), runtimePreferences: recorder}
	u.persistSkillsPreference([]string{"review"}, nil)
	if recorder.skillsCalls != 0 {
		t.Fatalf("child tab persisted skills: %+v", recorder)
	}
	u.activeAgent = "main"
	u.persistSkillsPreference([]string{"review"}, []string{"review"})
	if recorder.skillsCalls != 1 || len(recorder.selected) != 1 || recorder.selected[0] != "review" || len(recorder.selectedAlways) != 1 {
		t.Fatalf("main tab persistence = %+v", recorder)
	}
	u.persistSkillsPreference(nil, nil)
	if recorder.skillsCalls != 2 || len(recorder.selected) != 0 || len(recorder.selectedAlways) != 0 {
		t.Fatalf("cleared selection persistence = %+v", recorder)
	}
}
