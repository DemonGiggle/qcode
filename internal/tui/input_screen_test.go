package tui

import (
	"io"
	"os"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"

	"qcode/internal/session"
)

// fixedScreen replays the cursor positioning, erasure and wrapping used by the
// fixed renderer. Checking the terminal cells catches output that overwrites
// an unchanged row even though inputScreenRows still contains the right text.
type fixedScreen struct {
	rows           [][]rune
	x, y           int
	savedX, savedY int
	wrap           bool
	wrapPending    bool
}

func newFixedScreen(width, height int) *fixedScreen {
	s := &fixedScreen{rows: make([][]rune, height), wrap: true}
	for row := range s.rows {
		s.rows[row] = []rune(strings.Repeat(" ", width))
	}
	return s
}

func (s *fixedScreen) feed(data string) {
	width := len(s.rows[0])
	for len(data) > 0 {
		if strings.HasPrefix(data, "\x1b[") {
			length, complete := ansiSequenceLength([]byte(data))
			if !complete {
				panic("incomplete screen escape")
			}
			params, command := data[2:length-1], data[length-1]
			data = data[length:]
			switch command {
			case 'H':
				values := strings.Split(params, ";")
				s.y, _ = strconv.Atoi(values[0])
				s.x = 1
				if len(values) > 1 {
					s.x, _ = strconv.Atoi(values[1])
				}
				s.x = max(0, min(width-1, s.x-1))
				s.y = max(0, min(len(s.rows)-1, s.y-1))
				s.wrapPending = false
			case 'K':
				start := 0
				if params == "" || params == "0" {
					start = s.x
				} else if params != "2" {
					panic("unsupported screen erasure")
				}
				for x := start; x < width; x++ {
					s.rows[s.y][x] = ' '
				}
			case 's':
				s.savedX, s.savedY = s.x, s.y
			case 'u':
				s.x, s.y = s.savedX, s.savedY
				s.wrapPending = false
			case 'r':
				if params != "" {
					panic("unexpected scrolling region in fixed screen")
				}
				s.x, s.y, s.wrapPending = 0, 0, false
			case 'h', 'l':
				if params == "?7" {
					s.wrap = command == 'h'
				}
			case 'm': // Styling does not move the cursor.
			default:
				panic("unsupported fixed screen command")
			}
			continue
		}
		r, size := utf8.DecodeRuneInString(data)
		data = data[size:]
		if r == '\t' {
			s.x = min(width-1, (s.x/8+1)*8)
			continue
		}
		cells := runewidth.RuneWidth(r)
		if cells == 0 {
			continue
		}
		if s.wrap && (s.wrapPending || s.x+cells > width) {
			s.x, s.y = 0, min(len(s.rows)-1, s.y+1)
		}
		s.rows[s.y][s.x] = r
		s.x += cells
		s.wrapPending = s.x >= width
		s.x = min(width-1, s.x)
	}
}

func TestFixedComposerHasOneBlankSeparator(t *testing.T) {
	for _, tc := range []struct {
		name, output string
		queued       bool
		expanded     bool
		draft        string
		label        string
		prompt       string
		status       session.Status
	}{
		{name: "streaming", output: "last output"},
		{name: "newline", output: "last output\n"},
		{name: "blank tail", output: "last output\n\n\n"},
		{name: "styled blank tail", output: "last output\n\x1b[2m \t\x1b[0m\n"},
		{name: "shell running", output: "\r\x1b[2KRunning shell command: sleep 1 (|)"},
		{name: "manual shell", output: "Running shell command (Ctrl+C to cancel): sleep 1\n"},
		{name: "queued", output: "last output\n", queued: true},
		{name: "expanded queue", output: "last output\n", queued: true, expanded: true},
		{name: "suggestions", output: "last output\n", draft: "/"},
		{name: "queue and suggestions", output: "last output\n", queued: true, draft: "/"},
		{name: "wrapped draft", output: "last output", draft: strings.Repeat("draft ", 30)},
		{name: "idle", output: "last output", label: inputPrompt, prompt: "> ", status: session.StatusIdle},
		{name: "plan", output: "last output\n", label: planInputPrompt, prompt: "(Plan)> ", status: session.StatusCompleted},
		{name: "skill plan", output: "last output\n\n", label: skillPlanInputPrompt, prompt: "(Skill plan)> ", status: session.StatusCompleted},
		{name: "approval", output: "last output", prompt: "(Steer)> ", status: session.StatusWaitingForApproval},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u, _ := layoutFixture(t)
			u.unicode = true
			label, prompt := tc.label, tc.prompt
			if label == "" {
				label = inputPrompt
			}
			if prompt == "" {
				prompt = "(Steer)> "
			}
			if tc.status != "" {
				u.manager.(*layoutManager).states["main"] = tc.status
			}
			if tc.queued {
				u.manager.(*layoutManager).queued = map[string][]session.QueuedPrompt{"main": {
					{RequestID: "queued", Prompt: "Does this fix apply to all models"},
				}}
			}
			_, _ = io.WriteString(u.display, strings.Repeat("earlier output\n", 40)+tc.output)
			u.renderInput(label, tc.draft, len([]rune(tc.draft)))
			if tc.expanded {
				u.toggleQueuePanel()
			}
			check := func() {
				t.Helper()
				data, err := os.ReadFile(u.out.Name())
				if err != nil {
					t.Fatal(err)
				}
				screen := newFixedScreen(u.width, u.height)
				screen.feed(string(data))
				promptRow := -1
				for i, row := range screen.rows {
					if strings.HasPrefix(string(row), prompt) {
						promptRow = i
						break
					}
				}
				if promptRow < 2 {
					t.Fatalf("composer missing: %q", u.inputScreenRows)
				}
				if gap := strings.TrimSpace(string(screen.rows[promptRow-1])); gap != "" {
					t.Fatalf("missing blank separator above composer: %q", gap)
				}
				if previous := strings.TrimSpace(string(screen.rows[promptRow-2])); previous == "" {
					t.Fatalf("more than one blank row above composer: %q", u.inputScreenRows)
				}
			}
			check()
			if tc.name == "shell running" {
				_, _ = io.WriteString(u.display, "\r\x1b[2KRunning shell command: sleep 1 (/)")
				u.drawTaskIndicator()
				check()
			}
			// Resizing and repaints must keep the separator above the first
			// draft row, even when the cursor is on a wrapped continuation.
			u.width, u.height = 32, 12
			if err := u.out.Truncate(0); err != nil {
				t.Fatal(err)
			}
			if _, err := u.out.Seek(0, io.SeekStart); err != nil {
				t.Fatal(err)
			}
			u.inputFrame = ""
			u.renderInput(label, tc.draft, 0)
			check()
			if tc.queued {
				u.manager.(*layoutManager).queued["main"] = nil
				u.renderInput(label, tc.draft, 0)
				check()
			}
		})
	}
}

func TestFixedStreamingTabsDoNotOverwriteComposer(t *testing.T) {
	u, _ := layoutFixture(t)
	for i := 0; i < u.height; i++ {
		u.display.AddLine("earlier output")
	}
	u.renderInput(inputPrompt, "draft", 5)
	// Tabs occupy one cell in the wrapping calculation, but advance to the
	// next eight-column tab stop when written directly to a terminal.
	_, _ = io.WriteString(u.display, "\t\t\t"+strings.Repeat("x", 70))
	u.drawTaskIndicator()
	data, err := os.ReadFile(u.out.Name())
	if err != nil {
		t.Fatal(err)
	}
	screen := newFixedScreen(u.width, u.height)
	screen.feed(string(data))
	if got := string(screen.rows[u.inputCursorRow-1]); !strings.HasPrefix(got, "(Steer)> draft") {
		t.Fatalf("streaming output overwrote the composer without a keypress: %q", got)
	}
	if got := string(screen.rows[u.inputCursorRow-2]); strings.TrimSpace(got) != "" {
		t.Fatalf("streaming output overwrote the composer separator: %q", got)
	}
	if got := string(screen.rows[u.inputCursorRow-3]); !strings.HasPrefix(got, "   "+strings.Repeat("x", 70)) {
		t.Fatalf("tabbed output was clipped instead of fitting its measured row: %q", got)
	}
	if !screen.wrap {
		t.Fatal("fixed renderer did not restore terminal autowrap")
	}
}

func TestFixedRowsDoNotWrapOverUnchangedComposer(t *testing.T) {
	u, _ := layoutFixture(t)
	u.renderInput(inputPrompt, "draft", 5)
	// Simulate a row whose terminal width exceeds the renderer's estimate.
	// Only the output row changes; the composer remains cached.
	rows := append([]string(nil), u.inputScreenRows...)
	rows[u.inputCursorRow-1] = strings.Repeat("x", u.width+20)
	u.writeFixedScreenLocked(rows, u.inputCursorRow, u.inputCursorColumn)
	data, err := os.ReadFile(u.out.Name())
	if err != nil {
		t.Fatal(err)
	}
	screen := newFixedScreen(u.width, u.height)
	screen.feed(string(data))
	if got := string(screen.rows[u.inputCursorRow-1]); !strings.HasPrefix(got, "(Steer)> draft") {
		t.Fatalf("wide output overwrote the cached composer: %q", got)
	}
	if !screen.wrap {
		t.Fatal("fixed renderer did not restore terminal autowrap")
	}
}
