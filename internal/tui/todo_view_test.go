package tui

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
)

type todoEmptyRunner struct{ planViewTestRunner }

func (*todoEmptyRunner) LatestTodoText() (string, bool) { return "", false }

type todoFilledRunner struct{ planViewTestRunner }

func (*todoFilledRunner) LatestTodoText() (string, bool) {
	return "# Todo (1/2 done)\n\n- [x] First (one)\n- [-] Second (two)", true
}

func TestTodoCommandUsage(t *testing.T) {
	display := newHistoryWriter(io.Discard)
	u := &UI{display: display, width: 100, runner: &todoEmptyRunner{}}
	u.handleTodoCommand(context.Background(), []string{"/todo"})
	if got := strings.Join(display.Lines(), "\n"); !strings.Contains(got, "Usage: /todo show") {
		t.Fatalf("usage = %q", got)
	}
	display = newHistoryWriter(io.Discard)
	u = &UI{display: display, width: 100, runner: &todoEmptyRunner{}}
	u.handleTodoCommand(context.Background(), []string{"/todo", "show", "extra"})
	if got := strings.Join(display.Lines(), "\n"); !strings.Contains(got, "Usage: /todo show") {
		t.Fatalf("usage = %q", got)
	}
}

func TestTodoCommandWithoutList(t *testing.T) {
	display := newHistoryWriter(io.Discard)
	u := &UI{display: display, width: 100, runner: &todoEmptyRunner{}}
	u.handleTodoCommand(context.Background(), []string{"/todo", "show"})
	if got := strings.Join(display.Lines(), "\n"); !strings.Contains(got, "No todo list yet") {
		t.Fatalf("empty todo = %q", got)
	}
}

func TestTodoCommandUnavailable(t *testing.T) {
	display := newHistoryWriter(io.Discard)
	u := &UI{display: display, width: 100, runner: &planViewTestRunner{}}
	u.handleTodoCommand(context.Background(), []string{"/todo", "show"})
	if got := strings.Join(display.Lines(), "\n"); !strings.Contains(got, "Todo is unavailable") {
		t.Fatalf("unavailable = %q", got)
	}
}

func TestTodoShowDoesNotAddChecklistToHistory(t *testing.T) {
	out, err := os.CreateTemp(t.TempDir(), "todo-view")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()

	input := newInterruptReader(nil)
	input.data <- 'q'
	display := newHistoryWriter(io.Discard)
	display.AddLine("existing output")
	u := &UI{
		fixedInput: true,
		input:      input,
		out:        out,
		width:      80,
		height:     12,
		display:    display,
		runner:     &todoFilledRunner{},
	}

	u.handleTodoCommand(context.Background(), []string{"/todo", "show"})
	if got := strings.Join(display.Lines(), "\n"); strings.Contains(got, "Second (two)") {
		t.Fatalf("todo leaked into history: %q", got)
	}
	if !strings.Contains(strings.Join(display.Lines(), "\n"), "existing output") {
		t.Fatalf("existing history was lost: %q", display.Lines())
	}
}
