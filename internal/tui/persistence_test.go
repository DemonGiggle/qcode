package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"qcode/internal/session"
)

type persistenceManager struct {
	agentController
	agents []session.SavedAgent
	events chan session.Event
	once   sync.Once
}

func (m *persistenceManager) SaveAgents() ([]session.SavedAgent, int) { return m.agents, 2 }
func (m *persistenceManager) List() []session.Summary {
	var result []session.Summary
	for _, a := range m.agents {
		result = append(result, a.Summary)
	}
	return result
}
func (m *persistenceManager) Summary(id string) (session.Summary, error) {
	for _, a := range m.agents {
		if a.Summary.ID == id {
			return a.Summary, nil
		}
	}
	return session.Summary{}, fmt.Errorf("missing")
}
func (m *persistenceManager) Runner(string) (any, bool)    { return statusRunner{}, true }
func (m *persistenceManager) Events() <-chan session.Event { return m.events }
func (m *persistenceManager) Shutdown()                    { m.once.Do(func() { close(m.events) }) }

func persistenceUI(t *testing.T) (*UI, *persistenceManager) {
	t.Helper()
	out, err := os.CreateTemp(t.TempDir(), "terminal")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { out.Close() })
	u := New(out, out, statusRunner{}, "test", "main-model", t.TempDir())
	m := &persistenceManager{events: make(chan session.Event)}
	for _, id := range []string{"main", "agent-1"} {
		model := id + "-model"
		u.AddAgentView(id, "test", model)
		data := json.RawMessage(`{"Messages":[{"Role":"user","Content":"question"},{"Role":"assistant","Content":"latest answer"}]}`)
		m.agents = append(m.agents, session.SavedAgent{Summary: session.Summary{ID: id, Name: id, Model: model, Status: session.StatusCompleted}, State: data})
	}
	u.SetDetachedAgentManager(m)
	return u, m
}

func TestPresentationRoundTripAndConcurrentOutput(t *testing.T) {
	u, _ := persistenceUI(t)
	u.activeAgent = "agent-1"
	u.verbose = true
	u.drafts["main"] = "unfinished prompt"
	for _, v := range u.views {
		fmt.Fprint(v.display, "\x1b[31merror: example\x1b[0m\nReading file\npartial\rX")
		v.response.diffList = []string{"--- a/file\n+++ b/file\n-old\n+new"}
		v.viewport = viewport{browsing: true, anchor: historyPosition{line: 1, column: 2}}
		v.unseen = true
	}
	snap := u.snapshotPresentation()
	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	v, _ := persistenceUI(t)
	if err := v.RestorePresentation(data); err != nil {
		t.Fatal(err)
	}
	if v.activeAgent != u.activeAgent || !reflect.DeepEqual(v.drafts, u.drafts) || !v.verbose {
		t.Fatal("lost UI state")
	}
	for id, original := range u.views {
		restored := v.views[id]
		if !reflect.DeepEqual(original.display.Snapshot(), restored.display.Snapshot()) || !reflect.DeepEqual(original.display.ExportSnapshot(), restored.display.ExportSnapshot()) || !reflect.DeepEqual(original.response.diffList, restored.response.diffList) || original.viewport != restored.viewport || original.unseen != restored.unseen {
			t.Fatalf("lost view %s", id)
		}
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			fmt.Fprintln(u.views["main"].response, "streaming line")
		}
	}()
	for i := 0; i < 100; i++ {
		u.snapshotPresentation()
	}
	wg.Wait()
}

func TestResumeSwapsSavedTabsAndPreservesCurrentSession(t *testing.T) {
	u, _ := persistenceUI(t)
	store, err := session.Open(t.TempDir(), u.root)
	if err != nil {
		t.Fatal(err)
	}
	target, _ := persistenceUI(t)
	target.activeAgent = "agent-1"
	target.views["main"].display.AddLine("saved event and error")
	snap, lock, err := store.New()
	if err != nil {
		t.Fatal(err)
	}
	snap.Agents, _ = target.manager.(savedAgentController).SaveAgents()
	snap.Presentation, _ = json.Marshal(target.snapshotPresentation())
	snap.Saved = time.Now()
	snap.Preview = "saved preview"
	if err := store.Save(snap); err != nil {
		t.Fatal(err)
	}
	session.Release(lock)
	if err := u.EnableSessions(store, func(s session.Snapshot) (*UI, error) {
		v, _ := persistenceUI(t)
		return v, v.RestorePresentation(s.Presentation)
	}); err != nil {
		t.Fatal(err)
	}
	oldID := u.persistence.current.ID
	u.input.data <- '\r'
	u.resumeSession()
	defer func() { u.shutdownAgentManager(); u.closeSession() }()
	if u.persistence.current.ID != snap.ID || u.activeAgent != "agent-1" {
		t.Fatal("session was not switched")
	}
	if !strings.Contains(strings.Join(u.views["main"].display.Lines(), "\n"), "saved event and error") {
		t.Fatal("lost transcript")
	}
	if saved, err := store.Load(oldID); err != nil || saved.Left.IsZero() {
		t.Fatalf("previous session not archived: %v", err)
	}
}

func TestSessionPickerCancelsAfterBusyEntry(t *testing.T) {
	u, _ := persistenceUI(t)
	entries := []session.Entry{{Snapshot: session.Snapshot{ID: strings.Repeat("a", 32), Preview: "preview", Saved: time.Now()}, Busy: true}}
	u.input.data <- '\r'
	u.input.data <- ctrlC
	if _, accepted, err := u.selectSession(entries); accepted || err != nil {
		t.Fatalf("busy entry accepted: %v %v", accepted, err)
	}
}

func TestMatchingSessionIndicesFiltersUsefulSessionDetails(t *testing.T) {
	entries := []session.Entry{
		{Snapshot: session.Snapshot{ID: "a1b2c3d4", Preview: "Review the deployment plan"}},
		{Snapshot: session.Snapshot{ID: "e5f6g7h8", Preview: "Investigate a test failure"}},
	}
	if got := matchingSessionIndices(entries, "deployment"); !reflect.DeepEqual(got, []int{0}) {
		t.Fatalf("preview matches = %v", got)
	}
	if got := matchingSessionIndices(entries, "g7h8"); len(got) != 0 {
		t.Fatalf("opaque ID matched = %v", got)
	}
}

func TestSessionSelectorLineUsesTimeAgentsAndPreviewWithoutID(t *testing.T) {
	entry := session.Entry{Snapshot: session.Snapshot{
		ID:      "fead69f5000000000000000000000000",
		Saved:   time.Date(2026, time.September, 9, 14, 32, 0, 0, time.Local),
		Preview: "Fix terminal input flicker",
		Agents:  []session.SavedAgent{{}, {}},
	}}
	line := renderSessionLine(entry, true, 120, false)
	for _, want := range []string{"> ", "Sep 09 14:32", "2 agents", "Fix terminal input flicker"} {
		if !strings.Contains(line, want) {
			t.Fatalf("session line missing %q: %q", want, line)
		}
	}
	if strings.Contains(line, "fead69f5") {
		t.Fatalf("session ID leaked into selector: %q", line)
	}
}

func TestSessionSelectorHeaderShowsCtrlCLeaveHint(t *testing.T) {
	entries := []session.Entry{{Snapshot: session.Snapshot{ID: strings.Repeat("a", 32), Preview: "Saved work"}}}
	var output strings.Builder
	renderSessionSelector(&output, entries, []int{0}, 0, 0, 1, 80, "", false)
	if !strings.Contains(output.String(), selectorLeaveHint) {
		t.Fatalf("selector header = %q", output.String())
	}
}

func TestEmptyLaunchNotSaved(t *testing.T) {
	u, m := persistenceUI(t)
	m.agents = m.agents[:1]
	delete(u.views, "agent-1")
	for i := range m.agents {
		m.agents[i].State = json.RawMessage(`{"Messages":[{"Role":"system","Content":"system"}]}`)
	}
	store, _ := session.Open(t.TempDir(), u.root)
	if err := u.EnableSessions(store, nil); err != nil {
		t.Fatal(err)
	}
	defer u.closeSession()
	if err := u.saveSession(false); err != nil {
		t.Fatal(err)
	}
	entries, err := store.List()
	if err != nil || len(entries) != 0 {
		t.Fatalf("saved empty launch: %v %v", entries, err)
	}
}

func TestEmptySessionDoesNotSaveAfterPreviousSnapshot(t *testing.T) {
	u, m := persistenceUI(t)
	m.agents = m.agents[:1]
	delete(u.views, "agent-1")
	m.agents[0].State = json.RawMessage(`{"Messages":[{"Role":"system","Content":"system"}]}`)
	store, _ := session.Open(t.TempDir(), u.root)
	if err := u.EnableSessions(store, nil); err != nil {
		t.Fatal(err)
	}
	defer u.closeSession()

	// A prior save must not make a later empty session eligible for saving.
	u.persistence.current.Saved = time.Now().UTC()
	if err := u.saveSession(false); err != nil {
		t.Fatal(err)
	}
	entries, err := store.List()
	if err != nil || len(entries) != 0 {
		t.Fatalf("saved empty session after previous snapshot: %v %v", entries, err)
	}
}
