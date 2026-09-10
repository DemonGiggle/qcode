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
	u.printSystemMessage(dim + "Type to filter. Use Up/Down or PgUp/PgDn to move, Space to toggle, Enter to apply, or Ctrl+C to cancel." + reset)
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
	query := ""
	matches := matchingToolIndices(statuses, query)
	rows := visible + 1
	renderToolSelector(out, statuses, matches, current, start, visible, width, query, color)
	for {
		key, err := readSelectorKey(in)
		if err != nil {
			clearSelector(out, rows)
			return false, err
		}
		switch key {
		case string([]byte{ctrlC}):
			clearSelector(out, rows)
			return false, nil
		case "\r", "\n":
			clearSelector(out, rows)
			for _, status := range statuses {
				runner.ToggleTool(status.name, status.enabled)
			}
			return true, nil
		case " ":
			if len(matches) == 0 {
				continue
			}
			index := matches[current]
			statuses[index].enabled = !statuses[index].enabled
			replaceSelectorRow(out, rows, 1+current-start, renderToolLine(statuses[index], true, width, color))
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
				renderToolSelector(out, statuses, matches, current, start, visible, width, query, color)
			} else if current != oldCurrent {
				oldIndex, index := matches[oldCurrent], matches[current]
				replaceSelectorRow(out, rows, 1+oldCurrent-start, renderToolLine(statuses[oldIndex], false, width, color))
				replaceSelectorRow(out, rows, 1+current-start, renderToolLine(statuses[index], true, width, color))
			}
			continue
		case string([]byte{8}), string([]byte{127}):
			if query == "" {
				continue
			}
			query = query[:len(query)-1]
			matches = matchingToolIndices(statuses, query)
			current, start = 0, 0
		case string([]byte{ctrlU}):
			query = ""
			matches = matchingToolIndices(statuses, query)
			current, start = 0, 0
		default:
			if len(key) != 1 || key[0] < 32 || key[0] > 126 {
				continue
			}
			query += key
			matches = matchingToolIndices(statuses, query)
			current, start = 0, 0
		}
		clearSelector(out, rows)
		renderToolSelector(out, statuses, matches, current, start, visible, width, query, color)
	}
}

func matchingToolIndices(statuses []toolStatus, query string) []int {
	query = strings.ToLower(query)
	matches := make([]int, 0, len(statuses))
	for i, tool := range statuses {
		if strings.Contains(strings.ToLower(tool.name), query) {
			matches = append(matches, i)
		}
	}
	return matches
}

func renderToolSelector(out io.Writer, statuses []toolStatus, matches []int, current, start, visible, width int, query string, color bool) {
	header := fmt.Sprintf("%s | Select tools (%d/%d) | Filter: %s", selectorLeaveHint, len(matches), len(statuses), query)
	if width > 0 {
		header = truncateDiffLine(header, width, false)
	}
	fmt.Fprintln(out, header)
	for row := 0; row < visible; row++ {
		matchIndex := start + row
		if matchIndex >= len(matches) {
			if row == 0 && len(matches) == 0 {
				fmt.Fprintln(out, "  No matching tools")
			} else {
				fmt.Fprintln(out)
			}
			continue
		}
		i := matches[matchIndex]
		fmt.Fprintln(out, renderToolLine(statuses[i], matchIndex == current, width, color))
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
