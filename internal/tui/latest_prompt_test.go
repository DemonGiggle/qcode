package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"qcode/internal/redaction"
	"qcode/internal/session"
	qtheme "qcode/internal/theme"
)

func TestLatestPromptWrappingIsLiteralAndBounded(t *testing.T) {
	for _, tc := range []struct {
		name, text   string
		width, limit int
		unicode      bool
		want         []string
	}{
		{"empty", "", 20, 3, true, nil},
		{"short", "hello", 20, 3, true, []string{"✦ hello"}},
		{"ASCII marker", "hello", 20, 3, false, []string{"* hello"}},
		{"line breaks", "one\r\n\nthree", 20, 3, true, []string{"✦ one", "", "three"}},
		{"hard wrap", "abcdefghijklmnop", 10, 3, true, []string{"✦", "abcdefghij", "klmnop"}},
		{"wide and combining", "界界界e\u0301", 16, 3, true, []string{"✦ 界界界e\u0301"}},
		{"unicode truncation", "one\ntwo\nthree\nfour", 20, 3, true, []string{"✦ one", "two", "three…"}},
		{"ascii truncation", "one\ntwo\nthree\nfour", 20, 3, false, []string{"* one", "two", "three..."}},
		{"literal markup", "<b>**hi**</b>", 40, 3, true, []string{"✦ <b>**hi**</b>"}},
		{"controls", "\x1b[31m\tX\x00", 40, 3, true, []string{"✦ <0x1B>[31m    X<0x00>"}},
		{"tiny unicode", "界", 1, 1, true, []string{"…"}},
		{"tiny ascii", "界", 1, 1, false, []string{"."}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := latestPromptRows(tc.text, tc.width, tc.limit, tc.unicode, false)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("rows = %#v, want %#v", got, tc.want)
			}
			for _, row := range got {
				if visibleWidth(row) > tc.width || strings.Contains(row, "\x1b") {
					t.Fatalf("invalid plain row %q", row)
				}
			}
		})
	}
	for _, palette := range qtheme.All() {
		id := palette.ID
		row := qtheme.TransformANSI(latestPromptRows("hello", 80, 3, true, true)[0], palette)
		wantColor := magenta
		if id != "default" {
			wantColor = rgbSGR(38, palette.Accent)
		}
		if !strings.Contains(row, wantColor+bold+"✦\x1b[22m hello") {
			t.Fatalf("%s prompt style = %q", id, row)
		}
		if id != "default" && palette.Accent == palette.Prompt {
			t.Fatalf("%s prompt shares the active tab color", id)
		}
	}
	for width := 1; width <= 8; width++ {
		for _, row := range latestPromptRows("a long prompt 界", width, 1, true, true) {
			if !utf8.ValidString(row) || visibleWidth(row) > width {
				t.Fatalf("width %d: invalid colored row %q", width, row)
			}
		}
	}
}

func TestPinnedPromptPreservesHistoryAndComposer(t *testing.T) {
	u, frame := layoutFixture(t)
	m := u.manager.(*layoutManager)
	m.latest = map[string]string{"main": "first\nsecond\nthird\nfourth", "agent-1": "other tab"}
	u.unicode = true
	for i := 0; i < 80; i++ {
		u.display.AddLine(fmt.Sprintf("history %d", i))
	}
	u.renderInput(inputPrompt, "draft 界", 3)
	cursorRow, cursorCol := u.inputCursorRow, u.inputCursorColumn
	if u.inputScreenRows[2] != "✦ first" || u.inputScreenRows[4] != "third…" || !strings.Contains(frame(), "history 79") {
		t.Fatalf("prompt is not above transcript: %q", frame())
	}
	u.showPage(1)
	anchor := u.activeViewportLocked().anchor
	m.latest["main"] = "a delivered steer with a different height"
	_, _ = io.WriteString(u.display, "streamed output\n")
	if u.activeViewportLocked().anchor != anchor || !u.activeViewportLocked().browsing {
		t.Fatal("prompt update or streaming moved the reading anchor")
	}
	if u.inputCursorRow != cursorRow || u.inputCursorColumn != cursorCol || u.inputText != "draft 界" {
		t.Fatal("prompt update or streaming moved the composer")
	}
	u.width = 30
	u.repaintActive()
	if u.activeViewportLocked().anchor != anchor || !u.activeViewportLocked().browsing {
		t.Fatal("resize moved the reading anchor")
	}
	u.showPage(-1)
	u.showHistoryBoundary(false)
	if !strings.Contains(frame(), "streamed output") || u.activeViewportLocked().browsing {
		t.Fatal("paging did not return to live output")
	}
	u.activeAgent = "agent-1"
	u.renderInput(inputPrompt, "other draft", 2)
	if u.inputScreenRows[2] != "✦ other tab" {
		t.Fatal("tab switch retained the other agent's prompt")
	}
	u.display.Clear()
	u.repaintActive()
	if u.inputScreenRows[2] != "✦ other tab" {
		t.Fatal("clearing output removed the pin")
	}
	m.latest["agent-1"] = ""
	u.repaintActive()
	if strings.Contains(frame(), "✦") {
		t.Fatal("empty prompt left a pinned region")
	}
}

func TestPinnedPromptBackgroundFillsEveryColumn(t *testing.T) {
	const width = 40
	for _, palette := range qtheme.All() {
		row := latestPromptRows("hello", width, 3, true, true)[0]
		row = paintLatestPromptBackground(row, width, palette)
		painted := qtheme.PaintRow(row, width, palette)
		background := rgbSGR(48, qtheme.PinnedPromptBackground(palette))
		if visibleWidth(painted) != width || !strings.Contains(painted, background) {
			t.Fatalf("%s prompt background does not fill the row: %q", palette.ID, painted)
		}
		padding := strings.Repeat(" ", width-visibleWidth("✦ hello"))
		if !strings.Contains(painted, "hello"+padding+reset) {
			t.Fatalf("%s background reset before the right edge: %q", palette.ID, painted)
		}
		if qtheme.PinnedPromptBackground(palette) == palette.Background {
			t.Fatalf("%s highlight matches the surrounding background", palette.ID)
		}
	}
	t.Setenv("NO_COLOR", "1")
	u, _ := layoutFixture(t)
	u.manager.(*layoutManager).latest = map[string]string{"main": "hello"}
	u.renderInput(inputPrompt, "", 0)
	if u.inputScreenRows[2] != "* hello" {
		t.Fatal("NO_COLOR decorated the prompt")
	}
}

func TestPinnedPromptYieldsSpaceOnCrampedScreens(t *testing.T) {
	u, _ := layoutFixture(t)
	u.manager.(*layoutManager).latest = map[string]string{"main": strings.Repeat("界 long prompt\n", 100)}
	for _, height := range []int{1, 2, 3, 4, 5, 6, 7, 8, 10, 24} {
		for _, width := range []int{1, 8, 40} {
			u.height, u.width = height, width
			u.renderInput(inputPrompt, strings.Repeat("draft", 50), 120)
			if u.inputCursorRow < 1 || u.inputCursorRow > height || u.inputCursorColumn < 1 || u.inputCursorColumn > width {
				t.Fatalf("%dx%d cursor = %d,%d", width, height, u.inputCursorRow, u.inputCursorColumn)
			}
			if height >= 8 && u.activeViewportLocked().visibleRows < 2 {
				t.Fatalf("%dx%d has only %d output rows", width, height, u.activeViewportLocked().visibleRows)
			}
		}
	}
}

func TestPinnedPromptHidesInCommandViewsAndReturnsAfterwards(t *testing.T) {
	u, _ := layoutFixture(t)
	u.manager.(*layoutManager).latest = map[string]string{"main": "one\ntwo\nthree"}
	u.renderInput(inputPrompt, "draft", 3)
	before := fileSize(t, u.out)
	u.beginRawSelector()
	data, _ := os.ReadFile(u.out.Name())
	if !strings.Contains(string(data[before:]), "\x1b[2;22r\x1b[2;1H") || !strings.Contains(string(data[before:]), "\x1b[2;1H\x1b[2K") {
		t.Fatal("command view did not hide the pinned prompt")
	}
	for _, writer := range []io.Writer{planViewWriter{ui: u, start: u.latestPromptStartLocked()}, historyViewWriter{ui: u, start: u.latestPromptStartLocked()}} {
		before = fileSize(t, u.out)
		renderPlanPager(writer, newPlanPager("plan", 80), 80, 2, false)
		data, _ = os.ReadFile(u.out.Name())
		if !strings.Contains(string(data[before:]), "\x1b[2;1H") {
			t.Fatal("pager did not use space freed by the pinned prompt")
		}
	}
	u.endRawSelector()
	u.showRemoteLogin(RemoteLogin{URL: "https://example.test/#login=secret", ExpiresAt: time.Now().Add(time.Minute)})
	t.Cleanup(u.clearRemoteLogin)
	if u.inputScreenRows[2] != "* one" || !strings.Contains(u.inputScreenRows[5], "Remote login") {
		t.Fatal("login view overwrote the pinned prompt")
	}
}

func TestLatestPromptRedactionAndRemoteThemeUpdates(t *testing.T) {
	u, _ := layoutFixture(t)
	u.redaction, _ = redaction.New(redaction.Config{}, []string{tuiTestSecret})
	u.manager.(*layoutManager).latest = map[string]string{"main": "literal <b>\n" + tuiTestSecret}
	u.views["main"] = &agentView{id: "main", display: u.display.(*agentDisplay)}
	u.width = 12
	u.renderInput(inputPrompt, "", 0)
	if strings.Contains(strings.Join(u.inputScreenRows, ""), tuiTestSecret) {
		t.Fatal("terminal prompt disclosed a secret split across rows")
	}
	p := u.RemotePresentation()
	if p.Views[0].LatestPrompt != "literal <b>\n"+redaction.Marker || p.PromptColor != "#d58cff" || p.PromptMarker != "*" || p.PromptBackground != "#372846" {
		t.Fatalf("remote prompt = %+v", p)
	}
	u.presentationSubs = map[uint64]chan struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	updates := u.SubscribePresentation(ctx)
	if err := u.SetTheme("dracula"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-updates:
	default:
		t.Fatal("theme change did not notify remote viewers")
	}
	if p := u.RemotePresentation(); p.PromptColor != "#ff79c6" {
		t.Fatalf("remote prompt color = %q", p.PromptColor)
	}
	if p := u.RemotePresentation(); p.PromptBackground == "#372846" || p.PromptBackground == "" {
		t.Fatalf("theme change retained the default prompt background: %q", p.PromptBackground)
	}
	u.unicode = true
	if p := u.RemotePresentation(); p.PromptMarker != "✦" {
		t.Fatalf("remote Unicode marker = %q", p.PromptMarker)
	}
	snap, err := FilterSnapshot(u.redaction, session.Snapshot{Agents: []session.SavedAgent{{LatestPrompt: tuiTestSecret}}})
	if err != nil || snap.Agents[0].LatestPrompt != redaction.Marker {
		t.Fatalf("persisted prompt = %+v, %v", snap, err)
	}
	disabled := false
	policy, _ := redaction.New(redaction.Config{Terminal: &disabled, Remote: &disabled, Persistence: &disabled}, nil)
	u.redaction = policy
	if u.RemotePresentation().Views[0].LatestPrompt != "literal <b>\n"+tuiTestSecret {
		t.Fatal("disabled remote policy still redacted the prompt")
	}
	snap, err = FilterSnapshot(policy, session.Snapshot{Agents: []session.SavedAgent{{LatestPrompt: tuiTestSecret}}})
	if err != nil || snap.Agents[0].LatestPrompt != tuiTestSecret {
		t.Fatal("disabled persistence policy still redacted the prompt")
	}
	encoded, _ := json.Marshal(p)
	if !bytes.Contains(encoded, []byte(`"latest_prompt"`)) || !bytes.Contains(encoded, []byte(`"prompt_color"`)) {
		t.Fatal("remote transport omitted prompt fields")
	}
}
