package tui

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"qcode/internal/prompt"
	"qcode/internal/redaction"
)

// Skill selector modes for the three-state Space cycle:
// off -> lazy -> always-load -> off.
const (
	skillOff = iota
	skillLazy
	skillAlways
)

func (u *UI) chooseSkills() {
	if !u.activeAgentConfigurable() {
		return
	}
	u.printSystemMessage(u.skillLocationHint(ColorEnabled(u.out)))
	if !u.ensureSkillCatalog() {
		return
	}
	runner, ok := u.runner.(skillRunner)
	if !ok || len(u.skills) == 0 {
		u.printSystemMessage(dim + "No workspace skills are available." + reset)
		return
	}
	initial := make(map[int]int)
	if selectedRunner, ok := runner.(selectedSkillsRunner); ok {
		modes := make(map[string]int)
		for _, skill := range selectedRunner.SelectedSkills() {
			if skill.AlwaysLoad {
				modes[skill.Name] = skillAlways
			} else {
				modes[skill.Name] = skillLazy
			}
		}
		for i, skill := range u.skills {
			if mode, found := modes[skill.Name]; found {
				initial[i] = mode
			}
		}
	}
	u.printSystemMessage(formatSkillSelectionModes("Currently enabled", selectedSkillNames(u.skills, initial), alwaysSkillNames(u.skills, initial), u.width, u.unicode, ColorEnabled(u.out), dim))
	u.input.setRaw(true)
	u.beginRawSelector()
	defer func() {
		u.input.setRaw(false)
		u.endRawSelector()
	}()
	visible := min(12, max(3, u.height-6))
	src := skillContextSourceFromRunner(runner, u.skills)
	names, alwaysNames, summaries, accepted, err := selectSkills(u.input, u.themedSelectorWriter(), redaction.Copy(u.redaction, redaction.Terminal, u.skills), initial, visible, u.width, ColorEnabled(u.out), u.unicode, src)
	if err != nil {
		return
	}
	if !accepted {
		u.printSystemMessage(dim + "Skill selection cancelled." + reset)
		return
	}
	// Resolve displayed selections against original skill metadata,
	// preserving size/autoload flags and applying the chosen modes.
	byName := make(map[string]prompt.SkillSummary, len(u.skills))
	for _, skill := range u.skills {
		byName[skill.Name] = skill
	}
	alwaysSet := map[string]bool{}
	for _, name := range alwaysNames {
		alwaysSet[name] = true
	}
	summaries = nil
	for _, name := range names {
		summary, found := byName[name]
		if !found {
			continue
		}
		summary.AlwaysLoad = alwaysSet[name]
		summaries = append(summaries, summary)
	}
	bodies, bodyErr := u.loadAlwaysBodies(alwaysNames)
	if bodyErr != nil {
		u.printSystemMessage(yellow + "Cannot load always skills, keeping them lazy: " + sanitizeDiffLine(bodyErr.Error(), "<ESC>") + reset)
		for i := range summaries {
			summaries[i].AlwaysLoad = false
		}
		alwaysNames = nil
		bodies = nil
	}
	if !u.configureActive(func() error {
		if u.onSkills != nil {
			u.onSkills(names, alwaysNames)
		}
		setRunnerSkills(runner, summaries, bodies)
		return nil
	}) {
		return
	}
	u.printSystemMessage(formatSkillSelectionModes("Skills enabled", names, alwaysNames, u.width, u.unicode, ColorEnabled(u.out), green))
}

func (u *UI) setSkillCommand(fields []string) {
	if !u.activeAgentConfigurable() {
		return
	}
	if !u.ensureSkillCatalog() {
		return
	}
	runner, ok := u.runner.(skillRunner)
	if !ok {
		u.printSystemMessage(dim + "Skill selection is unavailable." + reset)
		return
	}
	requested := strings.Split(strings.Join(fields[1:], " "), ",")
	byName := make(map[string]prompt.SkillSummary, len(u.skills))
	for _, skill := range u.skills {
		byName[skill.Name] = skill
	}
	var names []string
	var alwaysNames []string
	var summaries []prompt.SkillSummary
	alwaysSet := map[string]bool{}
	seen := map[string]bool{}
	for _, raw := range requested {
		name, always := parseSkillToken(raw)
		if name == "" {
			continue
		}
		if name == "none" && len(requested) == 1 {
			continue
		}
		summary, found := byName[name]
		if !found {
			u.printSystemMessage(yellow + "Unknown skill: " + sanitizeDiffLine(name, "<ESC>") + reset)
			return
		}
		if seen[name] {
			// Last occurrence wins for the mode.
			for i, existing := range names {
				if existing == name {
					names = append(names[:i], names[i+1:]...)
					for j, s := range summaries {
						if s.Name == name {
							summaries = append(summaries[:j], summaries[j+1:]...)
							break
						}
					}
					break
				}
			}
			delete(alwaysSet, name)
		}
		seen[name] = true
		summary.AlwaysLoad = always
		if always {
			alwaysSet[name] = true
			alwaysNames = append(alwaysNames, name)
		}
		names, summaries = append(names, name), append(summaries, summary)
	}
	// Preserve catalog order for the prompt.
	order := map[string]int{}
	for i, skill := range u.skills {
		order[skill.Name] = i
	}
	for i := 1; i < len(summaries); i++ {
		for j := i; j > 0 && order[summaries[j].Name] < order[summaries[j-1].Name]; j-- {
			summaries[j], summaries[j-1] = summaries[j-1], summaries[j]
			names[j], names[j-1] = names[j-1], names[j]
		}
	}
	bodies, bodyErr := u.loadAlwaysBodies(alwaysNames)
	if bodyErr != nil {
		u.printSystemMessage(yellow + "Cannot load always skills, keeping them lazy: " + sanitizeDiffLine(bodyErr.Error(), "<ESC>") + reset)
		for i := range summaries {
			summaries[i].AlwaysLoad = false
		}
		alwaysNames = nil
		bodies = nil
		// Rebuild names from downgraded summaries.
		names = names[:0]
		for _, summary := range summaries {
			names = append(names, summary.Name)
		}
	}
	if !u.configureActive(func() error {
		if u.onSkills != nil {
			u.onSkills(names, alwaysNames)
		}
		setRunnerSkills(runner, summaries, bodies)
		return nil
	}) {
		return
	}
	u.drawStatusBar()
	u.printSystemMessage(formatSkillSelectionModes("Skills enabled", names, alwaysNames, u.width, u.unicode, ColorEnabled(u.out), green))
}

// parseSkillToken accepts "name" (lazy), "name!" / "name:always" /
// "name:always-load" (always), and "name:lazy" (explicit lazy).
func parseSkillToken(raw string) (string, bool) {
	token := strings.TrimSpace(raw)
	if token == "" {
		return "", false
	}
	if strings.HasSuffix(token, "!") {
		return strings.TrimSpace(strings.TrimSuffix(token, "!")), true
	}
	if name, mode, ok := strings.Cut(token, ":"); ok {
		name = strings.TrimSpace(name)
		switch strings.ToLower(strings.TrimSpace(mode)) {
		case "always", "always-load", "always_load", "on", "eager":
			return name, true
		case "lazy", "off", "":
			return name, false
		default:
			return strings.TrimSpace(token), false
		}
	}
	return token, false
}

func (u *UI) loadAlwaysBodies(alwaysNames []string) (map[string]string, error) {
	if len(alwaysNames) == 0 {
		return nil, nil
	}
	u.screenMu.Lock()
	loader := u.skillBodyLoader
	u.screenMu.Unlock()
	if loader == nil {
		return nil, fmt.Errorf("skill body loader is unavailable")
	}
	return loader(alwaysNames)
}

func setRunnerSkills(runner skillRunner, summaries []prompt.SkillSummary, bodies map[string]string) {
	if alwaysRunner, ok := runner.(skillAlwaysRunner); ok {
		alwaysRunner.SetSkillsWithContents(summaries, bodies)
		return
	}
	runner.SetSkills(summaries)
}

func (u *UI) skillLocationHint(color bool) string {
	locations := u.skillLocations
	if len(locations) == 0 {
		return skillLocationHint(u.root, color)
	}
	return formatSkillLocationHint(locations, color)
}

func skillLocationHint(root string, color bool) string {
	workspace := sanitizeDiffLine(displayRoot(root), "<ESC>")
	locations := []string{
		"~/.qcode/skills",
		filepath.Join(workspace, ".agents", "skills"),
		filepath.Join(workspace, ".qcode", "skills"),
	}
	return formatSkillLocationHint(locations, color)
}

func formatSkillLocationHint(locations []string, color bool) string {
	lines := make([]string, 0, len(locations)+1)
	paths := make([]string, len(locations))
	for i, location := range locations {
		paths[i] = filepath.Join(sanitizeDiffLine(location, "<ESC>"), "<name>", "SKILL.md")
	}
	if color {
		lines = append(lines, dim+"Skills are loaded from:"+reset)
		for _, location := range paths {
			lines = append(lines, "  "+cyan+location+reset)
		}
	} else {
		lines = append(lines, "Skills are loaded from:")
		for _, location := range paths {
			lines = append(lines, "  "+location)
		}
	}
	return strings.Join(lines, "\n")
}

func selectedSkillNames(skills []prompt.SkillSummary, selected map[int]int) []string {
	names := make([]string, 0, len(selected))
	for i, skill := range skills {
		if selected[i] != skillOff {
			names = append(names, skill.Name)
		}
	}
	return names
}

func alwaysSkillNames(skills []prompt.SkillSummary, selected map[int]int) []string {
	var names []string
	for i, skill := range skills {
		if selected[i] == skillAlways {
			names = append(names, skill.Name)
		}
	}
	return names
}

func skillSelectionValue(names []string) string {
	clean := make([]string, 0, len(names))
	for _, name := range names {
		clean = append(clean, sanitizeDiffLine(name, "<ESC>"))
	}
	if len(clean) == 0 {
		return "none"
	}
	return strings.Join(clean, ", ")
}

func formatSkillSelection(label string, names []string, width int, unicode, color bool, labelColor string) string {
	value := skillSelectionValue(names)
	plain := label + ": none"
	if len(names) > 0 {
		plain = fmt.Sprintf("%s (%d): %s", label, len(names), value)
	}
	if !color {
		return truncateDiffLine(plain, width, unicode)
	}
	styled := labelColor + label + reset
	if len(names) == 0 {
		styled += ": " + dim + "none" + reset
	} else {
		styled += fmt.Sprintf(" (%d): %s%s%s", len(names), cyan, value, reset)
	}
	return truncateDiffLine(styled, width, unicode)
}

func formatSkillSelectionModes(label string, names, always []string, width int, unicode, color bool, labelColor string) string {
	if len(always) == 0 {
		return formatSkillSelection(label, names, width, unicode, color, labelColor)
	}
	lazyCount := len(names) - len(always)
	plain := fmt.Sprintf("%s (lazy %d, always %d): %s", label, lazyCount, len(always), skillSelectionValue(names))
	if !color {
		return truncateDiffLine(plain, width, unicode)
	}
	styled := labelColor + label + reset + fmt.Sprintf(" (lazy %d, always %d): %s%s%s", lazyCount, len(always), cyan, skillSelectionValue(names), reset)
	return truncateDiffLine(styled, width, unicode)
}

func selectSkills(in io.Reader, out io.Writer, skills []prompt.SkillSummary, initial map[int]int, visible, width int, color, unicode bool, src skillContextSource) ([]string, []string, []prompt.SkillSummary, bool, error) {
	if len(skills) == 0 {
		return nil, nil, nil, false, nil
	}
	visible = selectorVisible(len(skills), visible)
	selected := make(map[int]int, len(initial))
	for index, mode := range initial {
		if mode == skillLazy || mode == skillAlways {
			selected[index] = mode
		}
	}
	current := 0
	start := 0
	query := ""
	matches := matchingSkillIndices(skills, query)
	rows := skillSelectorRows(visible)
	renderSkillSelector(out, skills, matches, selected, current, start, visible, width, query, color, unicode, src)
	for {
		key, err := readSelectorKey(in)
		if err != nil {
			clearSelector(out, rows)
			return nil, nil, nil, false, err
		}
		switch key {
		case string([]byte{ctrlC}), "\x1b":
			clearSelector(out, rows)
			return nil, nil, nil, false, nil
		case "\r", "\n":
			clearSelector(out, rows)
			names, alwaysNames, summaries := selectedSkillSummaries(skills, selected)
			return names, alwaysNames, summaries, true, nil
		case " ":
			if len(matches) == 0 {
				continue
			}
			index := matches[current]
			// Three-state cycle: off -> lazy -> always -> off.
			switch selected[index] {
			case skillOff:
				selected[index] = skillLazy
			case skillLazy:
				selected[index] = skillAlways
			default:
				delete(selected, index)
			}
			replaceSelectorRow(out, rows, 0, renderSkillHeader(skills, matches, selected, query, width, color, unicode))
			line := renderSkillLine(skills[index], selected[index], true, width, color, unicode)
			line = renderSkillLineWithScrollbar(line, current-start, start, visible, len(matches), width, unicode)
			replaceSelectorRow(out, rows, 1+current-start, line)
			updateSkillFooter(out, rows, visible, skills, selected, src, width, color, unicode)
			continue
		case arrowUpSequence, arrowDownSequence, selectorPageUp, selectorPageDown:
			if len(matches) == 0 {
				continue
			}
			oldCurrent, oldStart := current, start
			switch key {
			case arrowUpSequence:
				current = (current - 1 + len(matches)) % len(matches)
				start = selectorStart(current, len(matches), visible, start)
			case arrowDownSequence:
				current = (current + 1) % len(matches)
				start = selectorStart(current, len(matches), visible, start)
			case selectorPageUp:
				current, start = selectorPage(current, start, len(matches), visible, -1)
			case selectorPageDown:
				current, start = selectorPage(current, start, len(matches), visible, 1)
			}
			if start != oldStart {
				clearSelector(out, rows)
				renderSkillSelector(out, skills, matches, selected, current, start, visible, width, query, color, unicode, src)
			} else if current != oldCurrent {
				oldIndex, index := matches[oldCurrent], matches[current]
				oldLine := renderSkillLine(skills[oldIndex], selected[oldIndex], false, width, color, unicode)
				oldLine = renderSkillLineWithScrollbar(oldLine, oldCurrent-start, start, visible, len(matches), width, unicode)
				newLine := renderSkillLine(skills[index], selected[index], true, width, color, unicode)
				newLine = renderSkillLineWithScrollbar(newLine, current-start, start, visible, len(matches), width, unicode)
				replaceSelectorRow(out, rows, 1+oldCurrent-start, oldLine)
				replaceSelectorRow(out, rows, 1+current-start, newLine)
			}
			continue
		case string([]byte{8}), string([]byte{127}):
			if query == "" {
				continue
			}
			query = query[:len(query)-1]
			matches = matchingSkillIndices(skills, query)
			current, start = 0, 0
		case string([]byte{ctrlU}):
			query = ""
			matches = matchingSkillIndices(skills, query)
			current, start = 0, 0
		default:
			if len(key) != 1 || key[0] < 32 || key[0] > 126 {
				continue
			}
			query += key
			matches = matchingSkillIndices(skills, query)
			current, start = 0, 0
		}
		clearSelector(out, rows)
		renderSkillSelector(out, skills, matches, selected, current, start, visible, width, query, color, unicode, src)
	}
}

func selectedSkillSummaries(skills []prompt.SkillSummary, selected map[int]int) ([]string, []string, []prompt.SkillSummary) {
	var names []string
	var alwaysNames []string
	var summaries []prompt.SkillSummary
	for i, skill := range skills {
		mode := selected[i]
		if mode == skillOff {
			continue
		}
		copy := skill
		if mode == skillAlways {
			copy.AlwaysLoad = true
			alwaysNames = append(alwaysNames, skill.Name)
		} else {
			copy.AlwaysLoad = false
		}
		names = append(names, skill.Name)
		summaries = append(summaries, copy)
	}
	return names, alwaysNames, summaries
}

func matchingSkillIndices(skills []prompt.SkillSummary, query string) []int {
	query = strings.ToLower(query)
	matches := make([]int, 0, len(skills))
	for i, skill := range skills {
		if strings.Contains(strings.ToLower(skill.Name), query) || strings.Contains(strings.ToLower(skill.Description), query) {
			matches = append(matches, i)
		}
	}
	return matches
}

func renderSkillSelector(out io.Writer, skills []prompt.SkillSummary, matches []int, selected map[int]int, current, start, visible, width int, query string, color, unicode bool, src skillContextSource) {
	printSelectorRow(out, renderSkillHeader(skills, matches, selected, query, width, color, unicode))
	for row := 0; row < visible; row++ {
		matchIndex := start + row
		var line string
		if matchIndex >= len(matches) {
			if row == 0 && len(matches) == 0 {
				line = "  No matching skills"
			} else {
				line = ""
			}
		} else {
			i := matches[matchIndex]
			line = renderSkillLine(skills[i], selected[i], matchIndex == current, width, color, unicode)
		}
		line = renderSkillLineWithScrollbar(line, row, start, visible, len(matches), width, unicode)
		printSelectorRow(out, line)
	}
	sep, bar, legend := renderSkillFooter(selectedSummariesForBreakdown(skills, selected), src, width, color, unicode)
	printSelectorRow(out, sep)
	printSelectorRow(out, bar)
	printSelectorRow(out, legend)
}

// skillSelectorRows counts header + list + dedicated footer region.
func skillSelectorRows(visible int) int { return visible + 1 + skillFooterLines }

func renderSkillFooterForMap(skills []prompt.SkillSummary, selected map[int]int, src skillContextSource, width int, color, unicode bool) (sep, bar, legend string) {
	return renderSkillFooter(selectedSummariesForBreakdown(skills, selected), src, width, color, unicode)
}

func updateSkillFooter(out io.Writer, rows, visible int, skills []prompt.SkillSummary, selected map[int]int, src skillContextSource, width int, color, unicode bool) {
	sep, bar, legend := renderSkillFooterForMap(skills, selected, src, width, color, unicode)
	replaceSelectorRow(out, rows, 1+visible, sep)
	replaceSelectorRow(out, rows, 1+visible+1, bar)
	replaceSelectorRow(out, rows, 1+visible+2, legend)
}

func renderSkillHeader(skills []prompt.SkillSummary, matches []int, selected map[int]int, query string, width int, color, unicode bool) string {
	enabled := skillSelectionValue(selectedSkillNames(skills, selected))
	lazyCount := 0
	alwaysCount := 0
	for _, mode := range selected {
		if mode == skillLazy {
			lazyCount++
		} else if mode == skillAlways {
			alwaysCount++
		}
	}
	// Header stays compact: the stacked total-context bar lives in its own
	// footer region below the list so it never truncates the filter.
	header := fmt.Sprintf("Select skills (%d/%d) | Enabled: %s | Filter: %s | L%d A%d", len(matches), len(skills), enabled, sanitizeDiffLine(query, "<ESC>"), lazyCount, alwaysCount)
	if color {
		prefix := fmt.Sprintf("Select skills (%d/%d) | Enabled: ", len(matches), len(skills))
		header = dim + prefix + reset + cyan + enabled + reset + dim + " | Filter: " + reset + sanitizeDiffLine(query, "<ESC>") + reset + dim + fmt.Sprintf(" | L%d A%d", lazyCount, alwaysCount) + reset
	}
	return selectorHeader(header, width)
}

// skillContextSource carries the total-context inputs that stay fixed while
// the picker runs: model capacity, current remaining %, conversation/other
// tokens, and base system tokens. Selection-dependent must/lazy tokens are
// recomputed from the picker's selected map on every render.
type skillContextSource struct {
	total        int
	known        bool
	remainingPct int
	other        int
	sys          int
}

// skillContextBreakdown is one footer snapshot: prospective usage composed as
// system + must-load + lazy + other + remaining (free). When total is
// unknown there is no free segment; the bar normalizes to the known sum.
type skillContextBreakdown struct {
	sys, must, lazy, other, free, total, used int
	known                                     bool
	remainingPct                              int
}

func skillTokensForBytes(n int) int {
	if n <= 0 {
		return 0
	}
	return (n + 3) / 4
}

func skillSysTokens() int { return skillTokensForBytes(len(prompt.System)) }

func skillMustTokens(skill prompt.SkillSummary) int {
	n := skill.SizeBytes
	if n <= 0 {
		n = prompt.CatalogCharEstimate(skill)
	}
	return skillTokensForBytes(n)
}

func skillLazyTokens(skill prompt.SkillSummary) int {
	return skillTokensForBytes(prompt.CatalogCharEstimate(skill))
}

// skillContextSourceFromRunner snapshots total-context inputs from the active
// runner. Other (conversation/tools/learning) is derived as used - (sys +
// current must + current lazy) so toggling in the picker moves free, not
// other.
func skillContextSourceFromRunner(runner skillRunner, skills []prompt.SkillSummary) skillContextSource {
	sys := skillSysTokens()
	type capacityProvider interface{ ContextCapacity() (int, bool) }
	type remainingProvider interface{ ContextRemaining() (int, bool, bool) }
	type selectedProvider interface{ SelectedSkills() []prompt.SkillSummary }
	capRunner, hasCap := runner.(capacityProvider)
	remRunner, hasRem := runner.(remainingProvider)
	if !hasCap || !hasRem {
		return skillContextSource{sys: sys}
	}
	total, known := capRunner.ContextCapacity()
	remaining, knownRem, _ := remRunner.ContextRemaining()
	if !known || !knownRem || total <= 0 {
		return skillContextSource{sys: sys}
	}
	usedCurrent := total * (100 - remaining) / 100
	var mustCurrent, lazyCurrent int
	if selRunner, ok := runner.(selectedProvider); ok {
		for _, skill := range selRunner.SelectedSkills() {
			if skill.AlwaysLoad {
				mustCurrent += skillMustTokens(skill)
				continue
			}
			lazyCurrent += skillLazyTokens(skill)
		}
		_ = skills
	}
	other := usedCurrent - sys - mustCurrent - lazyCurrent
	if other < 0 {
		other = 0
	}
	if other > total {
		other = total
	}
	return skillContextSource{total: total, known: true, remainingPct: remaining, other: other, sys: sys}
}

func skillBreakdownForSelected(selectedSummaries []prompt.SkillSummary, src skillContextSource) skillContextBreakdown {
	var must, lazy int
	for _, skill := range selectedSummaries {
		if skill.AlwaysLoad {
			must += skillMustTokens(skill)
			continue
		}
		lazy += skillLazyTokens(skill)
	}
	if !src.known {
		sum := src.sys + must + lazy
		return skillContextBreakdown{sys: src.sys, must: must, lazy: lazy, total: sum, used: sum}
	}
	used := src.sys + must + lazy + src.other
	if used < 0 {
		used = 0
	}
	free := src.total - used
	if free < 0 {
		free = 0
	}
	remainingPct := 0
	if src.total > 0 {
		remainingPct = free * 100 / src.total
	}
	return skillContextBreakdown{sys: src.sys, must: must, lazy: lazy, other: src.other, free: free, total: src.total, used: used, known: true, remainingPct: remainingPct}
}

func selectedSummariesForBreakdown(skills []prompt.SkillSummary, selected map[int]int) []prompt.SkillSummary {
	var out []prompt.SkillSummary
	for i, skill := range skills {
		if mode := selected[i]; mode != skillOff {
			copy := skill
			copy.AlwaysLoad = mode == skillAlways
			out = append(out, copy)
		}
	}
	return out
}

// skillFooterLines is the dedicated footer region below the list:
// separator, stacked bar, legend.
const skillFooterLines = 3

func skillSeparatorLine(width int, unicode bool) string {
	if width <= 0 {
		return ""
	}
	if unicode {
		return strings.Repeat("─", width)
	}
	return strings.Repeat("-", width)
}

func skillScrollbarChar(row, start, visible, total int, unicode bool) string {
	if total <= visible || visible <= 0 {
		return ""
	}
	thumbSize := visible * visible / total
	if thumbSize < 1 {
		thumbSize = 1
	}
	if thumbSize > visible {
		thumbSize = visible
	}
	thumbStart := 0
	if total > visible {
		thumbStart = start * (visible - thumbSize) / (total - visible)
	}
	if row >= thumbStart && row < thumbStart+thumbSize {
		if unicode {
			return "┃"
		}
		return "#"
	}
	if unicode {
		return "│"
	}
	return "|"
}

func renderSkillLineWithScrollbar(line string, row, start, visible, total, width int, unicode bool) string {
	bar := skillScrollbarChar(row, start, visible, total, unicode)
	if bar == "" {
		return line
	}
	// Reserve one cell for the scrollbar so list and bar keep own regions.
	trimmed := line
	if width > 1 {
		trimmed = truncateDiffLine(line, width-1, false)
		pad := width - 1 - visibleWidth(trimmed)
		if pad > 0 {
			trimmed += strings.Repeat(" ", pad)
		}
	}
	if !unicode {
		return trimmed + bar
	}
	return trimmed + dim + bar + reset
}

// renderSkillContextBarV2 draws the total-context composition:
// system + must-load + lazy + other + remaining (free). When total is
// unknown the bar normalizes to the known sum with no free segment.
// Unicode uses distinct block elements; ASCII uses distinct letters so the
// legend works without color. Colors distinguish kinds when enabled.
func renderSkillContextBarV2(bd skillContextBreakdown, barWidth int, unicode, color bool) string {
	if barWidth < 4 {
		barWidth = 4
	}
	type seg struct {
		tokens    int
		uch, ach  string
		colorCode string
	}
	segs := []seg{
		{bd.sys, "█", "#", blue},
		{bd.must, "▓", "M", green},
		{bd.lazy, "░", "=", cyan},
	}
	if bd.known {
		segs = append(segs, seg{bd.other, "▒", "~", yellow}, seg{bd.free, "·", "-", dim})
		denom := bd.total
		if denom <= 0 {
			denom = 1
		}
		widths := make([]int, len(segs))
		assigned := 0
		for i, s := range segs {
			if s.tokens <= 0 {
				continue
			}
			w := s.tokens * barWidth / denom
			if w == 0 {
				w = 1
			}
			widths[i] = w
			assigned += w
		}
		for assigned > barWidth {
			reduced := false
			for i := len(widths) - 1; i >= 0 && assigned > barWidth; i-- {
				if widths[i] > 1 {
					widths[i]--
					assigned--
					reduced = true
				}
			}
			if !reduced {
				break
			}
		}
		var b strings.Builder
		b.WriteString("[")
		for i, s := range segs {
			ch := s.uch
			if !unicode {
				ch = s.ach
			}
			part := strings.Repeat(ch, widths[i])
			if color && widths[i] > 0 {
				part = s.colorCode + part + reset
			}
			b.WriteString(part)
		}
		empty := barWidth - assigned
		if empty > 0 {
			if unicode {
				b.WriteString(strings.Repeat(" ", empty))
			} else {
				b.WriteString(strings.Repeat(" ", empty))
			}
		}
		b.WriteString("]")
		usedText := formatSkillTokens(bd.used)
		totalText := formatSkillTokens(bd.total)
		return fmt.Sprintf("%s %s/%s (%d%% left)", b.String(), usedText, totalText, bd.remainingPct)
	}
	sum := bd.sys + bd.must + bd.lazy
	if sum <= 0 {
		sum = 1
	}
	widths := make([]int, len(segs))
	assigned := 0
	for i, s := range segs {
		if s.tokens <= 0 {
			continue
		}
		w := s.tokens * barWidth / sum
		if w == 0 {
			w = 1
		}
		widths[i] = w
		assigned += w
	}
	for assigned > barWidth {
		reduced := false
		for i := len(widths) - 1; i >= 0 && assigned > barWidth; i-- {
			if widths[i] > 1 {
				widths[i]--
				assigned--
				reduced = true
			}
		}
		if !reduced {
			break
		}
	}
	var b strings.Builder
	b.WriteString("[")
	for i, s := range segs {
		ch := s.uch
		if !unicode {
			ch = s.ach
		}
		part := strings.Repeat(ch, widths[i])
		if color && widths[i] > 0 {
			part = s.colorCode + part + reset
		}
		b.WriteString(part)
	}
	empty := barWidth - assigned
	if empty > 0 {
		b.WriteString(strings.Repeat(" ", empty))
	}
	b.WriteString("]")
	return fmt.Sprintf("%s %s (total unknown)", b.String(), formatSkillTokens(sum))
}

func formatSkillTokens(n int) string { return compactSkillCount(n, "tok") }

// renderSkillContextBar keeps the previous two-segment signature for
// existing callers/tests. It renders must+lazy stacked without a total.
func renderSkillContextBar(selected []prompt.SkillSummary, total, barWidth int, unicode bool) string {
	bd := skillBreakdownForSelected(selected, skillContextSource{sys: skillSysTokens()})
	return renderSkillContextBarV2(bd, barWidth, unicode, false)
}

func compactSkillCount(n int, unit string) string {
	if n < 1000 {
		if unit == "" {
			return fmt.Sprintf("%d", n)
		}
		return fmt.Sprintf("%d %s", n, unit)
	}
	if n < 1_000_000 {
		scaled := (n*10 + 500) / 1000
		v := fmt.Sprintf("%d", scaled/10)
		if scaled%10 != 0 {
			v += fmt.Sprintf(".%d", scaled%10)
		}
		if unit == "" {
			return v + "k"
		}
		return v + "k " + unit
	}
	scaled := (n*10 + 500000) / 1000000
	v := fmt.Sprintf("%d", scaled/10)
	if scaled%10 != 0 {
		v += fmt.Sprintf(".%d", scaled%10)
	}
	if unit == "" {
		return v + "M"
	}
	return v + "M " + unit
}

// renderSkillLegend labels every bar color/style so the stacked kinds are
// unambiguous with or without color.
func renderSkillLegend(bd skillContextBreakdown, width int, unicode, color bool) string {
	type item struct {
		label string
		tok   int
		uch   string
		ach   string
		code  string
	}
	items := []item{
		{"sys", bd.sys, "█", "#", blue},
		{"must", bd.must, "▓", "M", green},
		{"lazy", bd.lazy, "░", "=", cyan},
	}
	if bd.known {
		items = append(items, item{"other", bd.other, "▒", "~", yellow}, item{"free", bd.free, "·", "-", dim})
	}
	var b strings.Builder
	for i, it := range items {
		if i > 0 {
			b.WriteString("  ")
		}
		ch := it.uch
		if !unicode {
			ch = it.ach
		}
		sample := ch + ch
		if color {
			sample = it.code + sample + reset
		}
		fmt.Fprintf(&b, "%s %s %s", sample, it.label, formatSkillTokens(it.tok))
	}
	if bd.known {
		fmt.Fprintf(&b, "  total %s", formatSkillTokens(bd.total))
	} else {
		b.WriteString("  total unknown")
	}
	return truncateDiffLine(b.String(), width, unicode)
}

func renderSkillFooter(selected []prompt.SkillSummary, src skillContextSource, width int, color, unicode bool) (sep, bar, legend string) {
	bd := skillBreakdownForSelected(selected, src)
	barWidth := width - 2
	if barWidth < 10 {
		barWidth = 10
	}
	if barWidth > 40 {
		barWidth = 40
	}
	sep = skillSeparatorLine(width, unicode)
	if color && unicode {
		sep = dim + sep + reset
	}
	barLine := renderSkillContextBarV2(bd, barWidth, unicode, color)
	bar = truncateDiffLine("Context "+barLine, width, unicode)
	legend = renderSkillLegend(bd, width, unicode, color)
	return sep, bar, legend
}

func formatSkillBytes(n int) string {
	if n < 1024 {
		return fmt.Sprintf("%dB", n)
	}
	if n < 1024*1024 {
		return fmt.Sprintf("%.1fk", float64(n)/1024)
	}
	return fmt.Sprintf("%.1fM", float64(n)/(1024*1024))
}

func skillModeBox(mode int, unicode bool) string {
	switch mode {
	case skillLazy:
		if unicode {
			return "☑"
		}
		return "[x]"
	case skillAlways:
		if unicode {
			return "★"
		}
		return "[*]"
	default:
		if unicode {
			return "☐"
		}
		return "[ ]"
	}
}

func renderSkillLine(skill prompt.SkillSummary, mode int, current bool, width int, color, unicode bool) string {
	box := skillModeBox(mode, unicode)
	prefix := "  "
	if current {
		prefix = "> "
	}
	name := sanitizeDiffLine(skill.Name, "<ESC>")
	description := sanitizeDiffLine(skill.Description, "<ESC>")
	suffix := ""
	if skill.Autoload {
		if mode == skillAlways {
			suffix += " (auto-always)"
		} else {
			suffix += " (auto)"
		}
	} else if mode == skillAlways {
		suffix += " (always)"
	}
	if skill.SizeBytes > 0 {
		suffix += " " + formatSkillBytes(skill.SizeBytes)
	}
	detail := strings.TrimSpace(description + suffix)
	line := fmt.Sprintf("%s%s %-18s %s", prefix, box, name, detail)
	if width > 0 {
		line = truncateDiffLine(line, width, false)
	}
	if color && current {
		line = cyan + line + reset
	}
	return line
}
