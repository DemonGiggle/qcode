package tui

import (
	"fmt"
	"strings"

	"github.com/mattn/go-runewidth"
	"qcode/internal/redaction"
	qtheme "qcode/internal/theme"
)

type latestPromptReader interface {
	LatestPrompt(string) string
}

func latestPromptMarker(unicode bool) string { return interfaceGlyph(unicode, "✦", "*") }

// The region uses up to three prompt rows. On small screens it gives space
// back to the transcript and composer.
func (u *UI) latestPromptRegionRowsLocked(available int) []string {
	return u.latestPromptRowsLocked(min(3, available))
}

// latestPromptRows keeps literal user text out of the markdown and ANSI parsers.
// Filtering happens before wrapping so secrets spanning rows remain protected.
func (u *UI) latestPromptRowsLocked(limit int) []string {
	if u.commandViewActive {
		return nil
	}
	source, ok := u.manager.(latestPromptReader)
	if !ok || limit <= 0 {
		return nil
	}
	text := u.redaction.Text(redaction.Terminal, source.LatestPrompt(u.activeAgent))
	color := u.out != nil && ColorEnabled(u.out)
	rows := latestPromptRows(text, u.width, limit, u.unicode, color)
	if color {
		for i, row := range rows {
			rows[i] = paintLatestPromptBackground(row, u.width, u.currentTheme())
		}
	}
	return rows
}

func paintLatestPromptBackground(row string, width int, palette qtheme.Palette) string {
	// Put padding before the reset so every cell, including the right edge,
	// gets the highlight.
	return rgbSGR(48, qtheme.PinnedPromptBackground(palette)) + strings.TrimSuffix(row, reset) +
		strings.Repeat(" ", max(0, width-visibleWidth(row))) + reset
}

func latestPromptRows(text string, width, limit int, unicode, color bool) []string {
	if text == "" || width < 1 || limit < 1 {
		return nil
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	var literal strings.Builder
	marker := latestPromptMarker(unicode)
	literal.WriteString(marker + " ")
	for _, r := range text {
		switch {
		case r == '\n':
			literal.WriteRune(r)
		case r == '\t':
			literal.WriteString("    ")
		case r < 32 || r >= 127 && r < 160:
			fmt.Fprintf(&literal, "<0x%02X>", r)
		case runewidth.RuneWidth(r) > width:
			literal.WriteByte('?')
		default:
			literal.WriteRune(r)
		}
	}
	var rows []string
	for _, line := range strings.Split(literal.String(), "\n") {
		rows = append(rows, strings.Split(wrapANSI(line, width, ""), "\n")...)
		if len(rows) > limit {
			break
		}
	}
	if len(rows) > limit {
		rows = rows[:limit]
		// Force the suffix even when overflow came from an explicit newline.
		suffix := interfaceGlyph(unicode, "…", "...")
		if len(suffix) > width && !unicode {
			suffix = suffix[:width]
		}
		var last strings.Builder
		used := 0
		for _, unit := range displayUnits(rows[limit-1]) {
			if used+unit.width > width-visibleWidth(suffix) {
				break
			}
			last.WriteString(unit.raw)
			used += unit.width
		}
		rows[limit-1] = strings.TrimRight(last.String(), " ") + suffix
	}
	if color {
		for i, row := range rows {
			if i == 0 && strings.HasPrefix(row, marker) {
				row = bold + marker + "\x1b[22m" + row[len(marker):]
			}
			rows[i] = magenta + row + reset
		}
	}
	return rows
}

// Caller holds screenMu. The same boundary is used by transcript paging and
// selectors that draw absolute rows, including plan and history viewers.
func (u *UI) latestPromptStartLocked() int {
	return 2 + len(u.latestPromptRegionRowsLocked(u.height-u.statusLinesLocked()-5))
}
