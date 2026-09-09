package tui

import (
	"io"
	"os"
	"strings"
	"testing"

	"qcode/internal/session"
)

type layoutManager struct {
	agentController
	states map[string]session.Status
}

func (m *layoutManager) Summary(id string) (session.Summary, error) {
	return session.Summary{ID: id, Name: id, Status: m.states[id]}, nil
}
func (m *layoutManager) List() []session.Summary {
	a, _ := m.Summary("main")
	b, _ := m.Summary("agent-1")
	return []session.Summary{a, b}
}

func layoutFixture(t *testing.T) (*UI, func() string) {
	t.Helper()
	out, err := os.CreateTemp(t.TempDir(), "screen")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { out.Close() })
	u := &UI{fixedInput: true, out: out, width: 80, height: 24, inputLabel: inputPrompt,
		activeAgent: "main", drafts: map[string]string{}, views: map[string]*agentView{},
		manager: &layoutManager{states: map[string]session.Status{"main": session.StatusRunning, "agent-1": session.StatusIdle}}}
	u.display = &agentDisplay{ui: u, id: "main", history: newHistoryWriter(io.Discard)}
	return u, func() string {
		return strings.Join(u.inputScreenRows, "\n")
	}
}

func TestFixedInputSurvivesStreamingAndFiltersCandidates(t *testing.T) {
	u, frame := layoutFixture(t)
	u.renderInput(inputPrompt, "/", 1)
	for i := 0; i < 40; i++ {
		_, _ = u.display.Write([]byte("streamed output\n"))
	}
	got := frame()
	for _, want := range []string{"(Queue)> /", "  /agent", "  /exit", "streamed output"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in frame %q", want, got)
		}
	}
	if strings.Contains(strings.Join(u.display.Lines(), "\n"), "/agent") {
		t.Fatal("suggestions leaked into history")
	}
	u.renderInput(inputPrompt, "/h", 2)
	if !strings.Contains(frame(), "  /help") || strings.Contains(frame(), "  /agent") {
		t.Fatal("filter did not replace panel")
	}
	u.renderInput(inputPrompt, "", 0)
	if strings.Contains(frame(), "  /help") {
		t.Fatal("clearing draft left suggestions")
	}
}

func TestFixedPromptUsesActiveAgentState(t *testing.T) {
	u, frame := layoutFixture(t)
	u.renderInput(inputPrompt, "draft", 3)
	if !strings.Contains(frame(), "(Queue)> draft") {
		t.Fatal("running prompt missing")
	}
	u.activeAgent = "agent-1"
	u.renderInput(inputPrompt, "other draft", 2)
	if strings.Contains(frame(), "(Queue)>") || !strings.Contains(frame(), "> other draft") {
		t.Fatal("idle tab inherited queue state")
	}
	u.activeAgent = "main"
	u.manager.(*layoutManager).states["main"] = session.StatusCompleted
	u.renderInput(inputPrompt, "draft", 3)
	if strings.Contains(frame(), "(Queue)>") {
		t.Fatal("completed task retained queue prompt")
	}
}

func TestInputRowsTracksWideCharactersAndWrapBoundary(t *testing.T) {
	rows, y, x := inputRows("> 界ab", 5, 5)
	if strings.Join(rows, "|") != "> 界a|b" || y != 1 || x != 1 {
		t.Fatalf("rows=%q cursor=%d,%d", rows, y, x)
	}
}

func TestFixedLayoutSmallAndWrappedDraft(t *testing.T) {
	u, _ := layoutFixture(t)
	for _, height := range []int{1, 2, 3, 4, 8, 24} {
		u.height, u.width = height, 12
		u.renderInput(inputPrompt, strings.Repeat("x", 120), 120)
		if u.inputCursorRow < 1 || u.inputCursorColumn < 1 {
			t.Fatal("cursor not restored")
		}
		if len(u.inputScreenRows) != height+1 {
			t.Fatalf("screen rows = %d, want %d", len(u.inputScreenRows), height+1)
		}
	}
}

func TestFixedHistoryStaysPausedWhileInputChanges(t *testing.T) {
	u, frame := layoutFixture(t)
	for i := 0; i < 60; i++ {
		u.display.AddLine("history row")
	}
	u.renderInput(inputPrompt, "/", 1)
	u.paintFixedLocked(1)
	anchor := u.activeViewportLocked().anchor
	_, _ = u.display.Write([]byte("new streamed content\n"))
	u.renderInput(inputPrompt, "/h", 2)
	if !u.activeViewportLocked().browsing || u.activeViewportLocked().anchor != anchor {
		t.Fatal("stream or suggestions moved the reading position")
	}
	if !strings.Contains(frame(), "(Queue)> /h") {
		t.Fatal("paging moved the prompt")
	}
}

func TestFixedInputUpdatesOnlyChangedRows(t *testing.T) {
	u, _ := layoutFixture(t)
	u.manager.(*layoutManager).states["main"] = session.StatusIdle
	u.renderInput(inputPrompt, "a", 1)
	if err := u.out.Sync(); err != nil {
		t.Fatal(err)
	}
	before, err := u.out.Stat()
	if err != nil {
		t.Fatal(err)
	}
	u.renderInput(inputPrompt, "ab", 2)
	if err := u.out.Sync(); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(u.out.Name())
	if err != nil {
		t.Fatal(err)
	}
	update := string(after[before.Size():])
	if clears := strings.Count(update, "\x1b[2K"); clears != 1 {
		t.Fatalf("changed input cleared %d rows: %q", clears, update)
	}
	if strings.Contains(update, "\x1b[2;1H") {
		t.Fatalf("changed input repainted history: %q", update)
	}
}
