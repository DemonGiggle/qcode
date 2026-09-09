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
	u.printSystemMessage(dim + "Use Up/Down to move, Space to toggle, Enter to apply, or Ctrl+C to cancel." + reset)
	u.input.setRaw(true)
	u.beginRawSelector()
	defer func() {
		u.input.setRaw(false)
		u.endRawSelector()
	}()
	names, summaries, accepted, err := selectSkills(u.input, u.terminal, u.skills, ColorEnabled(u.out))
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

func selectSkills(in io.Reader, out interface{ Write([]byte) (int, error) }, skills []prompt.SkillSummary, color bool) ([]string, []prompt.SkillSummary, bool, error) {
	selected := make(map[int]bool)
	current := 0
	for {
		renderSkillSelector(out, skills, selected, current, color)
		key, err := readSkillByte(in)
		clearSkillSelector(out, len(skills))
		if err != nil {
			return nil, nil, false, err
		}
		switch key {
		case ctrlC:
			return nil, nil, false, nil
		case '\r', '\n':
			names := make([]string, 0, len(selected))
			summaries := make([]prompt.SkillSummary, 0, len(selected))
			for i, skill := range skills {
				if selected[i] {
					names = append(names, skill.Name)
					summaries = append(summaries, skill)
				}
			}
			return names, summaries, true, nil
		case ' ':
			selected[current] = !selected[current]
		case 0x1b:
			var tail [2]byte
			if _, err := io.ReadFull(in, tail[:]); err == nil && tail[0] == '[' {
				if tail[1] == 'A' {
					current = (current - 1 + len(skills)) % len(skills)
				}
				if tail[1] == 'B' {
					current = (current + 1) % len(skills)
				}
			}
		}
	}
}

func readSkillByte(input io.Reader) (byte, error) {
	var buffer [1]byte
	_, err := io.ReadFull(input, buffer[:])
	return buffer[0], err
}

func renderSkillSelector(out interface{ Write([]byte) (int, error) }, skills []prompt.SkillSummary, selected map[int]bool, current int, color bool) {
	for i, skill := range skills {
		box := "[ ]"
		if selected[i] {
			box = "[x]"
		}
		prefix := "  "
		if i == current {
			prefix = "> "
		}
		line := fmt.Sprintf("%s%s %-18s %s", prefix, box, skill.Name, skill.Description)
		if color && i == current {
			line = cyan + line + reset
		}
		fmt.Fprintln(out, strings.TrimSpace(line))
	}
}
func clearSkillSelector(out interface{ Write([]byte) (int, error) }, rows int) {
	for range rows {
		// The selector leaves the cursor on the line after its final item.
		// Move up and erase one row at a time, ending at the first row so the
		// next render replaces the selector in place. Moving back down between
		// rows can scroll the terminal when the selector reaches the bottom.
		fmt.Fprint(out, "\x1b[1A\r\x1b[2K")
	}
}
