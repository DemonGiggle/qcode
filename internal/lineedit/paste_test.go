package lineedit

import (
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

func bracketPaste(text string) string { return string(pasteStart) + text + string(pasteEnd) }

func TestPasteWaitsForEnterAndKeepsFullDraft(t *testing.T) {
	input, writer := io.Pipe()
	t.Cleanup(func() { input.Close(); writer.Close() })
	terminal := NewTerminal(readWriter{keyReader{input}, io.Discard}, "> ")
	const payload = "first\r\nsecond\rthird\n\t你好🙂�\n"
	const pasted = "first\nsecond\nthird\n\t你好🙂�\n"
	const prefix = "prefix "
	const want = prefix + pasted
	label := prefix + string(pasteLabel([]rune(pasted)))
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
	// Start from a non-empty draft so the long paste collapses; an empty
	// prompt keeps pastes inline by design (see TestPasteIntoEmptyPromptStaysInline).
	terminal.SetExpandedDraft(prefix)
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
	tall := "甲\n乙\n丙\n丁"
	tall2 := "ab\ncd\nef\ngh"
	tallControls := "a\t\x03\n\x15\n\x1b[A\nb"
	label := func(s string) string { return string(pasteLabel([]rune(s))) }
	largePaste := strings.Repeat("甲\n", 5000)
	hugePaste := strings.Repeat("a", 4096)
	hundredPaste := strings.Repeat("a", 100)
	fourLinePaste := "a\nb\nc\nd"
	for _, tc := range []struct{ name, input, want, preview string }{
		{"prefix-and-suffix", "before " + bracketPaste(tall) + " after", "before " + tall + " after", "before " + label(tall) + " after"},
		{"backspace", "before " + bracketPaste(tall) + "\x7fafter", "before after", "before after"},
		{"delete", "before " + bracketPaste(tall) + "after\x1b[D\x1b[D\x1b[D\x1b[D\x1b[D\x1b[D\x1b[3~", "before after", "before after"},
		{"insert-before", "before " + bracketPaste(tall) + "\x1b[Dprefix ", "before prefix " + tall, "before prefix " + label(tall)},
		{"insert-after", "before " + bracketPaste(tall) + "\x1b[D\x1b[C suffix", "before " + tall + " suffix", "before " + label(tall) + " suffix"},
		{"multiple", "before " + bracketPaste(tall) + " " + bracketPaste(tall2), "before " + tall + " " + tall2, "before " + label(tall) + " " + label(tall2)},
		{"insert-paste-before", "before " + bracketPaste(tall) + "\x01" + bracketPaste(tall2), tall2 + "before " + tall, label(tall2) + "before " + label(tall)},
		{"clear", bracketPaste(tall) + "\x15typed", "typed", "typed"},
		{"delete-word", "prefix " + bracketPaste(tall) + "\x17suffix", "prefix suffix", "prefix suffix"},
		{"kill-line", "prefix " + bracketPaste(tall) + " suffix\x01\x0btyped", "typed", "typed"},
		{"literal-controls", "before " + bracketPaste(tallControls), "before " + tallControls, "before " + label(tallControls)},
		{"empty", "before" + bracketPaste(""), "before", "before"},
		{"large", "before " + bracketPaste(largePaste) + "suffix", "before " + largePaste + "suffix", "before " + label(largePaste) + "suffix"},
		{"at-previous-limit", "before " + bracketPaste(hugePaste) + "suffix", "before " + hugePaste + "suffix", "before " + label(hugePaste) + "suffix"},
		{"short-single-stays-inline", "before " + bracketPaste("hi") + " after", "before hi after", "before hi after"},
		{"short-two-line-stays-inline", bracketPaste("alpha\nbeta"), "alpha\nbeta", "alpha\nbeta"},
		{"three-lines-stay-inline", bracketPaste("a\nb\nc"), "a\nb\nc", "a\nb\nc"},
		{"four-lines-collapse", "before " + bracketPaste(fourLinePaste), "before " + fourLinePaste, "before " + label(fourLinePaste)},
		{"ninety-nine-stays-inline", bracketPaste(strings.Repeat("a", 99)), strings.Repeat("a", 99), strings.Repeat("a", 99)},
		{"one-hundred-collapses", "before " + bracketPaste(hundredPaste), "before " + hundredPaste, "before " + label(hundredPaste)},
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
	terminal := NewTerminal(readWriter{keyReader{strings.NewReader("note " + bracketPaste("clipboard\nline2\nline3\nline4") + "\x1b[A\x1b[B\r")}, io.Discard}, "> ")
	terminal.history.Add("previous prompt")
	var preview string
	terminal.RenderInput = func(_, line string, pos int) {
		if line == "note clipboard\nline2\nline3\nline4" {
			preview, _ = terminal.DisplayInput(line, pos)
		}
	}
	line, err := terminal.ReadLine()
	wantPreview := "note " + string(pasteLabel([]rune("clipboard\nline2\nline3\nline4")))
	if err != nil || line != "note clipboard\nline2\nline3\nline4" || preview != wantPreview {
		t.Fatalf("history return: line=%q preview=%q error=%v; want preview %q", line, preview, err, wantPreview)
	}
}

func TestPasteIntoEmptyPromptStaysInline(t *testing.T) {
	tall := "alpha\nbeta\ngamma\ndelta"
	terminal := NewTerminal(readWriter{keyReader{strings.NewReader(bracketPaste(tall) + "\r")}, io.Discard}, "> ")
	preview := ""
	terminal.RenderInput = func(_, line string, pos int) {
		if line == tall {
			preview, _ = terminal.DisplayInput(line, pos)
		}
	}
	line, err := terminal.ReadLine()
	if err != nil || line != tall || preview != tall {
		t.Fatalf("empty paste should stay inline: line=%q preview=%q error=%v", line, preview, err)
	}
}

func TestPasteLabelShowsHeadAndTailPreview(t *testing.T) {
	content := "the road is dark and full of terrors and wonders some days behind."
	label := string(pasteLabel([]rune(content)))
	if len(label) < 2 || label[0] != '[' || label[len(label)-1] != ']' {
		t.Fatalf("label should be bracketed, got %q", label)
	}
	if !strings.Contains(label, "the road") {
		t.Fatalf("label should show head preview, got %q", label)
	}
	if !strings.Contains(label, "days behind.") {
		t.Fatalf("label should show tail preview, got %q", label)
	}
	if !strings.Contains(label, "(hide ") || !strings.Contains(label, " characters)") {
		t.Fatalf("label should show hidden count, got %q", label)
	}
	if strings.Contains(label, "paste ") {
		t.Fatalf("label should not use old [paste N] format, got %q", label)
	}
	flat := flattenPastePreview([]rune(content))
	headLen, tailLen := pastePreviewHead, pastePreviewTail
	if len(flat) <= pastePreviewHead+pastePreviewTail {
		headLen = len(flat) / 3
		tailLen = len(flat) / 3
	}
	hidden := len([]rune(content)) - headLen - tailLen
	expectedSuffix := fmt.Sprintf("(hide %d characters)", hidden)
	if !strings.Contains(label, expectedSuffix) {
		t.Fatalf("label %q should contain %q", label, expectedSuffix)
	}
}

func TestShortPasteStaysInlineAndEditable(t *testing.T) {
	terminal := NewTerminal(readWriter{keyReader{strings.NewReader("before " + bracketPaste("hi") + " \x1b[D\x1b[D\x7f\r")}, io.Discard}, "> ")
	var preview string
	terminal.RenderInput = func(_, line string, pos int) {
		if line == "before hi " {
			preview, _ = terminal.DisplayInput(line, pos)
		}
	}
	// Left twice moves char-wise inside the short paste, Backspace deletes
	// one rune instead of the whole block.
	line, err := terminal.ReadLine()
	if err != nil || line != "before i " || preview != "before hi " {
		t.Fatalf("short paste edit: line=%q preview=%q error=%v", line, preview, err)
	}
}

func TestInlinePasteExpandsOnlyAfterEnter(t *testing.T) {
	for _, width := range []int{12, 80} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			s := &screen{width: width}
			terminal := NewTerminal(readWriter{strings.NewReader("hi " + bracketPaste("alpha\nbeta\ngamma\ndelta")), s}, "> ")
			terminal.SetSize(width, 40)
			if _, err := terminal.ReadLine(); err != io.EOF {
				t.Fatalf("paste submitted before Enter: %v", err)
			}
			expected := &screen{width: width}
			expected.Write([]byte("> hi " + string(pasteLabel([]rune("alpha\nbeta\ngamma\ndelta")))))
			if got := s.contents(); got != expected.contents() {
				t.Fatalf("draft screen=%q, want %q", got, expected.contents())
			}
			terminal.c = readWriter{strings.NewReader("\r"), s}
			if line, err := terminal.ReadLine(); err != nil || line != "hi alpha\nbeta\ngamma\ndelta" {
				t.Fatalf("submission=%q, %v", line, err)
			}
			if got := s.contents(); got != "> hi alpha\nbeta\ngamma\ndelta" {
				t.Fatalf("submitted screen=%q", got)
			}
		})
	}
}

func TestInlineShortPasteStaysVisible(t *testing.T) {
	s := &screen{width: 80}
	terminal := NewTerminal(readWriter{strings.NewReader(bracketPaste("alpha\nbeta")), s}, "> ")
	terminal.SetSize(80, 40)
	if _, err := terminal.ReadLine(); err != io.EOF {
		t.Fatalf("paste submitted before Enter: %v", err)
	}
	expected := &screen{width: 80}
	expected.Write([]byte("> alpha\nbeta"))
	if got := s.contents(); got != expected.contents() {
		t.Fatalf("draft screen=%q, want %q", got, expected.contents())
	}
}
