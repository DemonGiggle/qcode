// Package theme defines terminal color palettes shared by configuration and
// the interactive terminal UI.
package theme

import (
	"fmt"
	"strconv"
	"strings"

	"qcode/internal/termtext"
)

type Palette struct {
	ID, Name, Appearance string
	Background           string
	Text                 string
	Prompt               string
	Markdown             string
	Status               string
	Diff                 string
	Success              string
	Warning              string
	Error                string
	Accent               string
	Muted                string
	Link                 string
}

var palettes = []Palette{
	{ID: "default", Name: "Default", Appearance: "Auto", Background: "", Text: "", Prompt: "", Markdown: "", Status: "", Diff: "", Success: "", Warning: "", Error: "", Accent: "", Muted: "", Link: ""},
	{ID: "catppuccin-mocha", Name: "Catppuccin Mocha", Appearance: "Dark", Background: "#1e1e2e", Text: "#cdd6f4", Prompt: "#89b4fa", Markdown: "#cdd6f4", Status: "#cba6f7", Diff: "#f9e2af", Success: "#a6e3a1", Warning: "#f9e2af", Error: "#f38ba8", Accent: "#cba6f7", Muted: "#a6adc8", Link: "#89dceb"},
	{ID: "dracula", Name: "Dracula", Appearance: "Dark", Background: "#282a36", Text: "#f8f8f2", Prompt: "#8be9fd", Markdown: "#f8f8f2", Status: "#bd93f9", Diff: "#f1fa8c", Success: "#50fa7b", Warning: "#f1fa8c", Error: "#ff5555", Accent: "#ff79c6", Muted: "#6272a4", Link: "#8be9fd"},
	{ID: "gruvbox-dark", Name: "Gruvbox Dark", Appearance: "Dark", Background: "#282828", Text: "#ebdbb2", Prompt: "#8ec07c", Markdown: "#ebdbb2", Status: "#d3869b", Diff: "#fabd2f", Success: "#b8bb26", Warning: "#fabd2f", Error: "#fb4934", Accent: "#d3869b", Muted: "#a89984", Link: "#8ec07c"},
	{ID: "solarized-dark", Name: "Solarized Dark", Appearance: "Dark", Background: "#002b36", Text: "#839496", Prompt: "#268bd2", Markdown: "#839496", Status: "#6c71c4", Diff: "#b58900", Success: "#859900", Warning: "#b58900", Error: "#dc322f", Accent: "#d33682", Muted: "#586e75", Link: "#2aa198"},
	{ID: "nord-dark", Name: "Nord Dark", Appearance: "Dark", Background: "#2e3440", Text: "#d8dee9", Prompt: "#88c0d0", Markdown: "#d8dee9", Status: "#81a1c1", Diff: "#ebcb8b", Success: "#a3be8c", Warning: "#ebcb8b", Error: "#bf616a", Accent: "#b48ead", Muted: "#616e88", Link: "#8fbcbb"},
	{ID: "tokyo-night", Name: "Tokyo Night", Appearance: "Dark", Background: "#1a1b26", Text: "#c0caf5", Prompt: "#7aa2f7", Markdown: "#c0caf5", Status: "#bb9af7", Diff: "#e0af68", Success: "#9ece6a", Warning: "#e0af68", Error: "#f7768e", Accent: "#bb9af7", Muted: "#a9b1d6", Link: "#7dcfff"},
	{ID: "one-dark", Name: "One Dark", Appearance: "Dark", Background: "#282c34", Text: "#abb2bf", Prompt: "#61afef", Markdown: "#abb2bf", Status: "#c678dd", Diff: "#e5c07b", Success: "#98c379", Warning: "#e5c07b", Error: "#e06c75", Accent: "#c678dd", Muted: "#828997", Link: "#56b6c2"},
	{ID: "rose-pine", Name: "Rosé Pine", Appearance: "Dark", Background: "#191724", Text: "#e0def4", Prompt: "#9ccfd8", Markdown: "#e0def4", Status: "#c4a7e7", Diff: "#f6c177", Success: "#95b1ac", Warning: "#f6c177", Error: "#eb6f92", Accent: "#ebbcba", Muted: "#908caa", Link: "#9ccfd8"},
	{ID: "everforest-dark", Name: "Everforest Dark", Appearance: "Dark", Background: "#2d353b", Text: "#d3c6aa", Prompt: "#7fbbb3", Markdown: "#d3c6aa", Status: "#d699b6", Diff: "#dbbc7f", Success: "#a7c080", Warning: "#dbbc7f", Error: "#e67e80", Accent: "#83c092", Muted: "#9da9a0", Link: "#7fbbb3"},
	{ID: "kanagawa-wave", Name: "Kanagawa Wave", Appearance: "Dark", Background: "#1f1f28", Text: "#dcd7ba", Prompt: "#7e9cd8", Markdown: "#dcd7ba", Status: "#957fb8", Diff: "#e6c384", Success: "#98bb6c", Warning: "#e6c384", Error: "#e46876", Accent: "#d27e99", Muted: "#c8c093", Link: "#7fb4ca"},
	{ID: "ayu-dark", Name: "Ayu Dark", Appearance: "Dark", Background: "#10141c", Text: "#bfbdb6", Prompt: "#59c2ff", Markdown: "#bfbdb6", Status: "#d2a6ff", Diff: "#ffb454", Success: "#aad94c", Warning: "#ffb454", Error: "#d95757", Accent: "#e6b450", Muted: "#5a6673", Link: "#95e6cb"},
	{ID: "catppuccin-latte", Name: "Catppuccin Latte", Appearance: "Light", Background: "#eff1f5", Text: "#4c4f69", Prompt: "#1e66f5", Markdown: "#4c4f69", Status: "#8839ef", Diff: "#df8e1d", Success: "#40a02b", Warning: "#df8e1d", Error: "#d20f39", Accent: "#ea76cb", Muted: "#8c8fa1", Link: "#179299"},
	{ID: "alucard", Name: "Alucard", Appearance: "Light", Background: "#fffbeb", Text: "#1f1f1f", Prompt: "#644ac9", Markdown: "#1f1f1f", Status: "#a3144d", Diff: "#846e15", Success: "#14710a", Warning: "#846e15", Error: "#cb3a2a", Accent: "#a3144d", Muted: "#6c664b", Link: "#036a96"},
	{ID: "gruvbox-light", Name: "Gruvbox Light", Appearance: "Light", Background: "#fbf1c7", Text: "#3c3836", Prompt: "#427b58", Markdown: "#3c3836", Status: "#8f3f71", Diff: "#b57614", Success: "#79740e", Warning: "#b57614", Error: "#9d0006", Accent: "#8f3f71", Muted: "#928374", Link: "#427b58"},
	{ID: "solarized-light", Name: "Solarized Light", Appearance: "Light", Background: "#fdf6e3", Text: "#657b83", Prompt: "#268bd2", Markdown: "#657b83", Status: "#6c71c4", Diff: "#b58900", Success: "#859900", Warning: "#b58900", Error: "#dc322f", Accent: "#d33682", Muted: "#93a1a1", Link: "#2aa198"},
	{ID: "nord-light", Name: "Nord Light", Appearance: "Light", Background: "#eceff4", Text: "#2e3440", Prompt: "#5e81ac", Markdown: "#2e3440", Status: "#5e81ac", Diff: "#d08770", Success: "#a3be8c", Warning: "#ebcb8b", Error: "#bf616a", Accent: "#b48ead", Muted: "#7b88a1", Link: "#5e81ac"},
	{ID: "tokyo-night-day", Name: "Tokyo Night Day", Appearance: "Light", Background: "#e1e2e7", Text: "#3760bf", Prompt: "#2e7de9", Markdown: "#3760bf", Status: "#9854f1", Diff: "#8c6c3e", Success: "#587539", Warning: "#8c6c3e", Error: "#f52a65", Accent: "#9854f1", Muted: "#6172b0", Link: "#007197"},
	{ID: "one-light", Name: "One Light", Appearance: "Light", Background: "#fafafa", Text: "#383a42", Prompt: "#4078f2", Markdown: "#383a42", Status: "#a626a4", Diff: "#986801", Success: "#50a14f", Warning: "#986801", Error: "#e45649", Accent: "#a626a4", Muted: "#696c77", Link: "#0184bc"},
	{ID: "rose-pine-dawn", Name: "Rosé Pine Dawn", Appearance: "Light", Background: "#faf4ed", Text: "#464261", Prompt: "#286983", Markdown: "#464261", Status: "#907aa9", Diff: "#ea9d34", Success: "#6d8f89", Warning: "#ea9d34", Error: "#b4637a", Accent: "#d7827e", Muted: "#797593", Link: "#56949f"},
	{ID: "everforest-light", Name: "Everforest Light", Appearance: "Light", Background: "#fffbef", Text: "#5c6a72", Prompt: "#3a94c5", Markdown: "#5c6a72", Status: "#df69ba", Diff: "#dfa000", Success: "#8da101", Warning: "#dfa000", Error: "#f85552", Accent: "#35a77c", Muted: "#829181", Link: "#3a94c5"},
	{ID: "kanagawa-lotus", Name: "Kanagawa Lotus", Appearance: "Light", Background: "#f2ecbc", Text: "#545464", Prompt: "#4d699b", Markdown: "#545464", Status: "#624c83", Diff: "#836f4a", Success: "#6f894e", Warning: "#836f4a", Error: "#c84053", Accent: "#b35b79", Muted: "#716e61", Link: "#597b75"},
	{ID: "ayu-light", Name: "Ayu Light", Appearance: "Light", Background: "#fcfcfc", Text: "#5c6166", Prompt: "#22a4e6", Markdown: "#5c6166", Status: "#a37acc", Diff: "#e59645", Success: "#86b300", Warning: "#e59645", Error: "#e65050", Accent: "#f29718", Muted: "#828e9f", Link: "#55b4d4"},
}

var paletteByID = func() map[string]Palette {
	result := make(map[string]Palette, len(palettes))
	for _, palette := range palettes {
		result[palette.ID] = palette
	}
	return result
}()

func Default() Palette { return palettes[0] }

// PinnedPromptBackground softly tints the surface with the theme's accent.
// Default uses a dark neutral surface because terminal Auto has no fixed color.
func PinnedPromptBackground(palette Palette) string {
	base, accent := palette.Background, palette.Accent
	if base == "" {
		base = "#101018"
	}
	if accent == "" {
		accent = "#d58cff"
	}
	b, _ := strconv.ParseUint(base[1:], 16, 32)
	a, _ := strconv.ParseUint(accent[1:], 16, 32)
	var blended uint64
	for _, shift := range []uint{16, 8, 0} {
		component := (4*((b>>shift)&255) + ((a >> shift) & 255)) / 5
		blended |= component << shift
	}
	return fmt.Sprintf("#%06x", blended)
}

func Lookup(id string) (Palette, bool) {
	palette, ok := paletteByID[strings.ToLower(strings.TrimSpace(id))]
	return palette, ok
}

func All() []Palette { return append([]Palette(nil), palettes...) }

func ValidID(id string) bool {
	_, ok := Lookup(id)
	return ok
}

func foreground(hex string) string { return ansiColor(38, hex) }
func background(hex string) string { return ansiColor(48, hex) }

func ansiColor(kind int, hex string) string {
	parts, ok := colorParams(kind, hex)
	if !ok {
		return ""
	}
	return "\x1b[" + strings.Join(parts, ";") + "m"
}

func colorParams(kind int, hex string) ([]string, bool) {
	if len(hex) != 7 || hex[0] != '#' {
		return nil, false
	}
	r, errR := strconv.ParseInt(hex[1:3], 16, 0)
	g, errG := strconv.ParseInt(hex[3:5], 16, 0)
	b, errB := strconv.ParseInt(hex[5:7], 16, 0)
	if errR != nil || errG != nil || errB != nil {
		return nil, false
	}
	return []string{strconv.Itoa(kind), "2", strconv.FormatInt(r, 10), strconv.FormatInt(g, 10), strconv.FormatInt(b, 10)}, true
}

type colorRole string

const (
	roleBackground colorRole = "background"
	roleText       colorRole = "text"
	rolePrompt     colorRole = "prompt"
	roleMarkdown   colorRole = "markdown"
	roleStatus     colorRole = "status"
	roleDiff       colorRole = "diff"
	roleSuccess    colorRole = "success"
	roleWarning    colorRole = "warning"
	roleError      colorRole = "error"
	roleAccent     colorRole = "accent"
	roleMuted      colorRole = "muted"
	roleLink       colorRole = "link"
)

func (p Palette) color(role colorRole) string {
	switch role {
	case roleBackground:
		return p.Background
	case roleText:
		return p.Text
	case rolePrompt:
		return p.Prompt
	case roleMarkdown:
		return p.Markdown
	case roleStatus:
		return p.Status
	case roleDiff:
		return p.Diff
	case roleSuccess:
		return p.Success
	case roleWarning:
		return p.Warning
	case roleError:
		return p.Error
	case roleAccent:
		return p.Accent
	case roleMuted:
		return p.Muted
	case roleLink:
		return p.Link
	default:
		return ""
	}
}

func roleForRGB(hex string) (colorRole, bool) {
	for _, palette := range palettes[1:] {
		for _, role := range []colorRole{roleBackground, roleText, rolePrompt, roleMarkdown, roleStatus, roleDiff, roleSuccess, roleWarning, roleError, roleAccent, roleMuted, roleLink} {
			if strings.EqualFold(palette.color(role), hex) {
				return role, true
			}
		}
	}
	return "", false
}

func ansiRole(code int, background bool) colorRole {
	if background {
		switch code {
		case 40, 100:
			return roleBackground
		case 41, 101:
			return roleError
		case 42, 102:
			return roleSuccess
		case 43, 103:
			return roleWarning
		case 44, 104:
			return roleStatus
		case 45, 105:
			return roleAccent
		case 46, 106:
			return rolePrompt
		case 47, 107:
			return roleText
		}
		return ""
	}
	switch code {
	case 30, 97:
		return roleText
	case 37:
		return roleMarkdown
	case 31, 91:
		return roleError
	case 32, 92:
		return roleSuccess
	case 33:
		return roleWarning
	case 93:
		return roleDiff
	case 34, 94:
		return roleStatus
	case 35, 95:
		return roleAccent
	case 36:
		return rolePrompt
	case 96:
		return roleLink
	case 90:
		return roleMuted
	}
	return ""
}

// TransformANSI maps qcode's semantic ANSI colors to a palette. The default
// palette intentionally leaves every sequence untouched for terminal Auto.
func TransformANSI(input string, palette Palette) string {
	if palette.ID == "" || palette.ID == "default" {
		return input
	}
	var output strings.Builder
	for len(input) > 0 {
		start := strings.Index(input, "\x1b[")
		if start < 0 {
			output.WriteString(input)
			break
		}
		output.WriteString(input[:start])
		input = input[start:]
		end := -1
		for index := 2; index < len(input); index++ {
			if input[index] >= 0x40 && input[index] <= 0x7e {
				end = index
				break
			}
		}
		if end < 0 {
			output.WriteString(input)
			break
		}
		sequence := input[:end+1]
		if sequence[end] == 'm' {
			output.WriteString(transformSGR(sequence, palette))
		} else {
			output.WriteString(sequence)
		}
		input = input[end+1:]
	}
	return output.String()
}

func transformSGR(sequence string, palette Palette) string {
	parameters := sequence[2 : len(sequence)-1]
	if parameters == "" {
		return sequence
	}
	parts := strings.Split(parameters, ";")
	var output []string
	for index := 0; index < len(parts); index++ {
		value, err := strconv.Atoi(parts[index])
		if err != nil {
			output = append(output, parts[index])
		} else if (value == 38 || value == 48) && index+1 < len(parts) {
			kind, kindErr := strconv.Atoi(parts[index+1])
			if kindErr == nil && kind == 2 && index+4 < len(parts) {
				r, e1 := strconv.Atoi(parts[index+2])
				g, e2 := strconv.Atoi(parts[index+3])
				b, e3 := strconv.Atoi(parts[index+4])
				if e1 == nil && e2 == nil && e3 == nil {
					old := fmt.Sprintf("#%02x%02x%02x", r, g, b)
					if role, ok := roleForRGB(old); ok && palette.color(role) != "" {
						mapped, _ := colorParams(value, palette.color(role))
						output = append(output, mapped...)
						index += 4
						continue
					}
				}
				// Unknown RGB colors are one atomic parameter group. Its channel
				// values must not be interpreted as separate semantic ANSI codes.
				output = append(output, parts[index:index+5]...)
				index += 4
				continue
			} else if kindErr == nil && kind == 5 && index+2 < len(parts) {
				role := rolePrompt
				if value == 48 {
					role = roleBackground
				}
				if palette.color(role) != "" {
					mapped, _ := colorParams(value, palette.color(role))
					output = append(output, mapped...)
					index += 2
					continue
				}
			}
			output = append(output, parts[index])
		} else if role := ansiRole(value, false); role != "" {
			mapped, _ := colorParams(38, palette.color(role))
			output = append(output, mapped...)
		} else if role := ansiRole(value, true); role != "" {
			mapped, _ := colorParams(48, palette.color(role))
			output = append(output, mapped...)
		} else {
			output = append(output, parts[index])
		}
	}
	return "\x1b[" + strings.Join(output, ";") + "m"
}

// PaintRow fills a rendered row with the palette background. It is used only
// by the fixed terminal screen; terminal Auto keeps the emulator background.
func PaintRow(row string, width int, palette Palette) string {
	if palette.ID == "" || palette.ID == "default" || palette.Background == "" {
		return row
	}
	// Match the UI's single-cell tab width with painted spaces. Terminal tab
	// stops skip cells without coloring them and can exceed the measured width.
	row = strings.ReplaceAll(row, "\t", " ")
	row = TransformANSI(row, palette)
	background := background(palette.Background)
	foreground := foreground(palette.Text)
	base := "\x1b[0m" + background + foreground
	row = strings.ReplaceAll(row, "\x1b[0m", base)
	padding := max(0, width-visibleWidth(row))
	return background + foreground + row + strings.Repeat(" ", padding) + "\x1b[0m"
}

func visibleWidth(s string) int {
	return termtext.Width(s)
}
