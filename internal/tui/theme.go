package tui

import (
	"fmt"
	"io"
	"strconv"

	qtheme "qcode/internal/theme"
)

func (u *UI) chooseTheme() {
	if u.input == nil || u.terminal == nil {
		u.printSystemMessage(dim + "Theme selection is unavailable." + reset)
		return
	}
	u.input.setRaw(true)
	u.beginRawSelector()
	u.screenMu.Lock()
	viewport := u.commandViewHeightLocked()
	width := u.width
	u.screenMu.Unlock()
	// Minimum viable block is header + 1 option + 1 cursor row = 3 viewport
	// rows. Smaller viewports cannot fit even header + 1 option once the
	// trailing LF's cursor row is reserved, so bail out like the session
	// picker instead of scrolling the header out of view.
	if viewport < themePickerHeaderRows+2 {
		u.input.setRaw(false)
		u.endRawSelector()
		u.printSystemMessage(yellow + "Enlarge the terminal to pick a theme." + reset)
		return
	}
	// Rows are newline-terminated, so the last line's trailing LF is an IND
	// when written on the scroll region's bottom margin. Leave one row for
	// the cursor, mirroring loginHeight in remote_menu.go. Otherwise the
	// region scrolls and the header/first option are lost, and the next
	// clearSelector anchors one row too high.
	budget := max(1, viewport-1)
	visible := themePickerVisible(budget, len(qtheme.All()))
	selected, accepted, err := selectTheme(u.input, u.themedSelectorWriter(), qtheme.All(), u.currentTheme().ID, visible, width, budget, ColorEnabled(u.out))
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

const (
	themePickerHeaderRows  = 1
	themePickerGapRows     = 3
	themePickerPreviewRows = 7 // len(renderThemePreview)
	themePickerOverhead    = themePickerHeaderRows + themePickerGapRows + themePickerPreviewRows
)

// themePickerVisible caps the option window so header + options + gap +
// preview fit within budget, where budget is the max rows the picker may
// emit (viewport height minus one cursor row).
func themePickerVisible(budget, total int) int {
	maxVisible := budget - themePickerOverhead
	if maxVisible < 1 {
		maxVisible = 1
	}
	return min(total, maxVisible)
}

func selectTheme(in io.Reader, out io.Writer, options []qtheme.Palette, current string, visible, width, budget int, color bool) (string, bool, error) {
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
	rows := renderThemePicker(out, options, selected, start, visible, width, budget, color)
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
			rows = renderThemePicker(out, options, selected, start, visible, width, budget, color)
		}
	}
}

func renderThemePicker(out io.Writer, options []qtheme.Palette, selected, start, visible, width, budget int, color bool) int {
	if width < 1 {
		width = 80
	}
	heading := fmt.Sprintf("Terminal theme  %d/%d | live preview", selected+1, len(options))
	if width < 42 {
		heading = fmt.Sprintf("Theme %d/%d | Enter apply; Esc/Ctrl+C cancel", selected+1, len(options))
	}
	header := selectorHeader(heading, width)
	if width < 42 {
		header = truncateDiffLine(heading, width, false)
	}
	end := min(len(options), start+visible)
	// budget is the max rows the picker may emit (viewport height minus one
	// cursor row; see chooseTheme). Never emit more rows than fit, otherwise
	// the scroll region pushes the header and the first option out of view
	// on short terminals. The caller sizes the option window via
	// themePickerVisible, so only the preview and gap are trimmed here.
	//
	// Keep the preview anchored: short final pages are padded with blank
	// menu rows so the preview starts at the same row as a full page.
	optionRows := end - start
	padRows := max(0, visible-optionRows)
	layoutRows := optionRows + padRows
	fullPreview := renderThemePreview(options[selected], width, color)
	if budget > 0 {
		if themePickerHeaderRows+layoutRows+themePickerGapRows+len(fullPreview) > budget {
			remaining := budget - themePickerHeaderRows - layoutRows
			if remaining < 0 {
				// Caller bug: options alone exceed the budget. Emit header +
				// options without gap/preview rather than desyncing start.
				remaining = 0
			}
			gap := min(themePickerGapRows, remaining)
			previewKeep := remaining - gap
			preview := fullPreview
			if previewKeep < len(preview) {
				preview = preview[:max(0, previewKeep)]
			}
			lines := []string{header}
			for index := start; index < end; index++ {
				lines = append(lines, renderThemeOption(options[index], index == selected, width, color))
			}
			for range padRows {
				lines = append(lines, "")
			}
			for range gap {
				lines = append(lines, "")
			}
			lines = append(lines, preview...)
			for index, line := range lines {
				// Keep the sample in the candidate palette; menu rows use the active theme.
				if index < themePickerHeaderRows+layoutRows+gap {
					line = paintSelectorRow(out, line)
				}
				fmt.Fprintln(out, line)
			}
			return len(lines)
		}
	}
	lines := []string{header}
	for index := start; index < end; index++ {
		lines = append(lines, renderThemeOption(options[index], index == selected, width, color))
	}
	for range padRows {
		lines = append(lines, "")
	}
	for range themePickerGapRows {
		lines = append(lines, "")
	}
	lines = append(lines, fullPreview...)
	for index, line := range lines {
		// Keep the sample in the candidate palette; menu rows use the active theme.
		if index < themePickerHeaderRows+layoutRows+themePickerGapRows {
			line = paintSelectorRow(out, line)
		}
		fmt.Fprintln(out, line)
	}
	return len(lines)
}

func renderThemeOption(option qtheme.Palette, selected bool, width int, color bool) string {
	marker := "  "
	if selected {
		marker = "> "
	}
	name := option.Name + interfaceGlyph(UnicodeEnabled(), " · ", " | ") + option.Appearance
	if option.ID == "default" {
		name = "Default (Auto)"
	}
	label := marker + name
	label = truncateDiffLine(label, width, false)
	if color && selected {
		return cyan + bold + label + reset
	}
	return label
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
	previewTitle := "Preview Output"
	if color {
		previewTitle = cyan + bold + previewTitle + reset
	}
	lines := []string{
		previewTitle,
		"A response with " + roleColor(palette.Accent, magenta, "**bold text**") + " and " + roleColor(palette.Link, cyan, "a link") + ".",
		roleColor(palette.Prompt, cyan, "> A quoted passage from the answer."),
		roleColor(palette.Diff, yellow, "~ modified line"),
		roleColor(palette.Success, green, "+ added line") + "  " + roleColor(palette.Error, red, "- removed line"),
		roleColor(palette.Status, magenta, "[MODE PLAN]") + "  " + roleColor(palette.Prompt, cyan, "[CTX 73% left]"),
		roleColor(palette.Success, green, interfaceGlyph(UnicodeEnabled(), "✓", "+")+" success") + "  " + roleColor(palette.Warning, yellow, "! warning") + "  " + roleColor(palette.Error, red, interfaceGlyph(UnicodeEnabled(), "×", "x")+" error"),
	}
	if palette.ID != "default" && color {
		for index := 1; index < len(lines); index++ {
			lines[index] = qtheme.PaintRow(truncateDiffLine(lines[index], width, false), width, palette)
		}
	} else {
		for index := 1; index < len(lines); index++ {
			lines[index] = truncateDiffLine(lines[index], width, false)
		}
	}
	lines[0] = truncateDiffLine(lines[0], width, false)
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

func renderThemeStatusBarLine(line string, width int, palette qtheme.Palette, color bool) string {
	if !color {
		return line
	}
	return qtheme.PaintRow(line, width, palette)
}
