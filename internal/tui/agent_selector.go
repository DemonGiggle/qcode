package tui

import (
	"fmt"
	"io"
	"strings"

	"qcode/internal/session"
)

const maxAgentKnowledgeLines = 5

type agentSelectorEntry struct {
	summary   session.Summary
	knowledge string
	active    bool
}

func selectAgent(in io.Reader, out io.Writer, entries []agentSelectorEntry, current string, visible, width int, color bool) (string, bool, error) {
	if len(entries) == 0 {
		return "", false, nil
	}
	selected := 0
	for index, entry := range entries {
		if entry.summary.ID == current {
			selected = index
			break
		}
	}
	visible = selectorVisible(len(entries), visible)
	start := selectorInitialStart(selected, len(entries), visible)
	rows := renderAgentSelector(out, entries, selected, start, visible, width, color)
	for {
		key, err := readSelectorKey(in)
		if err != nil {
			return "", false, err
		}
		switch key {
		case "\r", "\n":
			clearSelector(out, rows)
			return entries[selected].summary.ID, true, nil
		case string([]byte{ctrlC}), "\x1b":
			clearSelector(out, rows)
			return "", false, nil
		case arrowUpSequence, arrowDownSequence, selectorPageUp, selectorPageDown:
			if key == arrowUpSequence {
				selected = (selected - 1 + len(entries)) % len(entries)
			} else if key == arrowDownSequence {
				selected = (selected + 1) % len(entries)
			} else if key == selectorPageUp {
				selected, start = selectorPage(selected, start, len(entries), visible, -1)
			} else {
				selected, start = selectorPage(selected, start, len(entries), visible, 1)
			}
			newStart := selectorStart(selected, len(entries), visible, start)
			if newStart != start {
				start = newStart
			}
			clearSelector(out, rows)
			rows = renderAgentSelector(out, entries, selected, start, visible, width, color)
		}
	}
}

func renderAgentSelector(out io.Writer, entries []agentSelectorEntry, selected, start, visible, width int, color bool) int {
	header := fmt.Sprintf("%s | Select agent (%d/%d) | Up/Down, PgUp/PgDn, Enter to switch", selectorLeaveHint, selected+1, len(entries))
	if width > 0 {
		header = truncateDiffLine(header, width, false)
	}
	fmt.Fprintln(out, header)
	rows := 1
	end := min(len(entries), start+visible)
	for index := start; index < end; index++ {
		block := renderAgentEntry(entries[index], index == selected, width, color)
		for _, line := range block {
			fmt.Fprintln(out, line)
			rows++
		}
	}
	return rows
}

func renderAgentEntry(entry agentSelectorEntry, selected bool, width int, color bool) []string {
	marker := "  "
	if entry.active {
		marker = "* "
	}
	if selected {
		marker = "> "
	}
	status := string(entry.summary.Status)
	if entry.summary.QueueDepth > 0 {
		status += fmt.Sprintf(" (%d queued)", entry.summary.QueueDepth)
	}
	line := fmt.Sprintf("%s%-9s %-18s %-20s %s", marker,
		sanitizeDiffLine(entry.summary.ID, "<ESC>"), sanitizeDiffLine(entry.summary.Name, "<ESC>"),
		sanitizeDiffLine(entry.summary.Model, "<ESC>"), sanitizeDiffLine(status, "<ESC>"))
	if width > 0 {
		line = truncateDiffLine(line, width, false)
	}
	if color && selected {
		line = cyan + bold + line + reset
	}
	lines := []string{line}
	knowledge := agentKnowledgeLines(entry.knowledge, width)
	for index, value := range knowledge {
		prefix := "    "
		if index == 0 {
			prefix = "    knowledge: "
		}
		line := prefix + value
		if width > 0 {
			line = truncateDiffLine(line, width, false)
		}
		lines = append(lines, line)
	}
	return lines
}

func agentKnowledgeLines(knowledge string, width int) []string {
	knowledge = strings.ReplaceAll(knowledge, "\r\n", "\n")
	knowledge = strings.ReplaceAll(knowledge, "\r", "\n")
	parts := strings.Split(knowledge, "\n")
	if strings.TrimSpace(knowledge) == "" {
		parts = []string{"none recorded"}
	}
	if len(parts) > maxAgentKnowledgeLines {
		parts = parts[:maxAgentKnowledgeLines]
		parts[maxAgentKnowledgeLines-1] = "..."
	}
	for index, part := range parts {
		part = strings.TrimSpace(sanitizeDiffLine(part, "<ESC>"))
		if part == "" {
			part = " "
		}
		prefixWidth := len("    knowledge: ")
		if index > 0 {
			prefixWidth = len("    ")
		}
		if width > prefixWidth {
			part = truncateDiffLine(part, width-prefixWidth, false)
		}
		parts[index] = part
	}
	return parts
}
