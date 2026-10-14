package lineedit

import (
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func bracketPaste(text string) string { return string(pasteStart) + text + string(pasteEnd) }

func TestPasteWaitsForEnterAndKeepsFullDraft(t *testing.T) {
	input, writer := io.Pipe()
	t.Cleanup(func() { input.Close(); writer.Close() })
	terminal := NewTerminal(readWriter{keyReader{input}, io.Discard}, "> ")
	const payload = "first\r\nsecond\rthird\n\t你好🙂�\n"
	const want = "first\nsecond\nthird\n\t你好🙂�\n"
	label := fmt.Sprintf("[paste %d characters]", utf8.RuneCountInString(want))
	rendered := make(chan string, 1)
	terminal.RenderInput = func(_, line string, pos int) {
		display, cursor := terminal.DisplayInput(line, pos)
		if line == want {
			if display != label || cursor != len([]rune(label)) {
				rendered <- fmt.Sprintf("display=%q cursor=%d", display, cursor)
			} else {
				rendered <- "ready"
			}
		}
	}
	terminal.AutoCompleteCallback = func(line string, pos int, key rune) (string, int, bool) {
		t.Error("paste invoked autocomplete")
		return line, pos, false
	}
	result := make(chan struct {
		line string
		err  error
	}, 1)
	go func() {
		line, err := terminal.ReadLine()
		result <- struct {
			line string
			err  error
		}{line, err}
	}()
	if _, err := io.WriteString(writer, bracketPaste(payload)); err != nil {
		t.Fatal(err)
	}
	select {
	case state := <-rendered:
		if state != "ready" {
			t.Fatal(state)
		}
	case got := <-result:
		t.Fatalf("paste submitted before Enter: %q, %v", got.line, got.err)
	case <-time.After(time.Second):
		t.Fatal("paste did not reach the draft")
	}
	select {
	case got := <-result:
		t.Fatalf("paste submitted before Enter: %q, %v", got.line, got.err)
	default:
	}
	if _, err := io.WriteString(writer, "\r"); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-result:
		if got.err != nil || got.line != want || terminal.SubmissionKey != '\r' {
			t.Fatalf("submission=%q key=%q error=%v", got.line, terminal.SubmissionKey, got.err)
		}
	case <-time.After(time.Second):
		t.Fatal("Enter did not submit the paste")
	}
}

func TestPasteEditingKeepsBlocksWhole(t *testing.T) {
	for _, tc := range []struct{ name, input, want, preview string }{
		{"prefix-and-suffix", "before " + bracketPaste("甲\n乙") + " after", "before 甲\n乙 after", "before [paste 3 characters] after"},
		{"backspace", "before " + bracketPaste("甲\n乙") + "\x7fafter", "before after", "before after"},
		{"delete", "before " + bracketPaste("甲\n乙") + "after\x1b[D\x1b[D\x1b[D\x1b[D\x1b[D\x1b[D\x1b[3~", "before after", "before after"},
		{"insert-before", bracketPaste("甲\n乙") + "\x1b[Dprefix ", "prefix 甲\n乙", "prefix [paste 3 characters]"},
		{"insert-after", bracketPaste("甲\n乙") + "\x1b[D\x1b[C suffix", "甲\n乙 suffix", "[paste 3 characters] suffix"},
		{"multiple", bracketPaste("甲\n乙") + " " + bracketPaste("ab\ncd"), "甲\n乙 ab\ncd", "[paste 3 characters] [paste 5 characters]"},
		{"insert-paste-before", bracketPaste("甲\n乙") + "\x01" + bracketPaste("ab\ncd"), "ab\ncd甲\n乙", "[paste 5 characters][paste 3 characters]"},
		{"clear", bracketPaste("甲\n乙") + "\x15typed", "typed", "typed"},
		{"delete-word", "prefix " + bracketPaste("甲\n乙") + "\x17suffix", "prefix suffix", "prefix suffix"},
		{"kill-line", "prefix " + bracketPaste("甲\n乙") + " suffix\x01\x0btyped", "typed", "typed"},
		{"literal-controls", bracketPaste("a\t\x03\x15\x1b[A\nb"), "a\t\x03\x15\x1b[A\nb", "[paste 9 characters]"},
		{"empty", "before" + bracketPaste(""), "before", "before"},
		{"large", bracketPaste(strings.Repeat("甲\n", 5000)) + "suffix", strings.Repeat("甲\n", 5000) + "suffix", "[paste 10000 characters]suffix"},
		{"at-previous-limit", bracketPaste(strings.Repeat("a", 4096)) + "suffix", strings.Repeat("a", 4096) + "suffix", "[paste 4096 characters]suffix"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			terminal := NewTerminal(readWriter{keyReader{strings.NewReader(tc.input + "\r")}, io.Discard}, "> ")
			preview := ""
			terminal.RenderInput = func(_, line string, pos int) {
				if line == tc.want {
					preview, _ = terminal.DisplayInput(line, pos)
				}
			}
			line, err := terminal.ReadLine()
			if err != nil || line != tc.want || preview != tc.preview {
				t.Fatalf("line=%q preview=%q error=%v; want %q, %q", line, preview, err, tc.want, tc.preview)
			}
			if extra, err := terminal.ReadLine(); err != io.EOF {
				t.Fatalf("extra paste submission: %q, %v", extra, err)
			}
		})
	}
}

func TestHistoryNavigationKeepsUnsubmittedPasteCollapsed(t *testing.T) {
	terminal := NewTerminal(readWriter{keyReader{strings.NewReader(bracketPaste("clipboard") + "\x1b[A\x1b[B\r")}, io.Discard}, "> ")
	terminal.history.Add("previous prompt")
	var preview string
	terminal.RenderInput = func(_, line string, pos int) {
		if line == "clipboard" {
			preview, _ = terminal.DisplayInput(line, pos)
		}
	}
	line, err := terminal.ReadLine()
	if err != nil || line != "clipboard" || preview != "[paste 9 characters]" {
		t.Fatalf("history return: line=%q preview=%q error=%v", line, preview, err)
	}
}

func TestInlinePasteExpandsOnlyAfterEnter(t *testing.T) {
	for _, width := range []int{12, 80} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			s := &screen{width: width}
			terminal := NewTerminal(readWriter{strings.NewReader(bracketPaste("alpha\nbeta")), s}, "> ")
			terminal.SetSize(width, 40)
			if _, err := terminal.ReadLine(); err != io.EOF {
				t.Fatalf("paste submitted before Enter: %v", err)
			}
			expected := &screen{width: width}
			expected.Write([]byte("> [paste 10 characters]"))
			if got := s.contents(); got != expected.contents() {
				t.Fatalf("draft screen=%q, want %q", got, expected.contents())
			}
			terminal.c = readWriter{strings.NewReader("\r"), s}
			if line, err := terminal.ReadLine(); err != nil || line != "alpha\nbeta" {
				t.Fatalf("submission=%q, %v", line, err)
			}
			if got := s.contents(); got != "> alpha\nbeta" {
				t.Fatalf("submitted screen=%q", got)
			}
		})
	}
}
