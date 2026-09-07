package tui

import "strings"

// viewport has one owner: UI.screenMu. The anchor is a logical line ID and
// byte offset in its unstyled text, independent of incoming output and width.
type viewport struct {
	browsing bool
	anchor   historyPosition
}

type historyPosition struct {
	line   uint64
	column int
}

type historyRow struct {
	position historyPosition
	text     string
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

func historyRows(snapshot historySnapshot, width int) []historyRow {
	var rows []historyRow
	for _, line := range snapshot.lines {
		plain := plainHistoryText(line.text)
		consumed, style := 0, ""
		for _, part := range strings.Split(wrapANSI(line.text, width, ""), "\n") {
			text := plainHistoryText(part)
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

func (v *viewport) page(rows []historyRow, size, direction int) []historyRow {
	size = max(1, size)
	if len(rows) == 0 {
		v.browsing = false
		return nil
	}
	tail := max(0, len(rows)-size)
	start := v.start(rows, size)
	if direction > 0 {
		start -= size
	}
	if direction < 0 {
		start += size
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
