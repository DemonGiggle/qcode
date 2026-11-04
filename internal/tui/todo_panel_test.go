package tui

import (
	"io"
	"os"
	"strings"
	"testing"

	"qcode/internal/lineedit"
	"qcode/internal/session"
)

type todoPanelRunner struct {
	planViewTestRunner
	ids      []string
	titles   []string
	statuses []string
	version  uint64
	text     string
	hasText  bool
}

func (r *todoPanelRunner) TodoSnapshot() ([]string, []string, []string, uint64, bool) {
	if r.ids == nil {
		return nil, nil, nil, r.version, false
	}
	return r.ids, r.titles, r.statuses, r.version, true
}

func (r *todoPanelRunner) LatestTodoText() (string, bool) { return r.text, r.hasText }

func todoPanelFixture(t *testing.T, runner *todoPanelRunner) (*UI, func() string) {
	t.Helper()
	out, err := os.CreateTemp(t.TempDir(), "todo-screen")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { out.Close() })
	manager := &layoutManager{states: map[string]session.Status{"main": session.StatusIdle}, queued: map[string][]session.QueuedPrompt{}}
	u := &UI{fixedInput: true, out: out, width: 80, height: 24, inputLabel: inputPrompt,
		activeAgent: "main", drafts: map[string]string{}, views: map[string]*agentView{},
		manager: manager, runner: runner}
	u.display = &agentDisplay{ui: u, id: "main", history: newHistoryWriter(io.Discard)}
	u.views["main"] = &agentView{id: "main", display: u.display.(*agentDisplay)}
	return u, func() string {
		return strings.Join(u.inputScreenRows, "\n")
	}
}

func todoFiveItems() (ids, titles, statuses []string) {
	return []string{"one", "two", "three", "four", "five"},
		[]string{"First task", "Second task", "Third task", "Fourth task", "Fifth task"},
		[]string{"done", "doing", "todo", "todo", "todo"}
}

func TestTodoPanelCollapsedShowsThreeRowsAndCount(t *testing.T) {
	ids, titles, statuses := todoFiveItems()
	runner := &todoPanelRunner{ids: ids, titles: titles, statuses: statuses, version: 3,
		text: "# Todo (1/5 done)", hasText: true}
	u, frame := todoPanelFixture(t, runner)
	u.renderInput(inputPrompt, "", 0)
	got := frame()
	if !strings.Contains(got, "Todo 1/5 | Alt+T expand") {
		t.Fatalf("missing todo header with count: %q", got)
	}
	for _, want := range []string{"First task", "Second task", "Third task"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing preview row %q: %q", want, got)
		}
	}
	if strings.Contains(got, "Fourth task") || strings.Contains(got, "Fifth task") {
		t.Fatalf("collapsed panel shows more than three rows: %q", got)
	}
}

func TestTodoDisplayRowsPreservesStatusForThemeColoring(t *testing.T) {
	items := []todoItem{
		{id: "one", title: "First", status: "done"},
		{id: "two", title: "Second", status: "doing"},
		{id: "three", title: "Third", status: "todo"},
	}
	rows := todoDisplayRows(items, 80, false)
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(rows))
	}
	if rows[0].status != "done" || !rows[0].done {
		t.Fatalf("done row = %+v", rows[0])
	}
	if rows[1].status != "doing" || rows[1].done {
		t.Fatalf("doing row = %+v", rows[1])
	}
	if rows[2].status != "todo" || rows[2].done {
		t.Fatalf("todo row = %+v", rows[2])
	}
	// Theme governance: panel must use semantic ANSI colors that
	// TransformANSI maps per palette (cyan->Prompt, green->Success,
	// yellow->Warning), not dim-only styling.
	for _, seq := range []string{cyan, green, yellow} {
		if !strings.Contains(seq, "\x1b[") {
			t.Fatalf("theme color sequence missing: %q", seq)
		}
	}
}


func TestTodoPanelSitsAboveQueue(t *testing.T) {
	ids, titles, statuses := todoFiveItems()
	runner := &todoPanelRunner{ids: ids, titles: titles, statuses: statuses, version: 1,
		text: "# Todo (1/5 done)", hasText: true}
	u, frame := todoPanelFixture(t, runner)
	u.manager.(*layoutManager).queued["main"] = []session.QueuedPrompt{
		{RequestID: "q1", Prompt: "queued work"},
	}
	u.renderInput(inputPrompt, "", 0)
	got := frame()
	todoAt := strings.Index(got, "Todo 1/5")
	queueAt := strings.Index(got, "Queued")
	if todoAt < 0 || queueAt < 0 {
		t.Fatalf("missing todo (%d) or queue (%d): %q", todoAt, queueAt, got)
	}
	if todoAt > queueAt {
		t.Fatalf("todo panel is not above queue: %q", got)
	}
}

func TestTodoPanelExpandShowsAllRows(t *testing.T) {
	ids, titles, statuses := todoFiveItems()
	runner := &todoPanelRunner{ids: ids, titles: titles, statuses: statuses, version: 1,
		text: "# Todo (1/5 done)", hasText: true}
	u, frame := todoPanelFixture(t, runner)
	u.renderInput(inputPrompt, "", 0)
	u.toggleTodoPanel()
	got := frame()
	if !strings.Contains(got, "Todo 1/5 | Alt+T close") {
		t.Fatalf("missing expanded header: %q", got)
	}
	for _, want := range []string{"Fourth task", "Fifth task"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expanded panel missing %q: %q", want, got)
		}
	}
	u.toggleTodoPanel()
	if got := frame(); !strings.Contains(got, "Alt+T expand") || strings.Contains(got, "Fifth task") {
		t.Fatalf("collapse did not restore preview: %q", got)
	}
}

func TestTodoPanelEscapeCollapses(t *testing.T) {
	ids, titles, statuses := todoFiveItems()
	runner := &todoPanelRunner{ids: ids, titles: titles, statuses: statuses, version: 1,
		text: "# Todo (1/5 done)", hasText: true}
	u, frame := todoPanelFixture(t, runner)
	u.renderInput(inputPrompt, "", 0)
	u.toggleTodoPanel()
	if !u.views["main"].todo.expanded {
		t.Fatal("toggle did not expand the panel")
	}
	if !u.handleTodoPanelKey(lineedit.KeyEscape) {
		t.Fatal("Esc did not collapse the panel")
	}
	if u.views["main"].todo.expanded {
		t.Fatal("panel still expanded after Esc")
	}
	if got := frame(); !strings.Contains(got, "Alt+T expand") {
		t.Fatalf("frame missing collapsed header: %q", got)
	}
}

func TestTodoPanelCompletionArchivesOnceAndHides(t *testing.T) {
	runner := &todoPanelRunner{
		ids:      []string{"one", "two"},
		titles:   []string{"First task", "Second task"},
		statuses: []string{"done", "done"},
		version:  4,
		text:     "# Todo (2/2 done)\n\n- [x] First task (one)\n- [x] Second task (two)",
		hasText:  true,
	}
	u, frame := todoPanelFixture(t, runner)
	if _, err := u.display.Write([]byte("activity line\n")); err != nil {
		t.Fatal(err)
	}
	history := strings.Join(u.display.Lines(), "\n")
	if !strings.Contains(history, "# Todo (2/2 done)") || !strings.Contains(history, "Second task (two)") {
		t.Fatalf("completed list not archived to transcript: %q", history)
	}
	if got := frame(); strings.Contains(got, "Todo 2/2") {
		t.Fatalf("finished panel still visible: %q", got)
	}
	if _, err := u.display.Write([]byte("another line\n")); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(u.display.Lines(), "\n"); strings.Count(got, "# Todo (2/2 done)") != 1 {
		t.Fatalf("completed list archived more than once: %q", got)
	}
}

func TestTodoPanelClearedArchivesCachedList(t *testing.T) {
	ids, titles, statuses := todoFiveItems()
	runner := &todoPanelRunner{ids: ids, titles: titles, statuses: statuses, version: 2,
		text: "# Todo (1/5 done)\n\n- [x] First task (one)", hasText: true}
	u, _ := todoPanelFixture(t, runner)
	if _, err := u.display.Write([]byte("working\n")); err != nil {
		t.Fatal(err)
	}
	if !u.views["main"].todo.hasLive {
		t.Fatal("live list was not observed")
	}
	runner.ids, runner.titles, runner.statuses = nil, nil, nil
	runner.hasText = false
	if _, err := u.display.Write([]byte("fresh start\n")); err != nil {
		t.Fatal(err)
	}
	history := strings.Join(u.display.Lines(), "\n")
	if !strings.Contains(history, "# Todo (1/5 done)") {
		t.Fatalf("cleared list not archived to transcript: %q", history)
	}
	if _, err := u.display.Write([]byte("more output\n")); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(u.display.Lines(), "\n"); strings.Count(got, "# Todo (1/5 done)") != 1 {
		t.Fatalf("cleared list archived more than once: %q", got)
	}
}

func TestTodoPanelHiddenOnCrampedScreen(t *testing.T) {
	ids, titles, statuses := todoFiveItems()
	runner := &todoPanelRunner{ids: ids, titles: titles, statuses: statuses, version: 1,
		text: "# Todo (1/5 done)", hasText: true}
	u, frame := todoPanelFixture(t, runner)
	u.height = 8
	u.renderInput(inputPrompt, "", 0)
	got := frame()
	if !strings.Contains(got, "Todo 1/5") {
		t.Fatalf("panel should still fit at height 8: %q", got)
	}
	u.height = 6
	u.renderInput(inputPrompt, "", 0)
	if got := frame(); strings.Contains(got, "Todo 1/5") {
		t.Fatalf("panel should yield to transcript minimums: %q", got)
	}
}

func TestDiscardActiveTodoRemovesWithoutArchiving(t *testing.T) {
	ids, titles, statuses := todoFiveItems()
	runner := &todoPanelRunner{ids: ids, titles: titles, statuses: statuses, version: 2,
		text: "# Todo (1/5 done)\n\n- [x] First task (one)", hasText: true}
	u, frame := todoPanelFixture(t, runner)
	if _, err := u.display.Write([]byte("working\n")); err != nil {
		t.Fatal(err)
	}
	if !u.views["main"].todo.hasLive {
		t.Fatal("live list was not observed")
	}
	u.discardActiveTodo()
	panel := u.views["main"].todo
	if panel.hasLive || panel.lastText != "" || panel.expanded || panel.completed || panel.lastVersion != 0 {
		t.Fatalf("discard kept panel state: %+v", panel)
	}
	// Simulate /new clearing the backend checklist.
	runner.ids, runner.titles, runner.statuses = nil, nil, nil
	runner.hasText = false
	if _, err := u.display.Write([]byte("fresh start\n")); err != nil {
		t.Fatal(err)
	}
	if history := strings.Join(u.display.Lines(), "\n"); strings.Contains(history, "# Todo (1/5 done)") {
		t.Fatalf("discarded list leaked into transcript: %q", history)
	}
	u.renderInput(inputPrompt, "", 0)
	if got := frame(); strings.Contains(got, "Todo 1/5") {
		t.Fatalf("discarded panel still visible: %q", got)
	}
}

type newSessionTodoManager struct {
	*layoutManager
	runner *todoPanelRunner
	reset  bool
}

func (m *newSessionTodoManager) Reset(id string) error {
	m.reset = true
	m.runner.ids, m.runner.titles, m.runner.statuses = nil, nil, nil
	m.runner.hasText = false
	m.runner.text = ""
	return nil
}

func TestStartNewSessionDiscardsTodoPanel(t *testing.T) {
	ids, titles, statuses := todoFiveItems()
	runner := &todoPanelRunner{ids: ids, titles: titles, statuses: statuses, version: 2,
		text: "# Todo (1/5 done)\n\n- [x] First task (one)", hasText: true}
	u, frame := todoPanelFixture(t, runner)
	u.responseWriter = NewMarkdownWriter(io.Discard, false, 80)
	manager := &newSessionTodoManager{layoutManager: u.manager.(*layoutManager), runner: runner}
	u.manager = manager
	if _, err := u.display.Write([]byte("working\n")); err != nil {
		t.Fatal(err)
	}
	if !u.views["main"].todo.hasLive {
		t.Fatal("live list was not observed")
	}
	u.startNewSession()
	if !manager.reset {
		t.Fatal("manager session was not reset")
	}
	panel := u.views["main"].todo
	if panel.hasLive || panel.lastText != "" {
		t.Fatalf("/new kept todo panel state: %+v", panel)
	}
	gotHistory := strings.Join(u.display.Lines(), "\n")
	if strings.Contains(gotHistory, "# Todo (1/5 done)") {
		t.Fatalf("/new archived todo instead of removing it: %q", gotHistory)
	}
	if !strings.Contains(gotHistory, "New session started") {
		t.Fatalf("missing reset confirmation: %q", gotHistory)
	}
	u.renderInput(inputPrompt, "", 0)
	if got := frame(); strings.Contains(got, "Todo 1/5") {
		t.Fatalf("/new left todo panel visible: %q", got)
	}
}
