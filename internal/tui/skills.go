package tui

import (
	"fmt"
	"io"
	"strings"

	"qcode/internal/prompt"
)

func (u *UI) chooseSkills() {
	if !u.activeAgentConfigurable() {
		return
	}
	runner, ok := u.runner.(skillRunner)
	if !ok || len(u.skills) == 0 {
		u.printSystemMessage(dim + "No workspace skills are available." + reset)
		return
	}
	u.printSystemMessage(dim + "Use Up/Down to move, Space to toggle, Enter to apply, or Ctrl+C to cancel." + reset)
	u.input.setRaw(true)
	defer u.input.setRaw(false)
	names, summaries, accepted, err := selectSkills(u.input, u.terminal, u.skills, ColorEnabled(u.out))
	if err != nil {
		return
	}
	if !accepted {
		u.printSystemMessage(dim + "Skill selection cancelled." + reset)
		return
	}
	runner.SetSkills(summaries)
	if u.onSkills != nil {
		u.onSkills(names)
	}
	u.printSystemMessage(fmt.Sprintf("%sSkills enabled: %d%s", green, len(names), reset))
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
