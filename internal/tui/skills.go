package tui

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"qcode/internal/prompt"
)

// SkillInfo contains the safe display fields for a discovered skill. The
// location is kept separate from prompt.SkillSummary so it never enters the
// model's system prompt.
type SkillInfo struct {
	Name        string
	Description string
	Path        string
}

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
	u.printSystemMessage(dim + "Use Up/Down or PgUp/PgDn to move, Space to toggle, Enter to apply, or Ctrl+C to cancel." + reset)
	u.input.setRaw(true)
	u.beginRawSelector()
	defer func() {
		u.input.setRaw(false)
		u.endRawSelector()
	}()
	visible := min(12, max(3, u.height-6))
	names, summaries, accepted, err := selectSkills(u.input, u.terminal, u.skills, visible, u.width, ColorEnabled(u.out))
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

func (u *UI) listSkills() {
	if !u.ensureSkillCatalog() {
		return
	}
	color := u.out != nil && ColorEnabled(u.out)
	if len(u.skillInfos) == 0 {
		u.printSystemMessage(u.skillLocationHint(color))
		u.printSystemMessage(dim + "No skills were found." + reset)
		return
	}
	lines := []string{"Skills found:"}
	for _, skill := range u.skillInfos {
		name := sanitizeDiffLine(skill.Name, "<ESC>")
		description := sanitizeDiffLine(skill.Description, "<ESC>")
		path := sanitizeDiffLine(skill.Path, "<ESC>")
		if color {
			lines = append(lines, "  "+cyan+name+reset+"  "+description)
		} else {
			lines = append(lines, "  "+name+"  "+description)
		}
		if skill.Path != "" {
			if color {
				lines = append(lines, "    "+dim+path+reset)
			} else {
				lines = append(lines, "    "+path)
			}
		}
	}
	u.printSystemMessage(strings.Join(lines, "\n"))
}

func selectSkills(in io.Reader, out io.Writer, skills []prompt.SkillSummary, visible, width int, color bool) ([]string, []prompt.SkillSummary, bool, error) {
	if len(skills) == 0 {
		return nil, nil, false, nil
	}
	visible = selectorVisible(len(skills), visible)
	selected := make(map[int]bool)
	current := 0
	start := 0
	renderSkillSelector(out, skills, selected, current, start, visible, width, color)
	for {
		key, err := readSelectorKey(in)
		if err != nil {
			clearSelector(out, visible)
			return nil, nil, false, err
		}
		switch key {
		case string([]byte{ctrlC}):
			clearSelector(out, visible)
			return nil, nil, false, nil
		case "\r", "\n":
			clearSelector(out, visible)
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
			selected[current] = !selected[current]
			replaceSelectorRow(out, visible, current-start, renderSkillLine(skills[current], selected[current], true, width, color))
			continue
		case arrowUpSequence, arrowDownSequence, selectorPageUp, selectorPageDown:
			oldCurrent, oldStart := current, start
			switch key {
			case arrowUpSequence:
				current = (current - 1 + len(skills)) % len(skills)
				start = selectorStart(current, len(skills), visible, start)
			case arrowDownSequence:
				current = (current + 1) % len(skills)
				start = selectorStart(current, len(skills), visible, start)
			case selectorPageUp:
				current, start = selectorPage(current, start, len(skills), visible, -1)
			case selectorPageDown:
				current, start = selectorPage(current, start, len(skills), visible, 1)
			}
			if start != oldStart {
				clearSelector(out, visible)
				renderSkillSelector(out, skills, selected, current, start, visible, width, color)
			} else if current != oldCurrent {
				replaceSelectorRow(out, visible, oldCurrent-start, renderSkillLine(skills[oldCurrent], selected[oldCurrent], false, width, color))
				replaceSelectorRow(out, visible, current-start, renderSkillLine(skills[current], selected[current], true, width, color))
			}
		}
	}
}

func renderSkillSelector(out io.Writer, skills []prompt.SkillSummary, selected map[int]bool, current, start, visible, width int, color bool) {
	for row := 0; row < visible; row++ {
		i := start + row
		line := ""
		if i < len(skills) {
			line = renderSkillLine(skills[i], selected[i], i == current, width, color)
		}
		fmt.Fprintln(out, line)
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
