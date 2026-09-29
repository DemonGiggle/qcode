package tui

import (
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"

	qtheme "qcode/internal/theme"
)

func (u *UI) chooseTheme() {
	if u.input == nil || u.terminal == nil {
		u.printSystemMessage(dim + "Theme selection is unavailable." + reset)
		return
	}
	u.printSystemMessage(dim + "Preview with Up/Down. Enter applies and saves; Esc or Ctrl+C cancels." + reset)
	u.input.setRaw(true)
	u.beginRawSelector()
	visibleRows := u.height - 8
	if u.width < 74 {
		visibleRows = u.height - 14
	}
	visible := min(len(qtheme.All()), max(3, visibleRows))
	selected, accepted, err := selectTheme(u.input, u.terminal, qtheme.All(), u.currentTheme().ID, visible, u.width, u.height, ColorEnabled(u.out))
	u.input.setRaw(false)
	u.endRawSelector()
	if err != nil {
		return
	}
	if !accepted {
		u.printSystemMessage(dim + "Theme selection cancelled." + reset)
		return
	}
	if err := u.SetTheme(selected); err != nil {
		u.printSystemMessage(yellow + "Unable to apply theme: " + sanitizeDiffLine(err.Error(), "<ESC>") + reset)
		return
	}
	u.persistThemePreference(selected)
	palette, _ := qtheme.Lookup(selected)
	u.printSystemMessage(green + "Theme: " + palette.Name + " (" + palette.Appearance + ")." + reset)
}

func (u *UI) persistThemePreference(id string) {
	u.screenMu.Lock()
	writer := u.runtimePreferences
	u.screenMu.Unlock()
	if writer == nil {
		return
	}
	if err := writer.PersistTheme(id); err != nil {
		u.printSystemMessage(yellow + "Warning: unable to persist theme preference: " + sanitizeDiffLine(err.Error(), "<ESC>") + reset)
	}
}

func selectTheme(in io.Reader, out io.Writer, options []qtheme.Palette, current string, visible, width, height int, color bool) (string, bool, error) {
	if len(options) == 0 {
		return "", false, nil
	}
	selected := 0
	for index, option := range options {
		if option.ID == current {
			selected = index
			break
		}
	}
	visible = selectorVisible(len(options), visible)
	start := selectorInitialStart(selected, len(options), visible)
	rows := renderThemePicker(out, options, selected, start, visible, width, height, color)
	for {
		key, err := readSelectorKey(in)
		if err != nil {
			clearSelector(out, rows)
			return "", false, err
		}
		switch key {
		case "\r", "\n":
			clearSelector(out, rows)
			return options[selected].ID, true, nil
		case string([]byte{ctrlC}), "\x1b":
			clearSelector(out, rows)
			return "", false, nil
		case arrowUpSequence, arrowDownSequence, selectorPageUp, selectorPageDown:
			old := selected
			switch key {
			case arrowUpSequence:
				selected = (selected - 1 + len(options)) % len(options)
			case arrowDownSequence:
				selected = (selected + 1) % len(options)
			case selectorPageUp:
				selected = max(0, selected-visible)
			case selectorPageDown:
				selected = min(len(options)-1, selected+visible)
			}
			if selected == old {
				continue
			}
			start = selectorStart(selected, len(options), visible, start)
			clearSelector(out, rows)
			rows = renderThemePicker(out, options, selected, start, visible, width, height, color)
		}
	}
}

func renderThemePicker(out io.Writer, options []qtheme.Palette, selected, start, visible, width, height int, color bool) int {
	if width < 1 {
		width = 80
	}
	heading := fmt.Sprintf("Terminal theme  %d/%d | live preview", selected+1, len(options))
	if width < 42 {
		heading = fmt.Sprintf("Theme %d/%d | Enter apply; Esc/Ctrl+C cancel", selected+1, len(options))
		lines := []string{truncateDiffLine(heading, width, false)}
		lines = append(lines, renderThemePickerBody(options, selected, start, visible, width, height, color)...)
		for _, line := range lines {
			fmt.Fprintln(out, line)
		}
		return len(lines)
	}
	lines := []string{selectorHeader(heading, width)}
	end := min(len(options), start+visible)
	if width >= 74 {
		listWidth := min(31, max(24, width/3))
		previewWidth := max(1, width-listWidth-3)
		preview := renderThemePreview(options[selected], previewWidth, color)
		rows := max(visible, len(preview))
		for row := 0; row < rows; row++ {
			left := strings.Repeat(" ", listWidth)
			if row < visible && start+row < len(options) {
				left = renderThemeOption(options[start+row], start+row == selected, listWidth, color)
			}
			right := ""
			if row < len(preview) {
				right = preview[row]
			}
			lines = append(lines, left+"   "+right)
		}
	} else {
		for index := start; index < end; index++ {
			lines = append(lines, renderThemeOption(options[index], index == selected, width, color))
		}
		lines = append(lines, renderThemePreview(options[selected], width, color)...)
	}
	for _, line := range lines {
		fmt.Fprintln(out, line)
	}
	return len(lines)
}

func renderThemePickerBody(options []qtheme.Palette, selected, start, visible, width, height int, color bool) []string {
	end := min(len(options), start+visible)
	lines := make([]string, 0, visible+7)
	for index := start; index < end; index++ {
		lines = append(lines, renderThemeOption(options[index], index == selected, width, color))
	}
	lines = append(lines, renderThemePreview(options[selected], width, color)...)
	return lines
}

func renderThemeOption(option qtheme.Palette, selected bool, width int, color bool) string {
	marker := "  "
	if selected {
		marker = "> "
	}
	name := option.Name + " · " + option.Appearance
	if option.ID == "default" {
		name = "Default (Auto)"
	}
	label := marker + name
	if !color {
		return truncateDiffLine(label, width, false)
	}
	if selected && option.ID != "default" {
		foreground := option.Background
		if contrastRatio(option.Text, option.Accent) > contrastRatio(option.Background, option.Accent) {
			foreground = option.Text
		}
		label = rgbSGR(48, option.Accent) + rgbSGR(38, foreground) + label + reset
	} else if selected {
		label = cyan + bold + label + reset
	} else {
		label = rgbSGR(38, option.Text) + label + reset
	}
	return truncateDiffLine(label, width, false)
}

func renderThemePreview(palette qtheme.Palette, width int, color bool) []string {
	if width < 1 {
		width = 80
	}
	defaultColor := func(code, text string) string {
		if !color {
			return text
		}
		return code + text + reset
	}
	roleColor := func(hex, fallback, text string) string {
		if !color {
			return text
		}
		if palette.ID == "default" {
			return defaultColor(fallback, text)
		}
		return rgbSGR(38, hex) + text + reset
	}
	lines := []string{
		roleColor(palette.Markdown, cyan, "# Markdown preview"),
		"A response with " + roleColor(palette.Accent, magenta, "**bold text**") + " and " + roleColor(palette.Link, cyan, "a link") + ".",
		roleColor(palette.Prompt, cyan, "> A quoted passage from the answer."),
		roleColor(palette.Diff, yellow, "~ modified line"),
		roleColor(palette.Success, green, "+ added line") + "  " + roleColor(palette.Error, red, "- removed line"),
		roleColor(palette.Status, magenta, "[MODE PLAN]") + "  " + roleColor(palette.Prompt, cyan, "[CTX 73% left]"),
		roleColor(palette.Success, green, "✓ success") + "  " + roleColor(palette.Warning, yellow, "! warning") + "  " + roleColor(palette.Error, red, "× error"),
	}
	if palette.ID != "default" && color {
		for index := range lines {
			lines[index] = qtheme.PaintRow(truncateDiffLine(lines[index], width, false), width, palette)
		}
	} else {
		for index := range lines {
			lines[index] = truncateDiffLine(lines[index], width, false)
		}
	}
	return lines
}

func rgbSGR(kind int, hex string) string {
	if len(hex) != 7 || hex[0] != '#' {
		return ""
	}
	r, errR := strconv.ParseInt(hex[1:3], 16, 0)
	g, errG := strconv.ParseInt(hex[3:5], 16, 0)
	b, errB := strconv.ParseInt(hex[5:7], 16, 0)
	if errR != nil || errG != nil || errB != nil {
		return ""
	}
	return fmt.Sprintf("\x1b[%d;2;%d;%d;%dm", kind, r, g, b)
}

func contrastRatio(first, second string) float64 {
	linear := func(hex string) float64 {
		if len(hex) != 7 || hex[0] != '#' {
			return 0
		}
		channels := [3]float64{}
		for index, pair := range []string{hex[1:3], hex[3:5], hex[5:7]} {
			value, err := strconv.ParseInt(pair, 16, 0)
			if err != nil {
				return 0
			}
			component := float64(value) / 255
			if component <= 0.04045 {
				channels[index] = component / 12.92
			} else {
				channels[index] = math.Pow((component+0.055)/1.055, 2.4)
			}
		}
		return 0.2126*channels[0] + 0.7152*channels[1] + 0.0722*channels[2]
	}
	a, b := linear(first), linear(second)
	if a < b {
		a, b = b, a
	}
	return (a + 0.05) / (b + 0.05)
}
