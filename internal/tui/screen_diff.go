package tui

import "strings"

// screenGlyph keeps a whole grapheme and the SGR needed to paint it in
// isolation. Comparing styles as well as text catches color-only changes.
type screenGlyph struct {
	text, style string
	width       int
	offset      int
}

func (g screenGlyph) equal(other screenGlyph) bool {
	return g.text == other.text && g.style == other.style && g.width == other.width
}

func screenGlyphs(row string) ([]screenGlyph, int, bool) {
	var glyphs []screenGlyph
	var style strings.Builder
	width, offset := 0, 0
	for _, unit := range displayUnits(row) {
		if strings.HasPrefix(unit.raw, "\x1b") {
			if !strings.HasPrefix(unit.raw, "\x1b[") || !strings.HasSuffix(unit.raw, "m") {
				// Other terminal controls may affect more than glyph styling.
				// Keep them intact by falling back to a whole-row overwrite.
				return nil, visibleWidth(row), false
			}
			if unit.raw == reset || unit.raw == "\x1b[m" {
				style.Reset()
			} else {
				style.WriteString(unit.raw)
			}
			offset += len(unit.raw)
			continue
		}
		glyphs = append(glyphs, screenGlyph{unit.raw, style.String(), unit.width, offset})
		width += unit.width
		offset += len(unit.raw)
	}
	return glyphs, width, true
}

// fixedRowDiff returns a one-based column and an overwrite for the changed
// span. Erase only the leftover tail of a shorter row, after writing its new
// content: clearing first exposes a blank row on every animation frame.
func fixedRowDiff(previous, next string, width int, full bool) (int, string) {
	previous = strings.ReplaceAll(previous, "\t", " ")
	next = strings.ReplaceAll(next, "\t", " ")
	oldGlyphs, oldWidth, oldOK := screenGlyphs(previous)
	newGlyphs, newWidth, newOK := screenGlyphs(next)
	if !oldOK || !newOK || oldWidth > width || newWidth > width {
		text := reset + next + reset
		if newWidth < width {
			text += "\x1b[K"
		}
		return 1, text
	}
	start, column := 0, 1
	if !full {
		for start < len(oldGlyphs) && start < len(newGlyphs) && oldGlyphs[start].equal(newGlyphs[start]) {
			column += newGlyphs[start].width
			start++
		}
		if start == len(oldGlyphs) && start == len(newGlyphs) {
			return 0, ""
		}
	}
	end := len(newGlyphs)
	// A suffix can remain untouched only if it still occupies the same cells.
	// Whole graphemes keep edits away from the middle of wide/combined glyphs.
	if !full && oldWidth == newWidth {
		oldEnd := len(oldGlyphs)
		for end > start && oldEnd > start && newGlyphs[end-1].equal(oldGlyphs[oldEnd-1]) {
			end--
			oldEnd--
		}
	}
	var text strings.Builder
	text.WriteString(reset)
	if start < end {
		text.WriteString(newGlyphs[start].style)
		last := newGlyphs[end-1]
		text.WriteString(next[newGlyphs[start].offset : last.offset+len(last.text)])
	}
	text.WriteString(reset)
	if newWidth < width && (full || oldWidth > newWidth) {
		text.WriteString("\x1b[K")
	}
	return column, text.String()
}
