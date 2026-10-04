// Package termtext measures terminal cells without splitting visible characters.
package termtext

import (
	"strings"

	"github.com/mattn/go-runewidth"
	"github.com/rivo/uniseg"
)

// Unit is one grapheme cluster or one zero-width ANSI control sequence.
type Unit struct {
	Text  string
	Width int
}

func Units(text string) []Unit {
	units := make([]Unit, 0, len(text))
	for len(text) > 0 {
		if text[0] == '\x1b' {
			length := escapeLength(text)
			units = append(units, Unit{Text: text[:length]})
			text = text[length:]
			continue
		}
		end := strings.IndexByte(text, '\x1b')
		if end < 0 {
			end = len(text)
		}
		clusters := uniseg.NewGraphemes(text[:end])
		for clusters.Next() {
			cluster := clusters.Str()
			units = append(units, Unit{Text: cluster, Width: clusterWidth(cluster)})
		}
		text = text[end:]
	}
	return units
}

func Width(text string) int {
	width := 0
	for _, unit := range Units(text) {
		width += unit.Width
	}
	return width
}

func clusterWidth(cluster string) int {
	width, regionalIndicators := 0, 0
	emojiPresentation := false
	for _, r := range cluster {
		width = max(width, runewidth.RuneWidth(r))
		if r == '\ufe0f' || r == '\u20e3' {
			emojiPresentation = true
		}
		if r >= '\U0001f1e6' && r <= '\U0001f1ff' {
			regionalIndicators++
		}
	}
	// Terminals display an emoji presentation or a flag as two cells. ZWJ
	// and skin-tone sequences share the width of their widest component.
	if width > 0 && (emojiPresentation || regionalIndicators == 2) {
		width = max(2, width)
	}
	return width
}

func escapeLength(text string) int {
	if len(text) < 2 {
		return len(text)
	}
	switch text[1] {
	case '[':
		for i := 2; i < len(text); i++ {
			if text[i] >= 0x40 && text[i] <= 0x7e {
				return i + 1
			}
		}
		return len(text)
	case ']', 'P', '^', '_':
		for i := 2; i < len(text); i++ {
			if text[1] == ']' && text[i] == '\a' {
				return i + 1
			}
			if text[i] == '\x1b' && i+1 < len(text) && text[i+1] == '\\' {
				return i + 2
			}
		}
		return len(text)
	default:
		return 2
	}
}
