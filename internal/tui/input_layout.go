package tui

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
	"qcode/internal/session"
)

// renderInput receives committed editor state after every edit, including
// deletion, paste and history navigation. The editor lock is released first.
func (u *UI) renderInput(prompt, line string, pos int) {
	u.screenMu.Lock()
	defer u.screenMu.Unlock()
	u.inputLabel, u.inputText, u.inputPosition = prompt, line, pos
	u.tabMu.Lock()
	pending := u.pendingTab != 0
	u.tabMu.Unlock()
	if !pending && prompt == inputPrompt {
		u.drafts[u.activeAgent] = line
	}
	u.paintFixedLocked(0)
}

func queuePrompt(status session.Status) string {
	if status == session.StatusRunning || status == session.StatusWaitingForApproval {
		return "(Queue)> "
	}
	return "> "
}

// inputRows wraps by display cells and retains the logical cursor location.
func inputRows(text string, cursor, width int) ([]string, int, int) {
	width = max(1, width)
	rows := []string{""}
	x, cy, cx := 0, 0, 0
	for i, r := range []rune(text) {
		w := runewidth.RuneWidth(r)
		if w > width {
			r, w = '?', 1
		}
		if x+w > width {
			rows = append(rows, "")
			x = 0
		}
		if i == cursor {
			cy, cx = len(rows)-1, x
		}
		rows[len(rows)-1] += string(r)
		x += w
		if x >= width {
			rows = append(rows, "")
			x = 0
		}
	}
	if cursor >= utf8.RuneCountInString(text) {
		cy, cx = len(rows)-1, x
	}
	return rows, cy, cx
}

// paintFixedLocked owns the entire screen, including the cursor. No editor
// escape sequences or candidate text are passed through conversation history.
func (u *UI) paintFixedLocked(direction int) {
	if u.input != nil {
		u.input.mu.Lock()
		raw := u.input.raw
		u.input.mu.Unlock()
		if raw {
			u.inputFrame = ""
			return
		}
	}
	if u.out == nil || u.height < 1 || u.width < 1 {
		return
	}
	label := plainHistoryText(u.inputLabel)
	var summary session.Summary
	if u.manager != nil {
		summary, _ = u.manager.Summary(u.activeAgent)
	}
	if u.inputLabel == inputPrompt {
		label = queuePrompt(summary.Status)
	}
	rows, cy, cx := inputRows(label+u.inputText, utf8.RuneCountInString(label)+u.inputPosition, u.width)
	// Keep a cursor-centered window for drafts taller than the terminal.
	inputHeight := min(len(rows), max(1, u.height-5))
	start := max(0, cy-inputHeight+1)
	rows = rows[start:min(len(rows), start+inputHeight)]
	cy -= start
	footer := min(2, max(0, u.height-2))
	promptRow := u.height - footer - len(rows) + 1
	matches := matchingSlashCommands(u.inputText)
	if u.inputLabel != inputPrompt {
		matches = nil
	}
	count := min(5, len(matches), max(0, promptRow-3))
	outputHeight := max(0, promptRow-count-2)
	var b strings.Builder
	b.WriteString("\x1b[?25l\x1b[0m\x1b[r")
	// Clear rows explicitly: erase-to-end would erase the other regions.
	for row := 1; row <= u.height; row++ {
		fmt.Fprintf(&b, "\x1b[%d;1H\x1b[2K", row)
	}
	if u.manager != nil && u.height > 3 {
		fmt.Fprintf(&b, "\x1b[1;1H%s", tabBar(u.manager.List(), u.activeAgent, u.views, u.width, u.unicode, ColorEnabled(u.out)))
	}
	if outputHeight > 0 && u.display != nil {
		page := u.activeViewportLocked().page(historyRows(u.display.Snapshot(), u.width), outputHeight, direction)
		for i, row := range page {
			fmt.Fprintf(&b, "\x1b[%d;1H%s\x1b[0m", i+2, row.text)
		}
	}
	for i := 0; i < count; i++ {
		text := fmt.Sprintf("  %-8s %s", matches[i].name, matches[i].description)
		if ColorEnabled(u.out) {
			text = fmt.Sprintf("  %s%-8s%s %s%s%s", cyan, matches[i].name, reset, dim, matches[i].description, reset)
		}
		if i == count-1 && len(matches) > count {
			text += " (type to filter)"
		}
		fmt.Fprintf(&b, "\x1b[%d;1H%s", promptRow-count+i, truncateDiffLine(text, u.width, u.unicode))
	}
	for i, row := range rows {
		fmt.Fprintf(&b, "\x1b[%d;1H%s", promptRow+i, row)
	}
	if footer == 2 {
		message := taskIndicatorMessage(summary.Status, summary.QueueDepth, u.unicode, time.Now())
		if u.activeViewportLocked().browsing {
			message = "History paused | PgUp/PgDn | PgDn to bottom resumes"
		}
		fmt.Fprintf(&b, "\x1b[%d;1H%s\x1b[0m", u.height-1, truncateDiffLine(message, u.width, u.unicode))
	}
	if footer > 0 {
		fmt.Fprintf(&b, "\x1b[%d;1H%s\x1b[0m", u.height, statusBar(u.provider, u.model, displayRoot(u.root), u.width, u.unicode, ColorEnabled(u.out), u.contextLabel(), u.usageLabel()))
	}
	fmt.Fprintf(&b, "\x1b[%d;%dH\x1b[?25h", promptRow+cy, cx+1)
	frame := b.String()
	if frame != u.inputFrame {
		if _, err := u.out.WriteString(frame); err == nil {
			u.inputFrame = frame
		}
	}
}
