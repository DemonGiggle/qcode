package tui

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"qcode/internal/prompt"
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
	initial := make(map[int]bool)
	if selectedRunner, ok := runner.(selectedSkillsRunner); ok {
		selected := make(map[string]bool)
		for _, skill := range selectedRunner.SelectedSkills() {
			selected[skill.Name] = true
		}
		for i, skill := range u.skills {
			initial[i] = selected[skill.Name]
		}
	}
	u.printSystemMessage(dim + "Type to filter. Use Up/Down or PgUp/PgDn to move, Space to toggle, Enter to apply, or Ctrl+C to cancel." + reset)
	u.input.setRaw(true)
	u.beginRawSelector()
	defer func() {
		u.input.setRaw(false)
		u.endRawSelector()
	}()
	visible := min(12, max(3, u.height-6))
	names, summaries, accepted, err := selectSkills(u.input, u.terminal, u.skills, initial, visible, u.width, ColorEnabled(u.out))
	if err != nil {
		return
	}
	if !accepted {
		u.printSystemMessage(dim + "Skill selection cancelled." + reset)
		return
	}
	if u.onSkills != nil {
		u.onSkills(names)
	}
	runner.SetSkills(summaries)
	u.printSystemMessage(fmt.Sprintf("%sSkills enabled: %d%s", green, len(names), reset))
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

func selectSkills(in io.Reader, out io.Writer, skills []prompt.SkillSummary, initial map[int]bool, visible, width int, color bool) ([]string, []prompt.SkillSummary, bool, error) {
	if len(skills) == 0 {
		return nil, nil, false, nil
	}
	visible = selectorVisible(len(skills), visible)
	selected := make(map[int]bool, len(initial))
	for index, enabled := range initial {
		if enabled {
			selected[index] = true
		}
	}
	current := 0
	start := 0
	query := ""
	matches := matchingSkillIndices(skills, query)
	rows := visible + 1
	renderSkillSelector(out, skills, matches, selected, current, start, visible, width, query, color)
	for {
		key, err := readSelectorKey(in)
		if err != nil {
			clearSelector(out, rows)
			return nil, nil, false, err
		}
		switch key {
		case string([]byte{ctrlC}):
			clearSelector(out, rows)
			return nil, nil, false, nil
		case "\r", "\n":
			clearSelector(out, rows)
			names := make([]string, 0, len(selected))
			summaries := make([]prompt.SkillSummary, 0, len(selected))
			for i, skill := range skills {
				if selected[i] {
					names = append(names, skill.Name)
					summaries = append(summaries, skill)
				}
			}
			return names, summaries, true, nil
		case " ":
			if len(matches) == 0 {
				continue
			}
			index := matches[current]
			selected[index] = !selected[index]
			replaceSelectorRow(out, rows, 1+current-start, renderSkillLine(skills[index], selected[index], true, width, color))
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
				renderSkillSelector(out, skills, matches, selected, current, start, visible, width, query, color)
			} else if current != oldCurrent {
				oldIndex, index := matches[oldCurrent], matches[current]
				replaceSelectorRow(out, rows, 1+oldCurrent-start, renderSkillLine(skills[oldIndex], selected[oldIndex], false, width, color))
				replaceSelectorRow(out, rows, 1+current-start, renderSkillLine(skills[index], selected[index], true, width, color))
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
		renderSkillSelector(out, skills, matches, selected, current, start, visible, width, query, color)
	}
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

func renderSkillSelector(out io.Writer, skills []prompt.SkillSummary, matches []int, selected map[int]bool, current, start, visible, width int, query string, color bool) {
	header := fmt.Sprintf("Select skills (%d/%d) | Filter: %s", len(matches), len(skills), query)
	if width > 0 {
		header = truncateDiffLine(header, width, false)
	}
	fmt.Fprintln(out, header)
	for row := 0; row < visible; row++ {
		matchIndex := start + row
		if matchIndex >= len(matches) {
			if row == 0 && len(matches) == 0 {
				fmt.Fprintln(out, "  No matching skills")
			} else {
				fmt.Fprintln(out)
			}
			continue
		}
		i := matches[matchIndex]
		fmt.Fprintln(out, renderSkillLine(skills[i], selected[i], matchIndex == current, width, color))
	}
}

func renderSkillLine(skill prompt.SkillSummary, selected, current bool, width int, color bool) string {
	box := "[ ]"
	if selected {
		box = "[x]"
	}
	prefix := "  "
	if current {
		prefix = "> "
	}
	name := sanitizeDiffLine(skill.Name, "<ESC>")
	description := sanitizeDiffLine(skill.Description, "<ESC>")
	line := fmt.Sprintf("%s%s %-18s %s", prefix, box, name, description)
	if width > 0 {
		line = truncateDiffLine(line, width, false)
	}
	if color && current {
		line = cyan + line + reset
	}
	return line
}
