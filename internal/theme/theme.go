// Package theme defines terminal color palettes shared by configuration and
// the interactive terminal UI.
package theme

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
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
	{ID: "catppuccin-latte", Name: "Catppuccin Latte", Appearance: "Light", Background: "#eff1f5", Text: "#4c4f69", Prompt: "#1e66f5", Markdown: "#4c4f69", Status: "#8839ef", Diff: "#df8e1d", Success: "#40a02b", Warning: "#df8e1d", Error: "#d20f39", Accent: "#ea76cb", Muted: "#8c8fa1", Link: "#179299"},
	{ID: "alucard", Name: "Alucard", Appearance: "Light", Background: "#fffbeb", Text: "#1f1f1f", Prompt: "#644ac9", Markdown: "#1f1f1f", Status: "#a3144d", Diff: "#846e15", Success: "#14710a", Warning: "#846e15", Error: "#cb3a2a", Accent: "#a3144d", Muted: "#6c664b", Link: "#036a96"},
	{ID: "gruvbox-light", Name: "Gruvbox Light", Appearance: "Light", Background: "#fbf1c7", Text: "#3c3836", Prompt: "#427b58", Markdown: "#3c3836", Status: "#8f3f71", Diff: "#b57614", Success: "#79740e", Warning: "#b57614", Error: "#9d0006", Accent: "#8f3f71", Muted: "#928374", Link: "#427b58"},
	{ID: "solarized-light", Name: "Solarized Light", Appearance: "Light", Background: "#fdf6e3", Text: "#657b83", Prompt: "#268bd2", Markdown: "#657b83", Status: "#6c71c4", Diff: "#b58900", Success: "#859900", Warning: "#b58900", Error: "#dc322f", Accent: "#d33682", Muted: "#93a1a1", Link: "#2aa198"},
	{ID: "nord-light", Name: "Nord Light", Appearance: "Light", Background: "#eceff4", Text: "#2e3440", Prompt: "#5e81ac", Markdown: "#2e3440", Status: "#5e81ac", Diff: "#d08770", Success: "#a3be8c", Warning: "#ebcb8b", Error: "#bf616a", Accent: "#b48ead", Muted: "#7b88a1", Link: "#5e81ac"},
}

var paletteByID = func() map[string]Palette {
	result := make(map[string]Palette, len(palettes))
	for _, palette := range palettes {
		result[palette.ID] = palette
	}
	return result
}()

func Default() Palette { return palettes[0] }

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
	row = TransformANSI(row, palette)
	background := background(palette.Background)
	foreground := foreground(palette.Text)
	base := "\x1b[0m" + background + foreground
	row = strings.ReplaceAll(row, "\x1b[0m", base)
	padding := max(0, width-visibleWidth(row))
	return background + foreground + row + strings.Repeat(" ", padding) + "\x1b[0m"
}

func visibleWidth(s string) int {
	width := 0
	for len(s) > 0 {
		if strings.HasPrefix(s, "\x1b[") {
			end := strings.IndexByte(s, 'm')
			if end >= 0 {
				s = s[end+1:]
				continue
			}
		}
		if s[0] == '\x1b' && len(s) > 1 {
			s = s[2:]
			continue
		}
		r, size := utf8.DecodeRuneInString(s)
		width += runewidth.RuneWidth(r)
		s = s[size:]
	}
	return width
}
