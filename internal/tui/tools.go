package tui

import (
	"fmt"
	"io"
	"strings"
)

// toolStatus describes a single tool's enabled/disabled state for display.
type toolStatus struct {
	name    string
	enabled bool
}

func (u *UI) chooseTools() {
	runner, ok := u.runner.(toolRunner)
	if !ok {
		u.printSystemMessage(dim + "Tool selection is unavailable." + reset)
		return
	}
	names := runner.ToolNames()
	if len(names) == 0 {
		u.printSystemMessage(dim + "No tools are available." + reset)
		return
	}
	u.printSystemMessage(dim + "Use Up/Down to move, Space to toggle, Enter to apply, or Ctrl+C to cancel." + reset)
	u.input.setRaw(true)
	defer u.input.setRaw(false)
	accepted, err := selectTools(u.input, u.terminal, names, runner, ColorEnabled(u.out))
	if err != nil {
		return
	}
	if !accepted {
		u.printSystemMessage(dim + "Tool selection cancelled." + reset)
		return
	}
	// Count enabled tools.
	enabled := 0
	for _, name := range names {
		if runner.ToolEnabled(name) {
			enabled++
		}
	}
	u.printSystemMessage(fmt.Sprintf("%sTools enabled: %d of %d%s", green, enabled, len(names), reset))
}

func selectTools(in io.Reader, out interface{ Write([]byte) (int, error) }, names []string, runner toolRunner, color bool) (bool, error) {
	current := 0
	for {
		statuses := make([]toolStatus, len(names))
		for i, name := range names {
			statuses[i] = toolStatus{name: name, enabled: runner.ToolEnabled(name)}
		}
		renderToolSelector(out, statuses, current, color)
		key, err := readToolByte(in)
		clearToolSelector(out, len(statuses))
		if err != nil {
			return false, err
		}
		switch key {
		case ctrlC:
			return false, nil
		case '\r', '\n':
			return true, nil
		case ' ':
			name := names[current]
			runner.ToggleTool(name, !runner.ToolEnabled(name))
		case 0x1b:
			var tail [2]byte
			if _, err := io.ReadFull(in, tail[:]); err == nil && tail[0] == '[' {
				if tail[1] == 'A' {
					current = (current - 1 + len(names)) % len(names)
				}
				if tail[1] == 'B' {
					current = (current + 1) % len(names)
				}
			}
		}
	}
}

func readToolByte(input io.Reader) (byte, error) {
	var buffer [1]byte
	_, err := io.ReadFull(input, buffer[:])
	return buffer[0], err
}

func renderToolSelector(out interface{ Write([]byte) (int, error) }, statuses []toolStatus, current int, color bool) {
	for i, tool := range statuses {
		box := "[x]"
		if !tool.enabled {
			box = "[ ]"
		}
		prefix := "  "
		if i == current {
			prefix = "> "
		}
		line := fmt.Sprintf("%s%s %-18s%s", prefix, box, tool.name, reset)
		if color && i == current {
			line = cyan + line + reset
		}
		fmt.Fprintln(out, strings.TrimSpace(line))
	}
}

func clearToolSelector(out interface{ Write([]byte) (int, error) }, rows int) {
	for range rows {
		fmt.Fprint(out, "\x1b[1A\r\x1b[2K")
	}
}
