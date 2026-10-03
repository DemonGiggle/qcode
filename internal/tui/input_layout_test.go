package tui

import (
	"io"
	"os"
	"strings"
	"testing"

	"qcode/internal/lineedit"
	"qcode/internal/session"
)

type layoutManager struct {
	agentController
	states map[string]session.Status
	queued map[string][]session.QueuedPrompt
	latest map[string]string
}

func (m *layoutManager) LatestPrompt(id string) string { return m.latest[id] }

func (m *layoutManager) Summary(id string) (session.Summary, error) {
	return session.Summary{ID: id, Name: id, Status: m.states[id], QueueDepth: len(m.queued[id])}, nil
}
func (m *layoutManager) QueuedPrompts(id string) []session.QueuedPrompt {
	return append([]session.QueuedPrompt(nil), m.queued[id]...)
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
	for _, want := range []string{"(Steer)> /", "  /agent", "  /exit", "  /history", "type to filter", "streamed output"} {
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
	if !strings.Contains(frame(), "(Steer)> draft") {
		t.Fatal("running prompt missing")
	}
	u.activeAgent = "agent-1"
	u.renderInput(inputPrompt, "other draft", 2)
	if strings.Contains(frame(), "(Steer)>") || !strings.Contains(frame(), "> other draft") {
		t.Fatal("idle tab inherited queue state")
	}
	u.activeAgent = "main"
	u.manager.(*layoutManager).states["main"] = session.StatusCompleted
	u.renderInput(inputPrompt, "draft", 3)
	if strings.Contains(frame(), "(Steer)>") {
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
	if !strings.Contains(frame(), "(Steer)> /h") {
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

func TestQueuedPanelStaysVisibleAndPagesIndependently(t *testing.T) {
	u, frame := layoutFixture(t)
	manager := u.manager.(*layoutManager)
	manager.queued = map[string][]session.QueuedPrompt{"main": {
		{RequestID: "request-2", Prompt: "first queued prompt with several words and 你好"},
		{RequestID: "request-3", Prompt: "second queued prompt\nwith another line"},
		{RequestID: "request-4", Prompt: "third queued prompt " + strings.Repeat("long ", 20)},
	}}
	u.width, u.height = 32, 17
	u.renderInput(inputPrompt, "draft", 5)
	if got := frame(); !strings.Contains(got, "Queued | Alt+Q expand") || !strings.Contains(got, "1. first queued") || !strings.Contains(got, "2. second queued") || !strings.Contains(got, "(Steer)> draft") {
		t.Fatalf("reserved queue area = %q", got)
	}
	for i := 0; i < 30; i++ {
		u.display.AddLine("streamed output")
	}
	if !strings.Contains(frame(), "Queued | Alt+Q expand") || !strings.Contains(frame(), "1. first queued") {
		t.Fatal("streamed output displaced queue panel")
	}
	u.showPage(1)
	historyAnchor := u.activeViewportLocked().anchor
	u.toggleQueuePanel()
	if got := frame(); !strings.Contains(got, "Alt+Q close") || !strings.Contains(got, "1. first queued") {
		t.Fatalf("expanded panel = %q", got)
	}
	u.showPage(-1)
	if !u.activeQueueLocked().expanded || u.activeQueueLocked().anchorID == "request-2" || !u.activeViewportLocked().browsing || u.activeViewportLocked().anchor != historyAnchor {
		t.Fatalf("queue paging changed history: queue=%+v history=%+v", u.activeQueueLocked(), u.activeViewportLocked())
	}
	u.toggleQueuePanel()
	if !strings.Contains(frame(), "1. first queued") || !strings.Contains(frame(), "2. second queued") {
		t.Fatal("folded queue area lost queued message text")
	}
	u.showPage(1)
	if !u.activeViewportLocked().browsing {
		t.Fatal("Page Up did not return to transcript after collapsing queue")
	}
	u.width, u.height = 12, 8
	u.renderInput(inputPrompt, strings.Repeat("x", 200), 200)
	if !strings.Contains(frame(), "Q Alt+Q") {
		t.Fatal("tall draft or small width hid the queue shortcut")
	}
	manager.queued["main"] = nil
	u.renderInput(inputPrompt, "draft", 5)
	if strings.Contains(frame(), "Alt+Q") || u.activeQueueLocked().expanded {
		t.Fatal("drained queue retained panel state")
	}
}

type pendingLayoutManager struct {
	*layoutManager
	cancelledID string
}

func (*pendingLayoutManager) SubmitPrompt(session.PromptSubmission) (session.Submission, error) {
	return session.Submission{}, nil
}

func (m *pendingLayoutManager) PendingInputs(id string) []session.QueuedPrompt {
	return m.QueuedPrompts(id)
}

func (m *pendingLayoutManager) CancelInput(agentID, promptID string) error {
	m.cancelledID = promptID
	for i, item := range m.queued[agentID] {
		if item.RequestID == promptID {
			m.queued[agentID] = append(m.queued[agentID][:i], m.queued[agentID][i+1:]...)
			break
		}
	}
	return nil
}

func TestPendingPanelSeparatesSteeringAndQueuedWork(t *testing.T) {
	for _, unicodeEnabled := range []bool{true, false} {
		t.Run(map[bool]string{true: "unicode", false: "ascii"}[unicodeEnabled], func(t *testing.T) {
			u, frame := layoutFixture(t)
			u.unicode = unicodeEnabled
			manager := &pendingLayoutManager{layoutManager: u.manager.(*layoutManager)}
			u.manager = manager
			manager.queued = map[string][]session.QueuedPrompt{"main": {
				{RequestID: "steer", Intent: session.IntentSteer, State: session.InputPending, Source: "terminal", Actor: "local-tui", Prompt: "adjust the button to be more flexible"},
				{RequestID: "first", Intent: session.IntentQueue, Source: "terminal", Actor: "local-tui", Prompt: "after that please commit the code"},
				{RequestID: "second", Intent: session.IntentQueue, Source: "terminal", Actor: "local-tui", Prompt: "The commit message should be as short as possible"},
			}}
			u.renderInput(inputPrompt, "draft 你好", 8)
			want := "Steer\n ╰─ adjust the button to be more flexible\n\nQueued | Alt+Q expand\n │  1. after that please commit the code\n ╰─ 2. The commit message should be as short as possible"
			if !unicodeEnabled {
				want = strings.ReplaceAll(strings.ReplaceAll(want, "╰─", "+-"), "│", "|")
			}
			if got := frame(); !strings.Contains(got, want) || strings.Contains(got, "terminal:") || strings.Contains(got, "(pending)") {
				t.Fatalf("pending panel does not match requested layout:\n%s", got)
			}
			u.toggleQueuePanel()
			if !u.handlePendingPanelKey(lineedit.KeyDown) || u.activeQueueLocked().selectedID != "first" {
				t.Fatal("section headings changed item selection")
			}
			if !strings.Contains(frame(), ">  "+map[bool]string{true: "│", false: "|"}[unicodeEnabled]+"  1. after that please commit") {
				t.Fatalf("selected queued prompt is not marked: %s", frame())
			}
			u.handlePendingPanelKey(lineedit.KeyDelete)
			if manager.cancelledID != "first" || len(manager.queued["main"]) != 2 || u.drafts["main"] != "draft 你好" {
				t.Fatal("tree panel removal changed selection or composer draft")
			}
			u.handlePendingPanelKey(lineedit.KeyEscape)
			if !strings.Contains(frame(), "1. The commit message") || strings.Contains(frame(), "after that please commit") {
				t.Fatalf("collapsed panel did not refresh after removal: %s", frame())
			}
		})
	}
}

func TestHistoryBoundaryNavigatesTranscriptWithExpandedQueue(t *testing.T) {
	u, _ := layoutFixture(t)
	u.manager.(*layoutManager).queued = map[string][]session.QueuedPrompt{"main": {
		{RequestID: "first", Prompt: strings.Repeat("queued line\n", 20)},
		{RequestID: "second", Prompt: "next prompt"},
	}}
	for i := 0; i < 40; i++ {
		u.display.AddLine("transcript line")
	}
	u.renderInput(inputPrompt, "draft", 3)
	u.showPage(1)
	u.toggleQueuePanel()
	u.showPage(-1)
	queue := *u.activeQueueLocked()
	if queue.anchorRow == 0 {
		t.Fatal("queue fixture did not scroll")
	}
	u.showHistoryBoundary(true)
	oldest := historyRows(u.display.Snapshot(), u.width)[0]
	if !u.activeViewportLocked().browsing || u.activeViewportLocked().anchor != oldest.position {
		t.Fatalf("Home did not reach transcript beginning: %+v", u.activeViewportLocked())
	}
	u.showHistoryBoundary(false)
	if u.activeViewportLocked().browsing || *u.activeQueueLocked() != queue {
		t.Fatalf("End changed queue or left history paused: queue=%+v history=%+v", u.activeQueueLocked(), u.activeViewportLocked())
	}
}

func TestQueuedPanelStateIsPerTabAndEscapesPromptControls(t *testing.T) {
	u, frame := layoutFixture(t)
	manager := u.manager.(*layoutManager)
	manager.queued = map[string][]session.QueuedPrompt{
		"main":    {{RequestID: "request-2", Prompt: "safe\x1b[2J text"}},
		"agent-1": {{RequestID: "request-3", Prompt: "other tab"}},
	}
	u.views["main"] = &agentView{id: "main", display: u.display.(*agentDisplay)}
	u.views["agent-1"] = &agentView{id: "agent-1", display: &agentDisplay{ui: u, id: "agent-1", history: newHistoryWriter(io.Discard)}}
	u.renderInput(inputPrompt, "draft", 5)
	u.toggleQueuePanel()
	if strings.Contains(frame(), "\x1b[2J") || !strings.Contains(frame(), "<ESC>[2J") {
		t.Fatalf("unsafe prompt text rendered: %q", frame())
	}
	u.activeAgent = "agent-1"
	u.display = u.views["agent-1"].display
	u.renderInput(inputPrompt, "other", 5)
	if !strings.Contains(frame(), "Alt+Q expand") || strings.Contains(frame(), "Alt+Q close") || !strings.Contains(frame(), "other tab") {
		t.Fatal("expanded state leaked into other tab")
	}
	u.activeAgent = "main"
	u.display = u.views["main"].display
	u.renderInput(inputPrompt, "draft", 5)
	if !strings.Contains(frame(), "Alt+Q close") {
		t.Fatal("main tab lost expanded state")
	}
}
