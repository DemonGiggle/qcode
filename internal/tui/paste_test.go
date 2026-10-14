package tui

import (
	"io"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"qcode/internal/lineedit"
)

func TestComposerPasteKeepsFullDraftAndCompactDisplay(t *testing.T) {
	for _, restore := range []bool{false, true} {
		t.Run(map[bool]string{false: "terminal-paste", true: "restored-draft"}[restore], func(t *testing.T) {
			u, frame := layoutFixture(t)
			input, writer := io.Pipe()
			t.Cleanup(func() { input.Close(); writer.Close() })
			u.input = newInterruptReader(input)
			u.input.start()
			u.terminal = lineedit.NewTerminal(readWriter{Reader: u.input, Writer: io.Discard}, inputPrompt)
			const payload = "first line\nsecond line\n你好🙂\n"
			const label = "[paste 27 characters]"
			rendered := make(chan string, 1)
			u.terminal.RenderInput = func(prompt, line string, pos int) {
				u.renderInput(prompt, line, pos)
				if line == payload {
					u.screenMu.Lock()
					state := frame()
					if u.drafts["main"] != payload || u.inputPosition != utf8.RuneCountInString(payload) {
						state = "full draft was lost"
					}
					u.screenMu.Unlock()
					rendered <- state
				}
			}
			result := make(chan struct {
				line string
				err  error
			}, 1)
			go func() {
				line, err := u.readLine()
				result <- struct {
					line string
					err  error
				}{line, err}
			}()
			if restore {
				u.input.injectDraft(payload)
			} else if _, err := io.WriteString(writer, bracketedPasteStart+payload+bracketedPasteEnd); err != nil {
				t.Fatal(err)
			}
			select {
			case state := <-rendered:
				if !strings.Contains(state, "(Steer)> "+label) || strings.Contains(state, "first line") {
					t.Fatalf("paste frame=%q", state)
				}
			case got := <-result:
				t.Fatalf("paste submitted early: %q, %v", got.line, got.err)
			case <-time.After(time.Second):
				t.Fatal("paste was not rendered")
			}
			if strings.Contains(strings.Join(u.display.Lines(), "\n"), "first line") {
				t.Fatal("unsubmitted paste entered conversation history")
			}
			if _, err := io.WriteString(writer, "\r"); err != nil {
				t.Fatal(err)
			}
			select {
			case got := <-result:
				if got.err != nil || got.line != payload {
					t.Fatalf("submitted paste=%q, %v", got.line, got.err)
				}
			case <-time.After(time.Second):
				t.Fatal("Enter did not submit the full paste")
			}
		})
	}
}
