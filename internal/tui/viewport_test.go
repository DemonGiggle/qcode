package tui

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"

	"qcode/internal/lineedit"
	"qcode/internal/session"
)

func TestViewportPagesOverlapTwoRenderedRows(t *testing.T) {
	for _, wrapped := range []bool{false, true} {
		t.Run(fmt.Sprintf("wrapped=%v", wrapped), func(t *testing.T) {
			h := newHistoryWriter(io.Discard)
			if wrapped {
				// All rows belong to one styled logical line, so position columns
				// distinguish overlapping rows even though their text is identical.
				h.AddLine("\x1b[31m" + strings.Repeat("界", 200) + "\x1b[0m")
			} else {
				for i := 0; i < 40; i++ {
					h.AddLine(fmt.Sprintf("line %d", i))
				}
			}
			rows := historyRows(h.Snapshot(), 10)
			v := viewport{}
			previous := v.page(rows, 10, 0)
			original := previous[0].position
			for _, direction := range []int{1, 1, -1, -1} {
				page := v.page(rows, 10, direction)
				oldOverlap, newOverlap := previous[:2], page[len(page)-2:]
				if direction < 0 {
					oldOverlap, newOverlap = previous[len(previous)-2:], page[:2]
				}
				for i := range oldOverlap {
					if oldOverlap[i] != newOverlap[i] {
						t.Fatalf("direction %d: overlap = %v, want %v", direction, newOverlap, oldOverlap)
					}
				}
				previous = page
			}
			if previous[0].position != original || v.browsing {
				t.Fatalf("round trip did not restore live page: viewport=%+v page=%v", v, previous)
			}
		})
	}
}

func TestViewportPagingSmallViewsAndBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name      string
		count     int
		size      int
		wantStart int
	}{
		{"empty", 0, 5, -1},
		{"short history", 3, 5, 0},
		{"exact page", 5, 5, 0},
		{"one row", 30, 1, 28},
		{"two rows", 30, 2, 27},
		{"three rows", 30, 3, 26},
		{"five rows", 30, 5, 22},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := make([]historyRow, tc.count)
			for i := range rows {
				rows[i] = historyRow{position: historyPosition{line: uint64(i + 1)}, text: fmt.Sprint(i)}
			}
			v := viewport{}
			page := v.page(rows, tc.size, 1)
			if tc.count == 0 {
				if len(page) != 0 || v.browsing {
					t.Fatalf("empty history: page=%v viewport=%+v", page, v)
				}
				return
			}
			if len(page) != min(tc.size, tc.count) || page[0] != rows[tc.wantStart] {
				t.Fatalf("first PageUp = %v, want start %v", page, rows[tc.wantStart])
			}
			for i := 0; i <= tc.count; i++ {
				page = v.page(rows, tc.size, 1)
			}
			if page[0] != rows[0] {
				t.Fatalf("top boundary = %v", page)
			}
			for i := 0; i <= tc.count; i++ {
				page = v.page(rows, tc.size, -1)
			}
			if v.browsing || page[0] != rows[max(0, tc.count-tc.size)] {
				t.Fatalf("bottom boundary: page=%v viewport=%+v", page, v)
			}
		})
	}
}

func TestTranscriptLayoutsUseTwoRowPageOverlap(t *testing.T) {
	for _, fixed := range []bool{false, true} {
		t.Run(fmt.Sprintf("fixed=%v", fixed), func(t *testing.T) {
			u, out := pagingTestUI(t)
			u.fixedInput = fixed
			for i := 0; i < 40; i++ {
				u.display.AddLine(fmt.Sprintf("line %d", i))
			}
			u.renderInput(inputPrompt, "draft", 5)
			size := u.height - 3 - u.statusLinesLocked()
			if fixed {
				// The fixture has a single input row and no queue or candidates.
				size = u.inputCursorRow - 2
			}
			if size <= 2 {
				t.Fatalf("fixture has insufficient transcript rows: %d", size)
			}
			rows := historyRows(u.display.Snapshot(), u.width)
			tail := len(rows) - size
			before := fileSize(t, out)
			u.showPage(1)
			want := rows[tail-size+2]
			if !u.activeViewportLocked().browsing || u.activeViewportLocked().anchor != want.position {
				t.Fatalf("PageUp viewport = %+v, want anchor %v", u.activeViewportLocked(), want.position)
			}
			if fixed {
				if u.inputScreenRows[2] != want.text {
					t.Fatalf("top screen row = %q, want %q", u.inputScreenRows[2], want.text)
				}
			} else {
				data, err := os.ReadFile(out.Name())
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(data[before:]), "\x1b[2;1H\x1b[J"+want.text) {
					t.Fatalf("PageUp did not render expected top row %q", want.text)
				}
			}
			u.showPage(-1)
			if u.activeViewportLocked().browsing {
				t.Fatal("PageDown did not restore live output")
			}
		})
	}
}

func TestHistoryBoundaryJumpsPreserveDraftAndResumeLiveOutput(t *testing.T) {
	for _, fixed := range []bool{false, true} {
		t.Run(fmt.Sprintf("fixed=%v", fixed), func(t *testing.T) {
			u, out := pagingTestUI(t)
			u.fixedInput = fixed
			u.AddAgentView("main", "demo", "demo")
			background, _ := u.AddAgentView("agent-1", "demo", "demo")
			for i := 0; i < 40; i++ {
				fmt.Fprintf(u.display, "line %d\n", i)
			}
			u.renderInput(inputPrompt, "draft", 3)
			u.showPage(1)
			u.input.route([]byte("\x1b[H"))
			v := u.activeViewportLocked()
			oldest := historyRows(u.display.Snapshot(), u.width)[0]
			if !v.browsing || v.anchor != oldest.position {
				t.Fatalf("Home viewport = %+v, want oldest position %v", v, oldest.position)
			}
			before := fileSize(t, out)
			_, _ = io.WriteString(u.display, "latest streamed output\n")
			_, _ = io.WriteString(background, "background output\n")
			if v.anchor != oldest.position || !v.browsing {
				t.Fatal("streaming moved the Home position")
			}
			u.input.route([]byte("\x1b[F"))
			if v.browsing {
				t.Fatal("End did not resume live output")
			}
			data, err := os.ReadFile(out.Name())
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data[before:]), "latest streamed output") || strings.Contains(string(data[before:]), "background output") {
				t.Fatal("End did not restore the active tab's latest output")
			}
			if u.inputText != "draft" || u.inputPosition != 3 {
				t.Fatalf("history jump changed draft: text=%q position=%d", u.inputText, u.inputPosition)
			}
			before = fileSize(t, out)
			_, _ = io.WriteString(u.display, "live continuation\n")
			if fileSize(t, out) <= before {
				t.Fatal("End did not resume subsequent streamed output")
			}
		})
	}
}

func TestHistoryBoundaryOnEmptyAndShortHistory(t *testing.T) {
	for _, text := range []string{"", "short transcript"} {
		t.Run(text, func(t *testing.T) {
			u, _ := pagingTestUI(t)
			if text != "" {
				u.display.AddLine(text)
			}
			u.showHistoryBoundary(true)
			u.showHistoryBoundary(false)
			if u.activeViewportLocked().browsing {
				t.Fatal("End left short history paused")
			}
		})
	}
}

func TestViewportKeepsAnchorAsOutputArrives(t *testing.T) {
	h := newHistoryWriter(io.Discard)
	for i := 0; i < 30; i++ {
		h.AddLine(fmt.Sprintf("line %d", i))
	}
	v := viewport{}
	page := v.page(historyRows(h.Snapshot(), 80), 5, 1)
	anchor := page[0].position
	for i := 30; i < 70; i++ {
		h.AddLine(fmt.Sprintf("line %d", i))
	}
	page = v.page(historyRows(h.Snapshot(), 80), 5, 0)
	if !v.browsing || page[0].position != anchor {
		t.Fatalf("page moved: %v", page)
	}
	for i := 0; i < 20 && v.browsing; i++ {
		v.page(historyRows(h.Snapshot(), 80), 5, -1)
	}
	if v.browsing {
		t.Fatal("PageDown did not resume live output")
	}
}

func TestViewportReflowsStyledWideText(t *testing.T) {
	h := newHistoryWriter(io.Discard)
	h.AddLine("\x1b[31m你好世界你好世界你好世界你好世界\x1b[0m")
	for i := 0; i < 10; i++ {
		h.AddLine("tail")
	}
	rows := historyRows(h.Snapshot(), 8)
	v := viewport{browsing: true, anchor: rows[2].position}
	anchor := v.anchor
	page := v.page(historyRows(h.Snapshot(), 12), 3, 0)
	if page[0].position.line != anchor.line || page[0].position.column > anchor.column {
		t.Fatalf("resize lost logical position: %v", page)
	}
	if !strings.Contains(page[0].text, "\x1b[31m") {
		t.Fatal("wrapped page lost style")
	}
	v.page(historyRows(h.Snapshot(), 8), 3, 0)
	if v.anchor != anchor {
		t.Fatal("repeated reflow drifted anchor")
	}
	for _, row := range page {
		if visibleWidth(row.text) > 12 {
			t.Fatalf("wide row: %q", row.text)
		}
	}
}

func TestViewportClampsEvictedAnchor(t *testing.T) {
	h := newHistoryWriter(io.Discard)
	for i := 0; i < maxHistoryLines; i++ {
		h.AddLine("line")
	}
	v := viewport{browsing: true, anchor: historyPosition{line: 2}}
	h.AddLine("trigger eviction")
	snapshot := h.Snapshot()
	page := v.page(historyRows(snapshot, 80), 10, 0)
	if !v.browsing || page[0].position.line != snapshot.lines[0].id {
		t.Fatalf("evicted anchor not clamped: %v", v)
	}
}

func TestHistorySnapshotIncludesUnfinishedStyledText(t *testing.T) {
	h := newHistoryWriter(io.Discard)
	for _, b := range []byte("\x1b[31m你好") {
		_, _ = h.Write([]byte{b})
	}
	snapshot := h.Snapshot()
	if len(snapshot.lines) != 1 || snapshot.lines[0].text != "\x1b[31m你好\x1b[0m" {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if len(h.Lines()) != 0 {
		t.Fatal("snapshot committed unfinished output")
	}
	_, _ = h.Write([]byte("\nnext"))
	next := h.Snapshot()
	if next.lines[0].id != snapshot.lines[0].id || next.lines[1].id <= next.lines[0].id {
		t.Fatal("line IDs changed on commit")
	}
}

func TestHistorySnapshotKeepsRewriteCursor(t *testing.T) {
	h := newHistoryWriter(io.Discard)
	_, _ = io.WriteString(h, "abcdef\rxy")
	s := h.Snapshot()
	if plainHistoryText(s.lines[0].text) != "xycdef" || s.cursor != 2 {
		t.Fatalf("snapshot: %#v", s)
	}
}

func TestApprovalReturnsToLiveBeforeReading(t *testing.T) {
	u, out := pagingTestUI(t)
	for i := 0; i < 40; i++ {
		fmt.Fprintf(u.display, "line %d\n", i)
	}
	u.showPage(1)
	u.terminal = lineedit.NewTerminal(readWriter{Reader: strings.NewReader("\rn\r"), Writer: out}, "> ")
	before := fileSize(t, out)
	_, approved, err := u.ApproveDirectory(context.Background(), "/tmp", "/tmp")
	if err != nil || approved {
		t.Fatalf("approval=%v err=%v", approved, err)
	}
	if u.viewport.browsing {
		t.Fatal("approval remained in browsing mode")
	}
	data, _ := os.ReadFile(out.Name())
	if !strings.Contains(string(data[before:]), "Additional directory access requested") {
		t.Fatal("approval request hidden")
	}
}

func pagingTestUI(t *testing.T) (*UI, *os.File) {
	t.Helper()
	out, err := os.CreateTemp(t.TempDir(), "terminal")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = out.Close() })
	u := New(nil, out, nil, "demo", "demo", ".")
	u.height, u.width, u.statusActive = 12, 60, true
	u.terminal = lineedit.NewTerminal(readWriter{Reader: strings.NewReader(""), Writer: out}, "> ")
	return u, out
}

func fileSize(t *testing.T, out *os.File) int64 {
	t.Helper()
	s, err := out.Stat()
	if err != nil {
		t.Fatal(err)
	}
	return s.Size()
}

func TestDisplayBuffersWhileBrowsingAndRestoresPartialTail(t *testing.T) {
	for _, managed := range []bool{false, true} {
		t.Run(fmt.Sprint(managed), func(t *testing.T) {
			u, out := pagingTestUI(t)
			if managed {
				u.AddAgentView("main", "demo", "demo")
			}
			for i := 0; i < 40; i++ {
				fmt.Fprintf(u.display, "line %d\n", i)
			}
			u.showPage(1)
			before := fileSize(t, out)
			_, _ = io.WriteString(u.display, "new streamed line\npartial")
			if fileSize(t, out) != before {
				t.Fatal("stream overwrote browsing page")
			}
			u.resetPage()
			data, _ := os.ReadFile(out.Name())
			if !strings.Contains(string(data[before:]), "partial") {
				t.Fatal("return to live lost partial line")
			}
			before = fileSize(t, out)
			_, _ = io.WriteString(u.display, " continuation\n")
			if fileSize(t, out) <= before {
				t.Fatal("live output did not resume")
			}
		})
	}
}

func TestCompletionAndBackgroundOutputPreserveViewport(t *testing.T) {
	u, out := pagingTestUI(t)
	u.AddAgentView("main", "demo", "demo")
	background, _ := u.AddAgentView("agent-1", "demo", "demo")
	for i := 0; i < 40; i++ {
		fmt.Fprintf(u.display, "line %d\n", i)
	}
	u.showPage(1)
	anchor := u.views["main"].viewport.anchor
	before := fileSize(t, out)
	_, _ = io.WriteString(background, "background output\n")
	u.handleAgentEvent(session.Event{Agent: session.Summary{ID: "main", Status: session.StatusCompleted}})
	// A completion can repaint status, but its persistent notice stays in history.
	data, _ := os.ReadFile(out.Name())
	if strings.Contains(string(data[before:]), "Completed in") || strings.Contains(string(data[before:]), "background output") {
		t.Fatal("completion or background output overwrote page")
	}
	if u.views["main"].viewport.anchor != anchor || !u.views["main"].viewport.browsing {
		t.Fatal("completion moved viewport")
	}
	// Tab selection changes only which viewport is resolved by repaint.
	u.screenMu.Lock()
	u.activeAgent, u.display = "agent-1", u.views["agent-1"].display
	u.repaintActiveLocked(0)
	u.activeAgent, u.display = "main", u.views["main"].display
	u.repaintActiveLocked(0)
	u.screenMu.Unlock()
	if u.views["main"].viewport.anchor != anchor {
		t.Fatal("tab repaint lost anchor")
	}
}

func TestConcurrentPagingAndOutput(t *testing.T) {
	u, _ := pagingTestUI(t)
	u.AddAgentView("main", "demo", "demo")
	for i := 0; i < 40; i++ {
		fmt.Fprintf(u.display, "seed %d\n", i)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			fmt.Fprintf(u.display, "stream %d\n", i)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 30; i++ {
			u.showPage(1)
			u.showPage(-1)
		}
	}()
	wg.Wait()
	u.resetPage()
	if len(u.display.Lines()) != 140 {
		t.Fatal("paging dropped output")
	}
}

func TestHistoryBoundaryKeysEditPromptInLiveView(t *testing.T) {
	for _, resumeKey := range []string{"", "\x1b[F", pageDownSequence} {
		t.Run(fmt.Sprintf("resumeKey=%q", resumeKey), func(t *testing.T) {
			u, _ := pagingTestUI(t)
			for i := 0; i < 40; i++ {
				u.display.AddLine(fmt.Sprintf("line %d", i))
			}
			if resumeKey != "" {
				u.input.route([]byte(pageUpSequence))
				if !u.activeViewportLocked().browsing {
					t.Fatal("PageUp did not enter history browsing")
				}
				u.input.route([]byte(resumeKey))
				if u.activeViewportLocked().browsing {
					t.Fatal("history navigation did not restore live view")
				}
			}
			u.input.route([]byte("draft\x1b[HX\x1b[FY\r"))
			editor := lineedit.NewTerminal(readWriter{Reader: u.input, Writer: io.Discard}, inputPrompt)
			line, err := editor.ReadLine()
			if err != nil || line != "XdraftY" || u.activeViewportLocked().browsing {
				t.Fatalf("Home/End must edit the live prompt: line=%q err=%v viewport=%+v", line, err, u.activeViewportLocked())
			}
		})
	}
}
