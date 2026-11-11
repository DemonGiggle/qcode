package tui

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"qcode/internal/session"
	"qcode/internal/termtext"
	qtheme "qcode/internal/theme"
)

// renderInput receives committed editor state after every edit, including
// deletion, paste and history navigation. The editor lock is released first.
func (u *UI) renderInput(prompt, line string, pos int) {
	display, displayPos := line, pos
	if u.terminal != nil {
		display, displayPos = u.terminal.DisplayInput(line, pos)
	}
	u.screenMu.Lock()
	defer u.screenMu.Unlock()
	u.inputLabel, u.inputText, u.inputPosition = prompt, line, pos
	u.inputDisplay, u.inputDisplayPosition = display, displayPos
	u.tabMu.Lock()
	pending := u.pendingTab != 0
	u.tabMu.Unlock()
	if !pending && (prompt == inputPrompt || prompt == planInputPrompt || prompt == skillPlanInputPrompt) {
		if u.drafts[u.activeAgent] != line {
			u.drafts[u.activeAgent] = line
			u.signalPresentation()
		}
	}
	u.paintFixedLocked(0)
}

func queuePrompt(status session.Status) string {
	if status == session.StatusRunning || status == session.StatusWaitingForApproval {
		return "(Steer)> "
	}
	return "> "
}

// inputUnit is one display unit of the composer draft: the cells it paints
// plus the rune range it covers in the source text. raw may differ from the
// source when a cluster is wider than the row it lands on.
type inputUnit struct {
	raw   string
	width int
	start int
	end   int
	space bool
}

// inputRow is one rendered composer row. start and end delimit the source
// rune range the row covers, including break and newline characters that are
// never painted, so a logical cursor index maps back to exactly one row.
type inputRow struct {
	units []inputUnit
	start int
	end   int
	width int
}

// inputRows word-wraps the composer draft by display cells. Row 0 carries the
// prompt label and spans the full width; every later row starts with an
// indent gutter aligned under the label, so a wrapped draft reads as one
// block beneath the prompt. A soft break happens after the last space so
// whole words move to the next row together; only a word wider than the
// gutter hard-splits, and no unit is ever dropped: concatenating the rows
// without the indent gutter reproduces text exactly (newlines included). The
// cursor is a rune index into text and maps to the row covering that index
// plus its screen column; a cursor that reaches a full row's last cell moves
// to column 0 of the next row instead of the right margin.
func inputRows(text string, cursor, width, indent int) ([]string, int, int) {
	width = max(1, width)
	indent = max(0, min(indent, width-1))
	gutter := width - indent // content cells on continuation rows
	rows := []inputRow{{start: 0}}
	capacity := func() int {
		if len(rows) == 1 {
			return width
		}
		return gutter
	}
	i := 0
	for _, unit := range termtext.Units(text) {
		w, raw := unit.Width, unit.Text
		count := utf8.RuneCountInString(raw)
		start := i
		i += count
		if strings.ContainsAny(raw, "\r\n") {
			// A hard break ends the current row, unless wrapping already
			// left us at the start of an empty row. The break character is
			// not painted but stays inside the row it terminates so the
			// cursor keeps its logical position.
			if last := len(rows) - 1; len(rows[last].units) > 0 || rows[last].width > 0 {
				rows[last].end = i
				rows = append(rows, inputRow{start: i, end: i})
			} else {
				rows[last].end = i
			}
			continue
		}
		row := &rows[len(rows)-1]
		// A cluster wider than an empty row (possible only on very narrow
		// gutters) paints as "?" instead of overflowing its cells.
		if row.width == 0 && w > capacity() {
			raw, w = "?", 1
		}
		if row.width+w > capacity() {
			// Break after the last space when the word tail fits a fresh
			// continuation row; otherwise split in place so text is kept
			// whole instead of leaving a hole or losing characters.
			split, moved := -1, 0
			if s := lastInputBreak(row.units); s >= 0 {
				for _, u := range row.units[s+1:] {
					moved += u.width
				}
				if moved+w <= gutter {
					split = s
				}
			}
			if split >= 0 {
				tail := append([]inputUnit(nil), row.units[split+1:]...)
				boundary := start
				if len(tail) > 0 {
					boundary = tail[0].start
				}
				row.units = row.units[:split+1]
				row.width -= moved
				row.end = boundary
				rows = append(rows, inputRow{start: boundary, end: i, units: tail, width: moved})
			} else {
				row.end = start
				rows = append(rows, inputRow{start: start, end: i})
			}
			row = &rows[len(rows)-1]
			if row.width == 0 && w > capacity() {
				raw, w = "?", 1
			}
		}
		row.units = append(row.units, inputUnit{raw: raw, width: w, start: start, end: i, space: raw == " "})
		row.width += w
		rows[len(rows)-1].end = i
	}

	// Map the logical cursor to the row covering its rune index and to a
	// screen column that includes the indent gutter of continuation rows.
	total := utf8.RuneCountInString(text)
	rowIndex, col := 0, 0
	switch {
	case cursor <= 0:
	case cursor >= total:
		rowIndex = len(rows) - 1
		col = rows[rowIndex].width
	default:
		for rowIndex < len(rows)-1 && cursor >= rows[rowIndex].end {
			rowIndex++
		}
		for _, u := range rows[rowIndex].units {
			if u.end <= cursor {
				col += u.width
			}
		}
	}
	rowWidth := width
	if rowIndex > 0 {
		rowWidth = gutter
	}
	if col >= rowWidth {
		rowIndex++
		col = 0
		if rowIndex == len(rows) {
			rows = append(rows, inputRow{start: total, end: total})
		}
	}

	out := make([]string, len(rows))
	for index, r := range rows {
		var b strings.Builder
		if index > 0 {
			b.WriteString(strings.Repeat(" ", indent))
		}
		for _, u := range r.units {
			b.WriteString(u.raw)
		}
		out[index] = b.String()
	}
	if rowIndex > 0 {
		col += indent
	}
	return out, rowIndex, col
}

// lastInputBreak finds the last space with visible content before it: the
// point where a soft break keeps whole words on the row they started on.
func lastInputBreak(units []inputUnit) int {
	for i := len(units) - 1; i >= 0; i-- {
		if !units[i].space {
			continue
		}
		before := 0
		for _, u := range units[:i] {
			before += u.width
		}
		if before > 0 {
			return i
		}
	}
	return -1
}

// paintFixedLocked owns the entire screen, including the cursor. No editor
// escape sequences or candidate text are passed through conversation history.
func (u *UI) paintFixedLocked(direction int) {
	if u.commandViewActive {
		return
	}
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
	if u.inputLabel == inputPrompt || u.inputLabel == planInputPrompt || u.inputLabel == skillPlanInputPrompt {
		label = queuePrompt(summary.Status)
		if u.inputLabel != inputPrompt && summary.Status != session.StatusRunning && summary.Status != session.StatusWaitingForApproval {
			label = plainHistoryText(u.inputLabel)
		}
	}
	rows, cy, cx := inputRows(label+u.inputDisplay, utf8.RuneCountInString(label)+u.inputDisplayPosition, u.width, termtext.Width(label))
	queued := u.queuedPromptsLocked()
	if u.questionnaireActive {
		queued = nil
	}
	queue := u.activeQueueLocked()
	previewLimit := queuePreviewLimit(queued)
	var queueRows []queuedDisplayRow
	if len(queued) > 0 {
		queueWidth := u.width
		if queue.expanded {
			queueWidth = max(1, queueWidth-2)
		}
		queueRows = queueDisplayRows(queued, queueWidth, u.unicode)
	}
	// The live todo panel sits above the steer/queue section. It is driven by
	// the active agent's checklist snapshot; a finished list (all done) never
	// shows because the write path archives it to the transcript instead.
	todo := u.activeTodoLocked()
	var todoItems []todoItem
	todoDoneCount := 0
	todoLive := false
	if !u.questionnaireActive {
		if items, done, _, ok := u.todoSnapshotLocked(); ok && len(items) > 0 && !todoAllDone(items) {
			todoItems, todoDoneCount, todoLive = items, done, true
		}
	}
	if !todoLive {
		todo.expanded, todo.anchor = false, 0
	}
	var todoRows []todoDisplayRow
	if todoLive {
		todoRows = todoDisplayRows(todoItems, u.width, u.unicode)
	}
	bar := u.statusBar()
	statusN := statusBarLineCount(bar)
	if u.height < 5 {
		statusN = 1
	}
	footer := min(1+statusN, max(0, u.height-2))
	// The pin yields space to the composer, its separator, and two transcript
	// rows on short screens. Its height does not move a paused history anchor.
	latest := u.latestPromptRegionRowsLocked(u.height - footer - 5)
	// Keep a cursor-centered window for drafts taller than the terminal.
	inputLimit := u.height - 5 - statusN - len(latest)
	if len(queued) > 0 {
		reserve := min(previewLimit, len(queueRows), max(1, u.height-footer-5-len(latest)))
		inputLimit = min(inputLimit, u.height-footer-4-len(latest)-reserve)
	}
	inputHeight := min(len(rows), max(1, inputLimit))
	start := max(0, cy-inputHeight+1)
	rows = rows[start:min(len(rows), start+inputHeight)]
	cy -= start
	promptRow := u.height - footer - len(rows) + 1
	// All content panels end before this row, leaving exactly one separator
	// above the first composer row, including while output is unfinished.
	panelEnd := max(1, promptRow-1)
	if len(queued) == 0 && !u.questionnaireActive {
		queue.expanded, queue.anchorID, queue.anchorRow = false, "", 0
	}
	historyDirection := direction
	if queue.expanded && len(queued) > 0 {
		historyDirection = 0
	}
	if todo.expanded && todoLive {
		historyDirection = 0
	}
	suggestions := slashSuggestions(u.inputText)
	if u.inputLabel != inputPrompt && u.inputLabel != planInputPrompt && u.inputLabel != skillPlanInputPrompt {
		suggestions = nil
	}
	// Preserve two conversation rows when possible, yielding the second to
	// the queue shortcut on cramped screens. Each visible content region
	// below is separated by exactly one blank row: the pinned prompt from
	// the transcript, the transcript from suggestions/queue, and
	// suggestions from queue. The composer keeps its own separator above
	// it at panelEnd.
	baseOutputStart := 2 + len(latest)
	// Reserve the pinned-prompt separator only when some content follows.
	// It becomes the blank row at baseOutputStart.
	sepL := 0
	if len(latest) > 0 && panelEnd > baseOutputStart {
		sepL = 1
	}
	outputStart := baseOutputStart + sepL
	total := panelEnd - outputStart
	minOutput := 2
	if len(queued) > 0 && total < 3 {
		minOutput = 1
	}
	extras := max(0, total-minOutput)
	hasQueue := len(queueRows) > 0
	hasSugg := len(suggestions) > 0
	sepsNeeded := 0
	if hasQueue && hasSugg {
		sepsNeeded = 2
	} else if hasQueue || hasSugg {
		sepsNeeded = 1
	}
	queueHeight := 0
	count := 0
	outputHeight := 0
	sepO, sepS := 0, 0
	if extras >= sepsNeeded {
		extrasForPanels := extras - sepsNeeded
		if hasQueue && extrasForPanels > 0 {
			queueHeight = min(previewLimit, len(queueRows), extrasForPanels)
		}
		if hasSugg {
			count = min(len(suggestions), max(0, extrasForPanels-queueHeight))
		}
		if queue.expanded && queueHeight > 0 {
			room := max(0, extrasForPanels-count)
			queueHeight = min(1+len(queueRows), room, max(queueHeight, 2*room/3))
		}
		// Keep content over separators: if separators would hide pending
		// work or suggestions entirely, fall back to the touching layout.
		if (hasQueue && queueHeight == 0) || (hasSugg && count == 0) {
			sepL = 0
			outputStart = baseOutputStart
			total = panelEnd - outputStart
			minOutput = 2
			if len(queued) > 0 && total < 3 {
				minOutput = 1
			}
			extras = max(0, total-minOutput)
			queueHeight = 0
			if len(queued) > 0 && extras > 0 {
				queueHeight = min(previewLimit, len(queueRows), extras)
			}
			count = min(len(suggestions), extras-queueHeight)
			if queue.expanded && queueHeight > 0 {
				room := extras - count
				queueHeight = min(1+len(queueRows), room, max(queueHeight, 2*room/3))
			}
			outputHeight = max(0, panelEnd-count-queueHeight-outputStart)
			sepO, sepS = 0, 0
		} else {
			// Recompute actual separators from what will be shown. An
			// over-reserved separator becomes transcript space.
			if queueHeight > 0 || count > 0 {
				sepO = 1
			}
			if queueHeight > 0 && count > 0 {
				sepS = 1
			}
			actualSeps := sepO + sepS
			outputHeight = max(0, total-count-queueHeight-actualSeps)
			// Keep minimum transcript rows over separators on cramped
			// screens; fall back to touching when separators would hide
			// them.
			if outputHeight < minOutput && (count > 0 || queueHeight > 0 || len(latest) > 0) {
				// Check whether dropping all separators restores minimum
				// output. If so, prefer content over separators.
				withoutTotal := panelEnd - baseOutputStart
				withoutMin := 2
				if len(queued) > 0 && withoutTotal < 3 {
					withoutMin = 1
				}
				withoutExtras := max(0, withoutTotal-withoutMin)
				withoutQueue := 0
				if len(queued) > 0 && withoutExtras > 0 {
					withoutQueue = min(previewLimit, len(queueRows), withoutExtras)
				}
				withoutCount := min(len(suggestions), withoutExtras-withoutQueue)
				withoutOutput := max(0, withoutTotal-withoutCount-withoutQueue)
				if withoutOutput >= withoutMin && withoutOutput > outputHeight {
					sepL = 0
					outputStart = baseOutputStart
					total = withoutTotal
					minOutput = withoutMin
					extras = withoutExtras
					queueHeight = withoutQueue
					count = withoutCount
					if queue.expanded && queueHeight > 0 {
						room := extras - count
						queueHeight = min(1+len(queueRows), room, max(queueHeight, 2*room/3))
						count = min(len(suggestions), extras-queueHeight)
						withoutOutput = max(0, panelEnd-count-queueHeight-outputStart)
					}
					outputHeight = withoutOutput
					sepO, sepS = 0, 0
				}
			}
			// Drop the pinned-prompt separator when nothing follows; the
			// composer separator already separates the pin from the prompt.
			if outputHeight == 0 && count == 0 && queueHeight == 0 {
				sepL = 0
				outputStart = baseOutputStart
				total = panelEnd - outputStart
				sepO, sepS = 0, 0
			}
		}
	} else {
		// Cramped screens keep content over separators: fall back to the
		// touching layout so pending work and minimum output stay visible.
		sepL = 0
		outputStart = baseOutputStart
		total = panelEnd - outputStart
		minOutput = 2
		if len(queued) > 0 && total < 3 {
			minOutput = 1
		}
		extras = max(0, total-minOutput)
		if len(queued) > 0 && extras > 0 {
			queueHeight = min(previewLimit, len(queueRows), extras)
		}
		count = min(len(suggestions), extras-queueHeight)
		if queue.expanded && queueHeight > 0 {
			room := extras - count
			queueHeight = min(1+len(queueRows), room, max(queueHeight, 2*room/3))
		}
		outputHeight = max(0, panelEnd-count-queueHeight-outputStart)
		sepO, sepS = 0, 0
	}
	// Carve the todo block out of the transcript area, above the suggestions
	// and queue blocks. The collapsed preview is the header plus up to three
	// rows; an expanded panel pages through every row. Transcript minimums
	// win on cramped screens, hiding the panel until there is room.
	todoHeight := 0
	sepT := 0
	sepTodoFollowup := 0
	todoDesired := 0
	if todoLive {
		if todo.expanded {
			todoDesired = 1 + len(todoRows)
		} else {
			todoDesired = 1 + min(todoPreviewRows, len(todoRows))
		}
	}
	if todoDesired > 0 && outputHeight > minOutput {
		room := outputHeight - minOutput
		if room > todoDesired {
			todoHeight, sepT = todoDesired, 1
			if count > 0 || queueHeight > 0 {
				if sepO > 0 {
					// Reuse the existing transcript separator above TODO so
					// adding a follow-up gap does not double the top spacing.
					sepT = 0
				}
				// Keep a distinct gap between the TODO panel and follow-up.
				sepTodoFollowup = min(1, room-todoDesired-sepT)
			}
		} else {
			// Cramped: prefer panel content over its separator.
			todoHeight = room
		}
	}
	outputHeight -= todoHeight + sepT + sepTodoFollowup
	// When expanded panels own PageUp/Down but cannot scroll further (small
	// panels that fit entirely, or already at the top/bottom), fall back to
	// history so paging always makes visible progress instead of appearing
	// to do nothing. This keeps independent panel paging when panels can
	// move (see TestQueuedPanelStaysVisibleAndPagesIndependently) while
	// ensuring the transcript still pauses/scrolls when panels are stuck.
	if historyDirection == 0 && direction != 0 && outputHeight > 0 {
		queueCanScroll := false
		if queue.expanded && len(queued) > 0 && len(queueRows) > 0 && queueHeight > 1 {
			size := queueHeight - 1
			start := 0
			if queue.anchorID != "" {
				for i, row := range queueRows {
					if row.id == queue.anchorID && row.row <= queue.anchorRow {
						start = i
					}
				}
			}
			maxStart := max(0, len(queueRows)-size)
			clamped := max(0, min(start, maxStart))
			newStart := max(0, min(start-direction*size, maxStart))
			queueCanScroll = newStart != clamped
		}
		todoCanScroll := false
		if todo.expanded && todoLive && len(todoRows) > 0 && todoHeight > 1 {
			size := max(1, todoHeight-1)
			maxAnchor := max(0, len(todoRows)-size)
			clamped := max(0, min(todo.anchor, maxAnchor))
			newAnchor := max(0, min(maxAnchor, todo.anchor-direction*size))
			todoCanScroll = newAnchor != clamped
		}
		if !queueCanScroll && !todoCanScroll {
			historyDirection = direction
		}
	}
	u.activeViewportLocked().visibleRows = outputHeight
	screenRows := make([]string, u.height+1)
	if u.manager != nil && u.height > 3 {
		screenRows[1] = tabBar(u.manager.List(), u.activeAgent, u.views, u.width, u.unicode, ColorEnabled(u.out))
	}
	for i, row := range latest {
		screenRows[i+2] = row
	}
	if outputHeight > 0 && u.display != nil {
		page := u.activeViewportLocked().page(u.renderHistoryRowsLocked(u.display.Snapshot()), outputHeight, historyDirection)
		for i, row := range page {
			screenRows[i+outputStart] = row.text
		}
	}
	if u.remoteLogin != nil && outputHeight > 0 {
		for i := 0; i < outputHeight; i++ {
			screenRows[i+outputStart] = ""
		}
		for i, row := range u.remoteLoginRows(u.width, outputHeight) {
			if i == 0 {
				row = u.remoteLoginHeading(row)
			}
			screenRows[i+outputStart] = row
		}
	}
	for i := 0; i < count; i++ {
		text := fmt.Sprintf("  %-8s %s", suggestions[i].name, suggestions[i].description)
		if suggestions[i].subcommand {
			text = fmt.Sprintf("    %s %s", suggestions[i].name, suggestions[i].description)
		}
		if ColorEnabled(u.out) {
			if suggestions[i].subcommand {
				text = fmt.Sprintf("    %s%s%s %s%s%s", green, suggestions[i].name, reset, dim, suggestions[i].description, reset)
			} else {
				text = fmt.Sprintf("  %s%-8s%s %s%s%s", cyan, suggestions[i].name, reset, dim, suggestions[i].description, reset)
			}
		}
		if i == count-1 && len(suggestions) > count {
			text += " (type to filter)"
		}
		screenRows[panelEnd-queueHeight-sepS-count+i] = truncateDiffLine(text, u.width, u.unicode)
	}
	if todoHeight > 0 {
		todoStart := panelEnd - queueHeight - sepS - count - todoHeight - sepTodoFollowup
		header := todoHeader(todoDoneCount, len(todoItems), u.width, todo.expanded)
		if ColorEnabled(u.out) {
			// Theme-governed header: cyan maps to the Prompt role so
			// TransformANSI recolors it per palette (Default keeps ANSI).
			header = cyan + bold + header + reset
		}
		screenRows[todoStart] = truncateDiffLine(header, u.width, u.unicode)
		visible := todoRows
		if todo.expanded {
			size := max(1, todoHeight-1)
			if direction != 0 {
				todo.anchor = max(0, min(max(0, len(todoRows)-size), todo.anchor-direction*size))
			}
			start := max(0, min(todo.anchor, max(0, len(todoRows)-size)))
			todo.anchor = start
			visible = nil
			if start < len(todoRows) {
				visible = todoRows[start:min(len(todoRows), start+size)]
			}
		} else if len(visible) > max(0, todoHeight-1) {
			visible = visible[:max(0, todoHeight-1)]
		}
		color := ColorEnabled(u.out)
		for i, row := range visible {
			text := row.text
			if color {
				switch row.status {
				case "done":
					// Success (green) keeps completed rows theme-governed;
					// dim preserves the de-emphasized look.
					text = dim + green + text + reset
				case "doing":
					// Warning (yellow) makes in-progress rows stand out.
					text = bold + yellow + text + reset
				default:
					// Pending rows use the default themed text color.
					// Fall back to dimming when only the legacy done flag
					// is set (e.g. statuses missing).
					if row.done {
						text = dim + green + text + reset
					}
				}
			}
			screenRows[todoStart+1+i] = truncateDiffLine(text, u.width, u.unicode)
		}
	}
	if queueHeight > 0 {
		queueStart := panelEnd - queueHeight
		visible := queueRows[:min(len(queueRows), queueHeight)]
		if queue.expanded {
			header := "Alt+Q close | Up/Down | Delete remove | Esc"
			if u.width < 30 {
				header = "Alt+Q PgUp/Dn"
			}
			if ColorEnabled(u.out) {
				header = dim + header + reset
			}
			screenRows[queueStart] = truncateDiffLine(header, u.width, u.unicode)
			queueStart++
			visible = queue.page(queueRows, queueHeight-1, direction)
		}
		hasQueueHeading := false
		for _, item := range visible {
			if item.heading && item.text == "Queued" {
				hasQueueHeading = true
			}
		}
		for i, item := range visible {
			text := item.text
			if item.heading && text != "" {
				if !queue.expanded && (text == "Queued" || !hasQueueHeading) {
					text = queueHeading(text, u.width)
				}
				if ColorEnabled(u.out) {
					text = dim + text + reset
				}
			} else if !item.heading && queue.expanded && item.id == queue.selectedID {
				text = "> " + text
			}
			screenRows[queueStart+i] = truncateDiffLine(text, u.width, u.unicode)
		}
	}
	for i, row := range rows {
		screenRows[promptRow+i] = row
	}
	taskRow := u.height - statusN
	u.inputTaskRow = 0
	if footer == 1+statusN {
		u.inputTaskRow = taskRow
		screenRows[taskRow] = u.fixedTaskIndicatorMessageLocked(summary, time.Now())
	}
	if footer > 0 {
		lines := strings.Split(bar, "\n")
		if u.height < 5 && len(lines) > 1 {
			lines = lines[:1]
		}
		// Both status lines start at column 1 so the second aligns under
		// the first.
		start := u.height - len(lines) + 1
		for i, line := range lines {
			screenRows[start+i] = line
		}
		u.statusBarText = strings.Join(lines, "\n")
	}
	u.writeFixedScreenLocked(screenRows, promptRow+cy, cx+1)
}

// writeFixedScreenLocked diffs rows, then overwrites only their changed spans.
// Spinner ticks and typing leave unchanged text and cursor visibility alone.
func (u *UI) writeFixedScreenLocked(screenRows []string, cursorRow, cursorColumn int) {
	full := u.inputFrame == "" || len(u.inputScreenRows) != len(screenRows)
	changed := full || cursorRow != u.inputCursorRow || cursorColumn != u.inputCursorColumn
	if !changed {
		for row := 1; row < len(screenRows); row++ {
			if screenRows[row] != u.inputScreenRows[row] {
				changed = true
				break
			}
		}
	}
	if !changed {
		return
	}

	var rows strings.Builder
	for row := 1; row < len(screenRows); row++ {
		if !full && screenRows[row] == u.inputScreenRows[row] {
			continue
		}
		previous, painted := "", screenRows[row]
		if !full {
			previous = u.inputScreenRows[row]
		}
		if ColorEnabled(u.out) {
			painted = qtheme.PaintRow(painted, u.width, u.currentTheme())
			if !full {
				previous = qtheme.PaintRow(previous, u.width, u.currentTheme())
			}
		}
		if column, text := fixedRowDiff(previous, painted, u.width, full); text != "" {
			fmt.Fprintf(&rows, "\x1b[%d;%dH%s", row, column, text)
		}
	}
	keepCursor := !full && cursorRow == u.inputCursorRow && cursorColumn == u.inputCursorColumn
	if rows.Len() == 0 && keepCursor {
		u.inputScreenRows = screenRows
		return
	}
	var output strings.Builder
	// Each span is positioned explicitly. Disable terminal autowrap while
	// painting so a width mismatch cannot overwrite another cached row.
	if full {
		output.WriteString("\x1b[?25l")
	} else if keepCursor {
		output.WriteString("\x1b[s")
	}
	output.WriteString("\x1b[?7l")
	if full {
		output.WriteString("\x1b[0m\x1b[r")
	}
	output.WriteString(rows.String())
	output.WriteString("\x1b[?7h")
	if keepCursor {
		output.WriteString("\x1b[u")
	} else {
		fmt.Fprintf(&output, "\x1b[%d;%dH", cursorRow, cursorColumn)
	}
	if full {
		output.WriteString("\x1b[?25h")
	}
	if _, err := u.out.WriteString(output.String()); err != nil {
		return
	}
	u.inputFrame = "painted"
	u.inputScreenRows = screenRows
	u.inputCursorRow = cursorRow
	u.inputCursorColumn = cursorColumn
}
