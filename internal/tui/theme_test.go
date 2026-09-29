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
	id, accepted, err := selectTheme(bytes.NewBufferString(arrowDownSequence+"\r"), &output, qtheme.All(), "default", len(qtheme.All()), 90, 24, true)
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

func TestThemePickerPlacesPreviewBelowOptionsAndColorsOptions(t *testing.T) {
	var output bytes.Buffer
	options := qtheme.All()
	rows := renderThemePicker(&output, options, 0, 0, len(options), 90, 24, true)
	lines := strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n")
	if len(lines) != rows {
		t.Fatalf("rendered %d rows, want %d", len(lines), rows)
	}
	lastOption := -1
	preview := -1
	for index, line := range lines {
		if strings.Contains(line, "Nord Light · Light") {
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
	if !strings.Contains(lines[2], "\x1b[48;2;30;30;46m") {
		t.Fatalf("unselected Catppuccin Mocha option is missing its palette background: %q", lines[2])
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
