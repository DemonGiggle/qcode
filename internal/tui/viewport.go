package tui

import (
	"strings"

	qtheme "qcode/internal/theme"
)

// viewport has one owner: UI.screenMu. The anchor is a logical line ID and
// byte offset in its unstyled text, independent of incoming output and width.
type viewport struct {
	browsing    bool
	anchor      historyPosition
	visibleRows int
}

type historyPosition struct {
	line   uint64
	column int
}

type historyRow struct {
	position historyPosition
	text     string
}

// historyRowCache belongs to UI.screenMu, alongside the display's viewport.
// Committed lines are immutable; only a changed suffix needs to be wrapped.
// Keep one width/theme per display so resizing cannot grow the cache forever.
type historyRowCache struct {
	width   int
	palette qtheme.Palette
	lines   []cachedHistoryLine
	rows    []historyRow
}

type cachedHistoryLine struct {
	line historyLine
	end  int // Exclusive end of this logical line in the wrapped rows.
}

func (u *UI) renderHistoryRowsLocked(snapshot historySnapshot) []historyRow {
	cache := &u.rowCache
	if view := u.views[u.activeAgent]; view != nil {
		cache = &view.rowCache
	}
	return cache.render(snapshot, u.width, u.outputTheme())
}

// render returns borrowed rows, valid until the next call under screenMu.
func (c *historyRowCache) render(snapshot historySnapshot, width int, palette qtheme.Palette) []historyRow {
	if c.width != width || c.palette != palette {
		*c = historyRowCache{width: width, palette: palette}
	}
	if len(snapshot.lines) > 0 && len(c.lines) > 0 {
		// Retention drops old logical lines without changing surviving IDs.
		// Move their cached rows rather than rewrapping the remaining history.
		drop := 0
		for drop < len(c.lines) && c.lines[drop].line.id < snapshot.lines[0].id {
			drop++
		}
		if drop > 0 {
			rowDrop := c.lines[drop-1].end
			n := copy(c.rows, c.rows[rowDrop:])
			clear(c.rows[n:])
			c.rows = c.rows[:n]
			n = copy(c.lines, c.lines[drop:])
			clear(c.lines[n:])
			c.lines = c.lines[:n]
			for i := range c.lines {
				c.lines[i].end -= rowDrop
			}
		}
	}
	unchanged := 0
	for unchanged < len(c.lines) && unchanged < len(snapshot.lines) && c.lines[unchanged].line == snapshot.lines[unchanged] {
		unchanged++
	}
	end := 0
	if unchanged > 0 {
		end = c.lines[unchanged-1].end
	}
	clear(c.rows[end:])
	c.rows = c.rows[:end]
	clear(c.lines[unchanged:])
	c.lines = c.lines[:unchanged]
	for _, line := range snapshot.lines[unchanged:] {
		styled := historyLine{line.id, qtheme.TransformANSI(line.text, palette)}
		c.rows = append(c.rows, historyRows(historySnapshot{lines: []historyLine{styled}}, width)...)
		c.lines = append(c.lines, cachedHistoryLine{line: line, end: len(c.rows)})
	}
	return c.rows
}

func plainHistoryText(text string) string {
	var result strings.Builder
	for _, unit := range displayUnits(text) {
		if !strings.HasPrefix(unit.raw, "\x1b") {
			result.WriteString(unit.raw)
		}
	}
	return result.String()
}

// Question options are retained as logical numbered lines. Reserve the marker
// before wrapping so resizing keeps the same layout as the question panel.
func wrapHistoryLine(text, plain string, width int) (wrapped, continuation string) {
	if strings.HasPrefix(plain, "   ") {
		end := 3
		for end < len(plain) && plain[end] >= '0' && plain[end] <= '9' {
			end++
		}
		if end > 3 && strings.HasPrefix(plain[end:], ") ") {
			end += 2
			// Map the plain marker back to its styled bytes so ANSI sequences
			// remain intact when the label is wrapped separately.
			offset, consumed := 0, 0
			for _, unit := range displayUnits(text) {
				offset += len(unit.raw)
				if !strings.HasPrefix(unit.raw, "\x1b") {
					consumed += len(unit.raw)
				}
				if consumed >= end {
					break
				}
			}
			prefix := text[:offset]
			return wrapANSIWithPrefix(prefix, text[offset:], width), strings.Repeat(" ", visibleWidth(prefix))
		}
	}
	return wrapANSI(text, width, ""), ""
}

func historyRows(snapshot historySnapshot, width int) []historyRow {
	var rows []historyRow
	for _, line := range snapshot.lines {
		plain := plainHistoryText(line.text)
		consumed, style := 0, ""
		wrapped, continuation := wrapHistoryLine(line.text, plain, width)
		for index, part := range strings.Split(wrapped, "\n") {
			text := plainHistoryText(part)
			if index > 0 {
				// Added indentation occupies display cells but is not part of
				// the source line used to anchor a paused reading position.
				text = strings.TrimPrefix(text, continuation)
			}
			start := consumed
			if found := strings.Index(plain[consumed:], text); found >= 0 {
				start += found
			}
			rows = append(rows, historyRow{historyPosition{line.id, start}, style + part + reset})
			consumed = min(len(plain), start+len(text))
			for _, unit := range displayUnits(part) {
				if strings.HasPrefix(unit.raw, "\x1b[") && strings.HasSuffix(unit.raw, "m") {
					params := unit.raw[2 : len(unit.raw)-1]
					if params == "" || hasANSIReset(params) {
						style = ""
					}
					style += unit.raw
				}
			}
		}
	}
	return rows
}

func historyRowsForTheme(snapshot historySnapshot, width int, palette qtheme.Palette) []historyRow {
	snapshot.lines = append([]historyLine(nil), snapshot.lines...)
	for index := range snapshot.lines {
		snapshot.lines[index].text = qtheme.TransformANSI(snapshot.lines[index].text, palette)
	}
	snapshot.style = qtheme.TransformANSI(snapshot.style, palette)
	return historyRows(snapshot, width)
}

func (v *viewport) start(rows []historyRow, size int) int {
	if !v.browsing {
		return max(0, len(rows)-size)
	}
	start := 0
	for i, row := range rows {
		if row.position.line > v.anchor.line || row.position.line == v.anchor.line && row.position.column > v.anchor.column {
			break
		}
		start = i
	}
	return start
}

// atBottom checks the current row position against the actual output height.
// Resizing, rewrapping, or removing footer rows can bring a paused view to the
// bottom without a PageDown key changing its stored browsing flag.
func (v *viewport) atBottom(rows []historyRow) bool {
	if len(rows) == 0 || v.visibleRows <= 0 {
		return true
	}
	return v.start(rows, v.visibleRows) >= max(0, len(rows)-v.visibleRows)
}

func (v *viewport) page(rows []historyRow, size, direction int) []historyRow {
	size = max(1, size)
	v.visibleRows = size
	if len(rows) == 0 {
		v.browsing = false
		return nil
	}
	tail := max(0, len(rows)-size)
	start := v.start(rows, size)
	// Retain two rendered rows between pages, while allowing small views to move.
	step := max(1, size-2)
	if direction > 0 {
		start -= step
	}
	if direction < 0 {
		start += step
	}
	start = max(0, min(start, len(rows)-1))
	if direction != 0 {
		v.browsing = start < tail
		if !v.browsing {
			start = tail
		}
	}
	if v.browsing && (direction != 0 || v.anchor.line < rows[0].position.line) {
		v.anchor = rows[start].position
	}
	return rows[start:min(len(rows), start+size)]
}
