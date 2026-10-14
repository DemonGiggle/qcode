package tui

import (
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"

	qtheme "qcode/internal/theme"
)

func TestHistoryRowCacheTracksStreamingAndLayout(t *testing.T) {
	h := newHistoryWriter(io.Discard)
	var cache historyRowCache
	width, palette := 20, qtheme.Default()
	check := func() {
		t.Helper()
		snapshot := h.Snapshot()
		got := cache.render(snapshot, width, palette)
		want := historyRowsForTheme(snapshot, width, palette)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("cached rows differ at width %d, theme %s:\ngot  %+v\nwant %+v", width, palette.ID, got, want)
		}
	}
	write := func(text string) {
		t.Helper()
		_, _ = io.WriteString(h, text)
		check()
	}
	check()
	write("\x1b[36mstyled 日本語 👩‍💻 output that wraps across multiple rows\x1b[0m\n")
	write("   12) numbered question with a long wrapped option\n")
	write("unfinished")
	write(" line")
	write("\r\x1b[2Kreplacement")
	write("\rX")      // Cursor rewrites can retain text beyond the cursor.
	write("\n\x1b[3") // Incomplete ANSI input must not enter the cached row.
	write("1mred\x1b[0m\n")
	width = 9
	check()
	palette, _ = qtheme.Lookup("dracula")
	check()
	palette.Error = "#112233" // Key on palette values, not just the ID.
	check()
	width, palette = 80, qtheme.Default()
	check()
	h.Clear()
	check()
	write("after clear\n")
	// Restoring a different history may reuse IDs; source text must match too.
	h = newHistoryWriter(io.Discard)
	write("different session\n")
}

func TestHistoryRowCacheReusesRowsWithoutAllocating(t *testing.T) {
	snapshot := historySnapshot{lines: []historyLine{
		{0, "\x1b[36m" + strings.Repeat("styled 日本語 output ", 10) + reset},
		{1, "partial"},
	}}
	var cache historyRowCache
	rows := cache.render(snapshot, 20, qtheme.Default())
	if allocs := testing.AllocsPerRun(10, func() {
		cache.render(snapshot, 20, qtheme.Default())
	}); allocs != 0 {
		t.Fatalf("unchanged history allocated %g objects per repaint", allocs)
	}
	// Rewriting the unfinished line must preserve the wrapped committed prefix.
	first := &rows[0]
	snapshot.lines[1].text = "rewritten"
	got := cache.render(snapshot, 20, qtheme.Default())
	if &got[0] != first || !reflect.DeepEqual(got, historyRowsForTheme(snapshot, 20, qtheme.Default())) {
		t.Fatal("unfinished-line rewrite rebuilt or changed the committed prefix")
	}
}

func TestHistoryRowCacheTrimsRetainedHistory(t *testing.T) {
	h := newHistoryWriter(io.Discard)
	for i := 0; i < maxHistoryLines; i++ {
		h.AddLine(fmt.Sprintf("line %d with 日本語 that wraps across several rows", i))
	}
	var cache historyRowCache
	cache.render(h.Snapshot(), 20, qtheme.Default())
	h.AddLine("triggers retention trim")
	snapshot := h.Snapshot()
	got := cache.render(snapshot, 20, qtheme.Default())
	if got[0].position.line != uint64(maxHistoryLines/5) || !reflect.DeepEqual(got, historyRowsForTheme(snapshot, 20, qtheme.Default())) {
		t.Fatal("retention trimming lost surviving rows or kept evicted rows")
	}
	h.Clear()
	cache.render(h.Snapshot(), 20, qtheme.Default())
	if len(cache.lines) != 1 || len(cache.rows) != 1 {
		t.Fatal("clear retained old history in the cache")
	}
}

func TestTabSwitchPreservesCachedPausedHistory(t *testing.T) {
	u, _ := persistenceUI(t)
	u.fixedInput = true
	for i := 0; i < 60; i++ {
		u.views["main"].display.AddLine(fmt.Sprintf("main line %d with 日本語", i))
		u.views["agent-1"].display.AddLine(fmt.Sprintf("worker line %d", i))
	}
	if err := u.switchAgent("main"); err != nil {
		t.Fatal(err)
	}
	u.showPage(1)
	main := u.views["main"]
	anchor, first := main.viewport.anchor, &main.rowCache.rows[0]
	before := strings.Join(u.inputScreenRows[2:u.inputTaskRow-1], "\n")
	if err := u.switchAgent("agent-1"); err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(main.display, "background main output\n")
	if err := u.switchAgent("main"); err != nil {
		t.Fatal(err)
	}
	if main.viewport.anchor != anchor || !main.viewport.browsing || &main.rowCache.rows[0] != first {
		t.Fatal("tab switch moved the reading position or rebuilt unchanged history")
	}
	if got := strings.Join(u.inputScreenRows[2:u.inputTaskRow-1], "\n"); got != before {
		t.Fatalf("returning to a paused tab changed the visible transcript:\n%s", got)
	}
}
