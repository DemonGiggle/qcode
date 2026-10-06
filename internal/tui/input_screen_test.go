package tui

import (
	"io"
	"os"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
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
				if params != "2" {
					panic("unsupported screen erasure")
				}
				s.rows[s.y] = []rune(strings.Repeat(" ", width))
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
	if got := string(screen.rows[u.inputCursorRow-2]); !strings.HasPrefix(got, "   "+strings.Repeat("x", 70)) {
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
