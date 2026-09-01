package tui

import (
	"strings"
	"unicode/utf8"
)

type displayUnit struct {
	raw   string
	width int
	space bool
}

// wrapANSI word-wraps terminal text without counting ANSI control sequences
// toward the available width. Words longer than the terminal are hard-wrapped.
func wrapANSI(text string, width int, continuation string) string {
	if width <= 0 || visibleWidth(text) <= width {
		return text
	}
	if visibleWidth(continuation) >= width {
		continuation = ""
	}
	units := displayUnits(text)
	var output strings.Builder
	line := make([]displayUnit, 0, len(units))
	lineWidth := 0

	writeBreak := func(split int) {
		before := trimRightSpaces(line[:split])
		for _, unit := range before {
			output.WriteString(unit.raw)
		}
		output.WriteByte('\n')
		output.WriteString(continuation)
		line = append([]displayUnit(nil), trimLeftSpaces(line[split:])...)
		lineWidth = visibleUnitsWidth(line) + visibleWidth(continuation)
	}

	for _, unit := range units {
		line = append(line, unit)
		lineWidth += unit.width
		for lineWidth > width {
			space := lastBreakableSpace(line)
			if space >= 0 {
				writeBreak(space + 1)
				continue
			}
			if len(line) <= 1 {
				break
			}
			last := line[len(line)-1]
			line = line[:len(line)-1]
			writeBreak(len(line))
			line = append(line, last)
			lineWidth += last.width
		}
	}
	for _, unit := range trimRightSpaces(line) {
		output.WriteString(unit.raw)
	}
	return output.String()
}

func displayUnits(text string) []displayUnit {
	units := make([]displayUnit, 0, len(text))
	for index := 0; index < len(text); {
		if text[index] == '\x1b' {
			end := index + 1
			if end < len(text) && text[end] == '[' {
				end++
				for end < len(text) {
					char := text[end]
					end++
					if char >= 0x40 && char <= 0x7e {
						break
					}
				}
			}
			units = append(units, displayUnit{raw: text[index:end]})
			index = end
			continue
		}
		r, size := utf8.DecodeRuneInString(text[index:])
		if size == 0 {
			break
		}
		raw := text[index : index+size]
		space := r == ' ' || r == '\t'
		if space {
			raw = " "
		}
		units = append(units, displayUnit{raw: raw, width: 1, space: space})
		index += size
	}
	return units
}

func visibleWidth(text string) int { return visibleUnitsWidth(displayUnits(text)) }

func visibleUnitsWidth(units []displayUnit) int {
	width := 0
	for _, unit := range units {
		width += unit.width
	}
	return width
}

func lastBreakableSpace(units []displayUnit) int {
	for index := len(units) - 1; index > 0; index-- {
		if units[index].space && visibleUnitsWidth(units[:index]) > 0 {
			return index
		}
	}
	return -1
}

func trimLeftSpaces(units []displayUnit) []displayUnit {
	for len(units) > 0 && units[0].space {
		units = units[1:]
	}
	return units
}

func trimRightSpaces(units []displayUnit) []displayUnit {
	for len(units) > 0 && units[len(units)-1].space {
		units = units[:len(units)-1]
	}
	return units
}
