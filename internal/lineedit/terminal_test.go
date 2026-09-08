package lineedit

import (
	"bytes"
	"io"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
)

type keyReader struct{ io.Reader }

func (r keyReader) Read(p []byte) (int, error) { return r.Reader.Read(p[:min(1, len(p))]) }

func TestExternalInputRenderingKeepsEditsOutOfOutput(t *testing.T) {
	var output bytes.Buffer
	terminal := NewTerminal(readWriter{keyReader{strings.NewReader("/help\x7f\r")}, &output}, "> ")
	var states []string
	terminal.RenderInput = func(prompt, line string, pos int) {
		// Calling a locking method verifies callbacks run outside the editor lock.
		terminal.SetPrompt(prompt)
		states = append(states, line)
	}
	line, err := terminal.ReadLine()
	if err != nil || line != "/hel" {
		t.Fatalf("line=%q err=%v", line, err)
	}
	if output.Len() != 0 {
		t.Fatalf("inline echo escaped external renderer: %q", output.String())
	}
	if len(states) < 2 {
		t.Fatal("input was not rendered")
	}
	if !strings.Contains(strings.Join(states, "|"), "/help|/hel|") {
		t.Fatalf("deletion did not refresh input: %q", states)
	}
}

func TestExternalRendererDoesNotExposePassword(t *testing.T) {
	terminal := NewTerminal(readWriter{keyReader{strings.NewReader("secret\r")}, io.Discard}, "> ")
	terminal.RenderInput = func(_, line string, pos int) {
		if line != "" || pos != 0 {
			t.Fatal("password exposed to renderer")
		}
	}
	password, err := terminal.ReadPassword("Password: ")
	if err != nil || password != "secret" {
		t.Fatalf("password=%q err=%v", password, err)
	}
}

// screen interprets the editor's output as a terminal would, including delayed
// wrapping. Assertions concern visible cells, not just the submitted string.
type screen struct {
	width, x, y int
	rows        [40][80]string
}

func (s *screen) Write(data []byte) (int, error) {
	text := string(data)
	for len(text) > 0 {
		if strings.HasPrefix(text, "\x1b[") {
			end := 2
			for text[end] < 0x40 || text[end] > 0x7e {
				end++
			}
			n, _ := strconv.Atoi(text[2:end])
			if n == 0 {
				n = 1
			}
			switch text[end] {
			case 'A':
				s.y -= n
				if s.y < 0 {
					s.y = 0
				}
			case 'B':
				s.y += n
			case 'C':
				s.x += n
			case 'D':
				s.x -= n
				if s.x < 0 {
					s.x = 0
				}
			case 'K':
				for x := s.x; x < s.width; x++ {
					s.rows[s.y][x] = ""
				}
			case 'm':
			default:
				panic("unsupported screen sequence: " + text[:end+1])
			}
			text = text[end+1:]
			continue
		}
		r, size := utf8.DecodeRuneInString(text)
		text = text[size:]
		switch r {
		case '\r':
			s.x = 0
		case '\n':
			s.y++
		default:
			w := runewidth.RuneWidth(r)
			if w == 0 {
				continue
			}
			if s.x+w > s.width {
				s.x = 0
				s.y++
			}
			s.rows[s.y][s.x] = string(r)
			for i := 1; i < w; i++ {
				s.rows[s.y][s.x+i] = ""
			}
			s.x += w
		}
	}
	return len(data), nil
}

func (s *screen) contents() string {
	var rows []string
	for _, row := range s.rows {
		rows = append(rows, strings.TrimRight(strings.Join(row[:s.width], ""), " "))
	}
	return strings.TrimRight(strings.Join(rows, "\n"), "\n")
}

type readWriter struct {
	io.Reader
	io.Writer
}

func TestChineseRenderingAfterEveryKey(t *testing.T) {
	for _, width := range []int{7, 8, 80} {
		for _, callback := range []bool{false, true} {
			t.Run(strconv.Itoa(width)+"/callback="+strconv.FormatBool(callback), func(t *testing.T) {
				s := &screen{width: width}
				terminal := NewTerminal(readWriter{strings.NewReader(""), s}, "\x1b[36m> \x1b[0m")
				terminal.SetSize(width, 40)
				if callback {
					terminal.AutoCompleteCallback = func(line string, pos int, key rune) (string, int, bool) {
						return line[:pos] + string(key) + line[pos:], pos + len(string(key)), true
					}
				}
				terminal.lock.Lock()
				defer terminal.lock.Unlock()
				terminal.writeLine(terminal.prompt)
				flush := func() { s.Write(terminal.outBuf); terminal.outBuf = nil }
				flush()
				check := func(want string) {
					t.Helper()
					flush()
					expected := &screen{width: width}
					expected.Write([]byte("> " + want))
					if got := s.contents(); got != expected.contents() {
						t.Fatalf("visible text = %q, want %q", got, expected.contents())
					}
					if s.x != terminal.cursorX || s.y != terminal.cursorY {
						t.Fatalf("physical cursor (%d,%d), editor cursor (%d,%d)", s.x, s.y, terminal.cursorX, terminal.cursorY)
					}
				}
				text := ""
				for _, r := range "你好世界中文輸入" {
					terminal.handleKey(r)
					text += string(r)
					check(text)
				}
				terminal.handleKey(keyLeft)
				check(text)
				terminal.handleKey(keyBackspace)
				check("你好世界中文入")
				terminal.handleKey('測')
				check("你好世界中文測入")
				terminal.handleKey(keyHome)
				check("你好世界中文測入")
				terminal.handleKey(keyDeleteLine)
				check("")
				terminal.history.Add("上一句中文")
				terminal.handleKey(keyUp)
				check("上一句中文")
				terminal.handleKey(keyDown)
				check("")
			})
		}
	}
}

func TestUTF8SubmissionPreservesOriginalText(t *testing.T) {
	const want = "中文🙂e\u0301\u2063"
	terminal := NewTerminal(readWriter{strings.NewReader(want + "\r"), io.Discard}, "> ")
	got, err := terminal.ReadLine()
	if err != nil || got != want {
		t.Fatalf("ReadLine() = %q, %v; want %q", got, err, want)
	}
}
