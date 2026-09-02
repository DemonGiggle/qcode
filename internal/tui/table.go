package tui

import (
	"fmt"
	"strings"
)

type markdownTableLine struct {
	text    string
	newline bool
}

type tableAlignment uint8

const (
	alignLeft tableAlignment = iota
	alignCenter
	alignRight
)

func (w *MarkdownWriter) consumeLine(line string, newline bool) {
	if w.thinking || w.inFence {
		w.renderCompleteLine(line, newline)
		return
	}
	if len(w.table) == 0 {
		if isPotentialTableRow(line) {
			w.table = append(w.table, markdownTableLine{text: line, newline: newline})
			return
		}
		w.renderCompleteLine(line, newline)
		return
	}
	if len(w.table) == 1 {
		headerCells := splitTableRow(w.table[0].text)
		if _, ok := tableAlignments(line, len(headerCells)); ok {
			w.table = append(w.table, markdownTableLine{text: line, newline: newline})
			return
		}
		w.flushTable()
		w.consumeLine(line, newline)
		return
	}
	if isPotentialTableRow(line) {
		w.table = append(w.table, markdownTableLine{text: line, newline: newline})
		return
	}
	w.flushTable()
	w.consumeLine(line, newline)
}

func (w *MarkdownWriter) flushTable() {
	if len(w.table) == 0 {
		return
	}
	lines := w.table
	w.table = nil
	if len(lines) < 2 {
		w.renderCompleteLine(lines[0].text, lines[0].newline)
		return
	}
	header := splitTableRow(lines[0].text)
	alignments, ok := tableAlignments(lines[1].text, len(header))
	if !ok {
		for _, line := range lines {
			w.renderCompleteLine(line.text, line.newline)
		}
		return
	}
	rows := make([][]string, 0, len(lines)-1)
	rows = append(rows, normalizeTableRow(header, len(header)))
	for _, line := range lines[2:] {
		rows = append(rows, normalizeTableRow(splitTableRow(line.text), len(header)))
	}
	sanitizeTableCells(rows, w.unicode)
	if !w.renderTable(rows, alignments, lines[len(lines)-1].newline) {
		for _, line := range lines {
			w.renderCompleteLine(line.text, line.newline)
		}
	}
}

func sanitizeTableCells(rows [][]string, unicodeEnabled bool) {
	escapeLabel := "␛"
	if !unicodeEnabled {
		escapeLabel = "<ESC>"
	}
	for _, row := range rows {
		for index := range row {
			row[index] = sanitizeDiffLine(row[index], escapeLabel)
		}
	}
}

func isPotentialTableRow(line string) bool {
	trimmed := strings.TrimSpace(line)
	return trimmed != "" && strings.Contains(trimmed, "|") && !strings.HasPrefix(trimmed, ">") &&
		!strings.HasPrefix(trimmed, "```") && !strings.HasPrefix(trimmed, "~~~")
}

func splitTableRow(line string) []string {
	line = strings.TrimSpace(line)
	if strings.HasPrefix(line, "|") {
		line = line[1:]
	}
	if strings.HasSuffix(line, "|") && !strings.HasSuffix(line, `\|`) {
		line = line[:len(line)-1]
	}
	var cells []string
	var cell strings.Builder
	inCode := false
	escaped := false
	for _, char := range line {
		switch {
		case escaped:
			if char != '|' {
				cell.WriteRune('\\')
			}
			cell.WriteRune(char)
			escaped = false
		case char == '\\':
			escaped = true
		case char == '`':
			inCode = !inCode
			cell.WriteRune(char)
		case char == '|' && !inCode:
			cells = append(cells, strings.TrimSpace(cell.String()))
			cell.Reset()
		default:
			cell.WriteRune(char)
		}
	}
	if escaped {
		cell.WriteRune('\\')
	}
	cells = append(cells, strings.TrimSpace(cell.String()))
	return cells
}

func tableAlignments(line string, columns int) ([]tableAlignment, bool) {
	cells := splitTableRow(line)
	if columns == 0 || len(cells) != columns {
		return nil, false
	}
	alignments := make([]tableAlignment, columns)
	for index, cell := range cells {
		cell = strings.TrimSpace(cell)
		left := strings.HasPrefix(cell, ":")
		right := strings.HasSuffix(cell, ":")
		dashes := strings.Trim(cell, ":")
		if len(dashes) < 3 || strings.Trim(dashes, "-") != "" {
			return nil, false
		}
		switch {
		case left && right:
			alignments[index] = alignCenter
		case right:
			alignments[index] = alignRight
		default:
			alignments[index] = alignLeft
		}
	}
	return alignments, true
}

func normalizeTableRow(cells []string, columns int) []string {
	row := make([]string, columns)
	copy(row, cells)
	return row
}

func (w *MarkdownWriter) renderTable(rows [][]string, alignments []tableAlignment, finalNewline bool) bool {
	widths := tableColumnWidths(rows)
	if !fitTableWidths(widths, w.width) {
		return false
	}
	top, middle, bottom, vertical := tableGlyphs(w.unicode)
	w.writeTableLine(tableBorder(widths, top, w.enabled), true)
	for rowIndex, row := range rows {
		w.writeTableLine(w.tableDataRow(row, widths, alignments, rowIndex == 0, vertical), true)
		if rowIndex == 0 {
			w.writeTableLine(tableBorder(widths, middle, w.enabled), true)
		}
	}
	w.writeTableLine(tableBorder(widths, bottom, w.enabled), finalNewline)
	return true
}

func (w *MarkdownWriter) writeTableLine(line string, newline bool) {
	fmt.Fprint(w.out, line)
	if newline {
		fmt.Fprint(w.out, "\n")
	}
}

func tableColumnWidths(rows [][]string) []int {
	widths := make([]int, len(rows[0]))
	for _, row := range rows {
		for index, cell := range row {
			if width := visibleWidth(renderInline(cell)); width > widths[index] {
				widths[index] = width
			}
		}
	}
	for index := range widths {
		if widths[index] < 1 {
			widths[index] = 1
		}
	}
	return widths
}

func fitTableWidths(widths []int, terminalWidth int) bool {
	if terminalWidth <= 0 {
		return true
	}
	available := terminalWidth - (3*len(widths) + 1)
	if available < len(widths) {
		return false
	}
	total := 0
	maximum := 0
	for _, width := range widths {
		total += width
		if width > maximum {
			maximum = width
		}
	}
	if total <= available {
		return true
	}
	original := append([]int(nil), widths...)
	low, high := 1, maximum
	for low < high {
		mid := (low + high + 1) / 2
		used := 0
		for _, width := range widths {
			used += min(width, mid)
		}
		if used <= available {
			low = mid
		} else {
			high = mid - 1
		}
	}
	used := 0
	for index := range widths {
		widths[index] = min(widths[index], low)
		used += widths[index]
	}
	for used < available {
		changed := false
		for index := range widths {
			if widths[index] < original[index] && used < available {
				widths[index]++
				used++
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	return true
}

func tableGlyphs(unicodeEnabled bool) ([4]string, [4]string, [4]string, string) {
	if !unicodeEnabled {
		return [4]string{"+", "+", "+", "-"}, [4]string{"+", "+", "+", "-"}, [4]string{"+", "+", "+", "-"}, "|"
	}
	return [4]string{"┌", "┬", "┐", "─"}, [4]string{"├", "┼", "┤", "─"}, [4]string{"└", "┴", "┘", "─"}, "│"
}

func tableBorder(widths []int, glyphs [4]string, styled bool) string {
	var line strings.Builder
	line.WriteString(glyphs[0])
	for index, width := range widths {
		line.WriteString(strings.Repeat(glyphs[3], width+2))
		if index == len(widths)-1 {
			line.WriteString(glyphs[2])
		} else {
			line.WriteString(glyphs[1])
		}
	}
	if styled {
		return gray + line.String() + reset
	}
	return line.String()
}

func (w *MarkdownWriter) tableDataRow(cells []string, widths []int, alignments []tableAlignment, header bool, vertical string) string {
	var line strings.Builder
	border := vertical
	if w.enabled {
		border = gray + vertical + reset
	}
	line.WriteString(border)
	for index, cell := range cells {
		rendered := renderInline(cell)
		if header && w.enabled {
			rendered = bold + rendered + reset
		}
		rendered = truncateDiffLine(rendered, widths[index], w.unicode)
		if w.enabled {
			rendered += reset
		}
		padding := widths[index] - visibleWidth(rendered)
		left, right := 0, padding
		switch alignments[index] {
		case alignRight:
			left, right = padding, 0
		case alignCenter:
			left, right = padding/2, padding-padding/2
		}
		fmt.Fprintf(&line, " %s%s%s %s", strings.Repeat(" ", left), rendered, strings.Repeat(" ", right), border)
	}
	return line.String()
}
