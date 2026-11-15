package tui

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	qtheme "qcode/internal/theme"
)

func TestSelectThemeUpdatesLivePreviewAndAccepts(t *testing.T) {
	var output bytes.Buffer
	id, accepted, err := selectTheme(bytes.NewBufferString(arrowDownSequence+"\r"), &output, qtheme.All(), "default", len(qtheme.All()), 90, 40, true)
	if err != nil || !accepted || id != "catppuccin-mocha" {
		t.Fatalf("selection = (%q, %v, %v)", id, accepted, err)
	}
	if !strings.Contains(output.String(), "\x1b[48;2;30;30;46m") {
		t.Fatalf("updated preview did not render Catppuccin Mocha: %q", output.String())
	}
}

func TestSelectThemeCancelsWithEscapeOrCtrlC(t *testing.T) {
	for _, key := range []string{"\x1b", string([]byte{ctrlC})} {
		var output bytes.Buffer
		id, accepted, err := selectTheme(bytes.NewBufferString(key), &output, qtheme.All(), "dracula", 8, 80, 24, false)
		if err != nil || accepted || id != "" {
			t.Errorf("cancel key %q = (%q, %v, %v)", key, id, accepted, err)
		}
	}
}

func TestThemePickerNarrowLayoutFitsTerminalWidth(t *testing.T) {
	var output bytes.Buffer
	options := qtheme.All()
	rows := renderThemePicker(&output, options, 6, 4, 4, 28, 18, true)
	lines := strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n")
	if len(lines) != rows {
		t.Fatalf("rendered %d rows, want %d", len(lines), rows)
	}
	for index, line := range lines {
		if width := visibleWidth(line); width > 28 {
			t.Errorf("row %d width = %d: %q", index, width, line)
		}
	}
	if !strings.Contains(output.String(), "Preview Output") {
		t.Fatal("narrow layout omitted its preview")
	}
}

func TestThemePickerFitsShortViewport(t *testing.T) {
	options := qtheme.All()
	for _, viewport := range []int{3, 4, 11, 13, 15} {
		budget := max(1, viewport-1)
		visible := themePickerVisible(budget, len(options))
		var output bytes.Buffer
		rows := renderThemePicker(&output, options, 0, 0, visible, 80, budget, false)
		if rows > budget {
			t.Fatalf("viewport %d: rendered %d rows, want at most budget %d", viewport, rows, budget)
		}
		if rows >= viewport {
			t.Fatalf("viewport %d: rendered %d rows, must leave one cursor row", viewport, rows)
		}
		lines := strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n")
		if len(lines) != rows {
			t.Fatalf("viewport %d: rendered %d rows, want %d", viewport, len(lines), rows)
		}
		if !strings.Contains(lines[0], "Terminal theme  1/") {
			t.Fatalf("viewport %d: header scrolled out of view: %q", viewport, lines[0])
		}
		if !strings.Contains(lines[1], "Default (Auto)") {
			t.Fatalf("viewport %d: first option missing: %q", viewport, lines[1])
		}
	}
}

func TestThemePickerPreviewAnchoredAcrossPages(t *testing.T) {
	options := qtheme.All()
	visible := 8
	budget := themePickerHeaderRows + visible + themePickerGapRows + themePickerPreviewRows
	firstStart := selectorStart(0, len(options), visible, 0)
	lastSelected := len(options) - 1
	lastStart := selectorStart(lastSelected, len(options), visible, 0)
	if lastStart == firstStart {
		t.Fatalf("need multiple pages for anchoring test: visible=%d total=%d", visible, len(options))
	}
	if len(options)%visible == 0 {
		t.Fatalf("anchoring test needs a short final page: total=%d visible=%d", len(options), visible)
	}
	var firstOut, lastOut bytes.Buffer
	firstRows := renderThemePicker(&firstOut, options, 0, firstStart, visible, 80, budget, false)
	lastRows := renderThemePicker(&lastOut, options, lastSelected, lastStart, visible, 80, budget, false)
	if firstRows != lastRows {
		t.Fatalf("row count moved: first=%d last=%d", firstRows, lastRows)
	}
	firstLines := strings.Split(strings.TrimSuffix(firstOut.String(), "\n"), "\n")
	lastLines := strings.Split(strings.TrimSuffix(lastOut.String(), "\n"), "\n")
	previewIndex := func(lines []string) int {
		for i, line := range lines {
			if strings.Contains(line, "Preview Output") {
				return i
			}
		}
		return -1
	}
	if got, want := previewIndex(firstLines), previewIndex(lastLines); got < 0 || got != want {
		t.Fatalf("preview moved: first page at %d, last page at %d", got, want)
	}
	// Trimmed path: same visible window with a smaller budget must keep the
	// preview at the same row, only showing fewer preview lines.
	trimmedBudget := themePickerHeaderRows + visible + 4
	var firstTrimOut, lastTrimOut bytes.Buffer
	firstTrimRows := renderThemePicker(&firstTrimOut, options, 0, firstStart, visible, 80, trimmedBudget, false)
	lastTrimRows := renderThemePicker(&lastTrimOut, options, lastSelected, lastStart, visible, 80, trimmedBudget, false)
	if firstTrimRows != lastTrimRows {
		t.Fatalf("trimmed row count moved: first=%d last=%d", firstTrimRows, lastTrimRows)
	}
	firstTrimLines := strings.Split(strings.TrimSuffix(firstTrimOut.String(), "\n"), "\n")
	lastTrimLines := strings.Split(strings.TrimSuffix(lastTrimOut.String(), "\n"), "\n")
	if got, want := previewIndex(firstTrimLines), previewIndex(lastTrimLines); got < 0 || got != want {
		t.Fatalf("trimmed preview moved: first page at %d, last page at %d", got, want)
	}
	if got, want := previewIndex(firstTrimLines), previewIndex(firstLines); got != want {
		t.Fatalf("trimmed preview row %d != untrimmed row %d", got, want)
	}
}

func TestThemePickerPlacesPreviewBelowOptionsAndUsesStandardOptionColors(t *testing.T) {
	var output bytes.Buffer
	options := qtheme.All()
	rows := renderThemePicker(&output, options, 0, 0, len(options), 90, 40, true)
	lines := strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n")
	if len(lines) != rows {
		t.Fatalf("rendered %d rows, want %d", len(lines), rows)
	}
	lastOption := -1
	preview := -1
	for index, line := range lines {
		if strings.Contains(line, options[len(options)-1].Name+" · "+options[len(options)-1].Appearance) {
			lastOption = index
		}
		if strings.Contains(line, "Preview Output") {
			preview = index
		}
	}
	if lastOption < 0 || preview < 0 || preview <= lastOption {
		t.Fatalf("preview must follow all theme options (last option row %d, preview row %d): %q", lastOption, preview, lines)
	}
	if preview-lastOption != 4 || lines[lastOption+1] != "" || lines[lastOption+2] != "" || lines[lastOption+3] != "" {
		t.Fatalf("preview should have three blank rows after the theme options: %q", lines[lastOption+1:preview])
	}
	if !strings.Contains(lines[1], cyan+bold) || strings.Contains(lines[1], "48;2;") {
		t.Fatalf("selected option should use the standard selector highlight: %q", lines[1])
	}
	if got, want := lines[2], "  Catppuccin Mocha · Dark"; got != want {
		t.Fatalf("unselected option should use the normal terminal color: got %q, want %q", got, want)
	}
	for _, line := range lines[1 : 1+len(options)] {
		if strings.Contains(line, "38;2;") || strings.Contains(line, "48;2;") {
			t.Errorf("theme-specific RGB color appeared in option list: %q", line)
		}
	}
}

func TestThemePreviewHeadingColorStaysFixedAcrossPalettes(t *testing.T) {
	palettes := []qtheme.Palette{qtheme.Default()}
	for _, id := range []string{"catppuccin-mocha", "catppuccin-latte", "dracula"} {
		palette, ok := qtheme.Lookup(id)
		if !ok {
			t.Fatalf("missing test palette %q", id)
		}
		palettes = append(palettes, palette)
	}
	want := "\x1b[36m\x1b[1mPreview Output\x1b[0m"
	for _, palette := range palettes {
		lines := renderThemePreview(palette, 80, true)
		if lines[0] != want {
			t.Errorf("%s preview heading = %q, want fixed color %q", palette.ID, lines[0], want)
		}
	}
}

func TestThemeStatusBarPaintsBackgroundAcrossEachLine(t *testing.T) {
	const width = 80
	bar := statusBar("ollama", "qwen", "/workspace", width, true, true, "73% left", "I:1 O:2", "2/32")
	for _, id := range []string{"catppuccin-mocha", "catppuccin-latte"} {
		palette, ok := qtheme.Lookup(id)
		if !ok {
			t.Fatalf("missing test palette %q", id)
		}
		lines := strings.Split(bar, "\n")
		for index, line := range lines {
			painted := renderThemeStatusBarLine(line, width, palette, true)
			if gotWidth := visibleWidth(painted); gotWidth != width {
				t.Errorf("%s status line %d width = %d, want %d", id, index, gotWidth, width)
			}
			if !strings.Contains(painted, rgbSGR(48, palette.Background)) {
				t.Errorf("%s status line %d has no themed background: %q", id, index, painted)
			}
		}
	}
}

func TestThemeStatusBarKeepsAutoAndNoColorBehavior(t *testing.T) {
	bar := statusBar("ollama", "qwen", "/workspace", 80, true, true)
	auto := renderThemeStatusBarLine(bar, 80, qtheme.Default(), true)
	if auto != bar {
		t.Fatalf("Auto changed status bar styling: got %q, want %q", auto, bar)
	}
	palette, _ := qtheme.Lookup("dracula")
	plain := "status bar"
	if got := renderThemeStatusBarLine(plain, 80, palette, false); got != plain {
		t.Fatalf("color-disabled status bar changed: %q", got)
	}
}

func TestThemeRecolorsRetainedHistoryForEveryAgentView(t *testing.T) {
	write := func(text string) *historyWriter {
		history := newHistoryWriter(io.Discard)
		if _, err := history.Write([]byte(cyan + text + reset + "\n")); err != nil {
			t.Fatal(err)
		}
		return history
	}
	views := []*historyWriter{write("main prompt"), write("worker prompt")}
	palette, _ := qtheme.Lookup("catppuccin-latte")
	for index, history := range views {
		rows := historyRowsForTheme(history.Snapshot(), 80, palette)
		if len(rows) == 0 || !strings.Contains(rows[0].text, "\x1b[38;2;30;102;245m") {
			t.Errorf("view %d was not recolored: %+v", index, rows)
		}
	}
	defaultRows := historyRowsForTheme(views[0].Snapshot(), 80, qtheme.Default())
	if len(defaultRows) == 0 || !strings.Contains(defaultRows[0].text, cyan) {
		t.Fatalf("Auto did not preserve the saved ANSI color: %+v", defaultRows)
	}
}

func TestThemePreferencePersistsFromChildAndReportsErrors(t *testing.T) {
	var output bytes.Buffer
	recorder := &runtimePreferenceRecorder{themeErr: os.ErrPermission}
	u := &UI{display: newHistoryWriter(&output), runtimePreferences: recorder, activeAgent: "agent-2"}
	if err := u.SetTheme("dracula"); err != nil {
		t.Fatal(err)
	}
	u.persistThemePreference("dracula")
	if recorder.themeCalls != 1 || recorder.theme != "dracula" {
		t.Fatalf("theme persistence = %+v", recorder)
	}
	if u.currentTheme().ID != "dracula" {
		t.Fatalf("active theme reverted after persistence failure: %q", u.currentTheme().ID)
	}
	if !strings.Contains(output.String(), "Warning: unable to persist theme preference") {
		t.Fatalf("save failure was not reported: %q", output.String())
	}
}

func TestThemeOutputStaysColorlessWithoutTTY(t *testing.T) {
	out, err := os.CreateTemp(t.TempDir(), "not-a-terminal")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	u := &UI{out: out, themePalette: qtheme.Default()}
	if err := u.SetTheme("dracula"); err != nil {
		t.Fatal(err)
	}
	input := cyan + "prompt" + reset
	if got := u.themeOutput(input); got != input {
		t.Fatalf("non-TTY output changed: %q", got)
	}
	if got := u.outputTheme().ID; got != "default" {
		t.Fatalf("non-TTY history palette = %q, want Auto", got)
	}
	history := newHistoryWriter(io.Discard)
	_, _ = history.Write([]byte(input + "\n"))
	rows := historyRowsForTheme(history.Snapshot(), 80, u.outputTheme())
	if len(rows) == 0 || !strings.Contains(rows[0].text, cyan) || strings.Contains(rows[0].text, "38;2") {
		t.Fatalf("non-TTY history gained theme color: %+v", rows)
	}
}
