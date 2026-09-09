package tui

import (
	"fmt"
	"io"
)

// toolStatus describes a single tool's enabled/disabled state for display.
type toolStatus struct {
	name    string
	enabled bool
}

func (u *UI) chooseTools() {
	if !u.activeAgentConfigurable() {
		return
	}
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
	u.printSystemMessage(dim + "Use Up/Down or PgUp/PgDn to move, Space to toggle, Enter to apply, or Ctrl+C to cancel." + reset)
	u.input.setRaw(true)
	u.beginRawSelector()
	defer func() {
		u.input.setRaw(false)
		u.endRawSelector()
	}()
	visible := min(12, max(3, u.height-6))
	accepted, err := selectTools(u.input, u.terminal, names, runner, visible, u.width, ColorEnabled(u.out))
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

func selectTools(in io.Reader, out io.Writer, names []string, runner toolRunner, visible, width int, color bool) (bool, error) {
	if len(names) == 0 {
		return false, nil
	}
	visible = selectorVisible(len(names), visible)
	current := 0
	start := 0
	statuses := make([]toolStatus, len(names))
	for i, name := range names {
		statuses[i] = toolStatus{name: name, enabled: runner.ToolEnabled(name)}
	}
	renderToolSelector(out, statuses, current, start, visible, width, color)
	for {
		key, err := readSelectorKey(in)
		if err != nil {
			clearSelector(out, visible)
			return false, err
		}
		switch key {
		case string([]byte{ctrlC}):
			clearSelector(out, visible)
			return false, nil
		case "\r", "\n":
			clearSelector(out, visible)
			for _, status := range statuses {
				runner.ToggleTool(status.name, status.enabled)
			}
			return true, nil
		case " ":
			statuses[current].enabled = !statuses[current].enabled
			replaceSelectorRow(out, visible, current-start, renderToolLine(statuses[current], true, width, color))
			continue
		case arrowUpSequence, arrowDownSequence, selectorPageUp, selectorPageDown:
			oldCurrent, oldStart := current, start
			switch key {
			case arrowUpSequence:
				current = (current - 1 + len(names)) % len(names)
				start = selectorStart(current, len(names), visible, start)
			case arrowDownSequence:
				current = (current + 1) % len(names)
				start = selectorStart(current, len(names), visible, start)
			case selectorPageUp:
				current, start = selectorPage(current, start, len(names), visible, -1)
			case selectorPageDown:
				current, start = selectorPage(current, start, len(names), visible, 1)
			}
			if start != oldStart {
				clearSelector(out, visible)
				renderToolSelector(out, statuses, current, start, visible, width, color)
			} else if current != oldCurrent {
				replaceSelectorRow(out, visible, oldCurrent-start, renderToolLine(statuses[oldCurrent], false, width, color))
				replaceSelectorRow(out, visible, current-start, renderToolLine(statuses[current], true, width, color))
			}
		}
	}
}

func renderToolSelector(out io.Writer, statuses []toolStatus, current, start, visible, width int, color bool) {
	for row := 0; row < visible; row++ {
		i := start + row
		line := ""
		if i < len(statuses) {
			line = renderToolLine(statuses[i], i == current, width, color)
		}
		fmt.Fprintln(out, line)
	}
}

func renderToolLine(tool toolStatus, current bool, width int, color bool) string {
	box := "[x]"
	if !tool.enabled {
		box = "[ ]"
	}
	prefix := "  "
	if current {
		prefix = "> "
	}
	line := fmt.Sprintf("%s%s %s", prefix, box, sanitizeDiffLine(tool.name, "<ESC>"))
	if width > 0 {
		line = truncateDiffLine(line, width, false)
	}
	if color && current {
		line = cyan + line + reset
	}
	return line
}
