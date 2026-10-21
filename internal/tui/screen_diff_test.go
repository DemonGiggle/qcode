package tui

import (
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"qcode/internal/session"
	qtheme "qcode/internal/theme"
)

func TestFixedRowDiffOverwritesChangedSpan(t *testing.T) {
	for _, tc := range []struct {
		name, previous, next, want string
		column                     int
	}{
		{"spinner", "Waiting (|) for model", "Waiting (/) for model", "/", 10},
		{"append", "> a", "> ab", "b", 4},
		{"shorten", "result: pending", "result: done", "done", 9},
		{"delete", "> abc", "> a", "", 4},
		{"wide prefix", "界 (|) ready", "界 (/) ready", "/", 5},
		{"combining prefix", "e\u0301 (|) ready", "e\u0301 (/) ready", "/", 4},
		{"emoji prefix", "👩‍💻 (|) ready", "👩‍💻 (/) ready", "/", 5},
		{"wide replacement", "a界z", "abbz", "bb", 2},
		{"narrow replacement", "abbz", "a界z", "界", 2},
		{"styled spinner", dim + "Waiting (|) for model" + reset, dim + "Waiting (/) for model" + reset, "/", 10},
		{"color only", "a\x1b[31mb" + reset + "c", "a\x1b[32mb" + reset + "c", "b", 2},
		{"combined SGR", "\x1b[1;31mabc", "\x1b[1;31mabd", "d", 3},
		{"RGB zero", "\x1b[38;2;0;128;0mabc", "\x1b[38;2;0;128;0mabd", "d", 3},
		{"tabs", "\t(|) ready", "\t(/) ready", "/", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			column, text := fixedRowDiff(tc.previous, tc.next, 80, false)
			if column != tc.column || plainHistoryText(text) != tc.want {
				t.Fatalf("diff = column %d, %q; want column %d, %q", column, text, tc.column, tc.want)
			}
			if strings.Contains(text, "\x1b[2K") {
				t.Fatal("update cleared the row before overwriting it")
			}
			if wantErase := visibleWidth(tc.next) < visibleWidth(tc.previous); strings.HasSuffix(text, "\x1b[K") != wantErase {
				t.Fatalf("tail erasure incorrect: %q", text)
			}
		})
	}
}

func TestFixedScreenDoesNotWriteUnchangedVisibleRows(t *testing.T) {
	u, _ := layoutFixture(t)
	u.renderInput(inputPrompt, "draft", 3)
	before, _ := u.out.Stat()
	rows := append([]string(nil), u.inputScreenRows...)
	rows[2] = reset // Same blank row, with a redundant trailing reset.
	u.writeFixedScreenLocked(rows, u.inputCursorRow, u.inputCursorColumn)
	after, _ := u.out.Stat()
	if before.Size() != after.Size() {
		t.Fatal("invisible style change caused terminal output")
	}
}

func TestFixedScreenProgressKeepsTranscriptAndCursor(t *testing.T) {
	u, _ := layoutFixture(t)
	now := time.Now()
	u.manager = &indicatorManager{layoutManager: u.manager.(*layoutManager),
		progress: &session.Progress{Summary: "Waiting for model response", StartedAt: now}}
	_, _ = io.WriteString(u.display, "unchanged transcript\n\rRunning shell (|)")
	u.renderInput(inputPrompt, "draft", 3)
	u.drawFixedTaskIndicatorLocked(now)
	before, _ := u.out.Stat()
	for i := 1; i < 10; i++ {
		_, _ = fmt.Fprintf(u.display, "\rRunning shell (%s)", []string{"|", "/", "-", "\\"}[i%4])
		u.drawFixedTaskIndicatorLocked(now.Add(time.Duration(i) * 100 * time.Millisecond))
	}
	_, _ = io.WriteString(u.display, "\r\x1b[2KDone\n")
	data, err := os.ReadFile(u.out.Name())
	if err != nil {
		t.Fatal(err)
	}
	update := string(data[before.Size():])
	for _, unwanted := range []string{"\x1b[2K", "\x1b[?25l", "\x1b[?25h", "unchanged transcript", "Running shell", "Ctrl+C"} {
		if strings.Contains(update, unwanted) {
			t.Fatalf("progress disturbed unchanged content with %q: %q", unwanted, update)
		}
	}
	screen := newFixedScreen(u.width, u.height)
	screen.feed(string(data))
	for row := 1; row <= u.height; row++ {
		got := strings.TrimRight(string(screen.rows[row-1]), " ")
		want := strings.TrimRight(plainHistoryText(u.inputScreenRows[row]), " ")
		if got != want {
			t.Fatalf("row %d = %q, want %q", row, got, want)
		}
	}
	if screen.y+1 != u.inputCursorRow || screen.x+1 != u.inputCursorColumn {
		t.Fatal("progress moved the input cursor")
	}
}

func TestFixedRowDiffKeepsThemeBackgroundAndStyles(t *testing.T) {
	palette, _ := qtheme.Lookup("catppuccin-mocha")
	previous := qtheme.PaintRow("\x1b[31mlong result"+reset, 40, palette)
	next := qtheme.PaintRow("\x1b[32mdone"+reset, 40, palette)
	column, text := fixedRowDiff(previous, next, 40, false)
	if column != 1 || !strings.Contains(text, "\x1b[48;2;30;30;46m") || !strings.Contains(text, "\x1b[38;2;166;227;161m") {
		t.Fatalf("themed overwrite lost its colors: %q", text)
	}
	if strings.Contains(text, "\x1b[K") || !strings.HasSuffix(plainHistoryText(text), strings.Repeat(" ", 7)) {
		t.Fatalf("shorter themed row did not overwrite its tail in the theme background: %q", text)
	}
}
