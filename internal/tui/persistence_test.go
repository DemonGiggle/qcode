package tui

import (
	"context"
	"encoding/json"
	"errors"
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
	agents    []session.SavedAgent
	events    chan session.Event
	once      sync.Once
	work      *session.WorkHistory
	submitErr error
}

func (m *persistenceManager) Submit(string, string) (session.Submission, error) {
	return session.Submission{}, m.submitErr
}

type persistencePromptManager struct{ *persistenceManager }

func (m *persistencePromptManager) SubmitPrompt(input session.PromptSubmission) (session.Submission, error) {
	return m.Submit(input.AgentID, input.Prompt)
}
func (*persistencePromptManager) PendingInputs(string) []session.QueuedPrompt { return nil }
func (*persistencePromptManager) CancelInput(string, string) error            { return nil }

func (m *persistenceManager) SaveWorkHistory() *session.WorkHistory { return m.work }
func (m *persistenceManager) ConsultationEvents(after uint64) []session.ConsultationEvent {
	if m.work == nil || after >= uint64(len(m.work.Events)) {
		return nil
	}
	return append([]session.ConsultationEvent(nil), m.work.Events[after:]...)
}

func TestConsultationEventsReplayAndPersistWithoutDuplicates(t *testing.T) {
	u, m := persistenceUI(t)
	m.work = &session.WorkHistory{NextRequestID: 1, Events: []session.ConsultationEvent{
		{Sequence: 1, AgentID: "agent-1", RequestID: "request-1", Status: "timed_out", Error: "deadline exceeded"},
		{Sequence: 2, AgentID: "missing", Status: "failed", Error: "unknown agent"},
	}}
	m.events = make(chan session.Event, 32)
	for i := 0; i < cap(m.events); i++ {
		m.events <- session.Event{Consultation: true}
	}
	u.SetAgentManager(m)
	u.shutdownAgentManager()
	lines := strings.Join(u.views["main"].display.Lines(), "\n")
	if strings.Count(lines, "deadline exceeded") != 1 || strings.Count(lines, "unknown agent") != 1 {
		t.Fatalf("missing or duplicated events: %s", lines)
	}
	store, err := session.Open(t.TempDir(), u.root)
	if err != nil {
		t.Fatal(err)
	}
	if err := u.EnableSessions(store, nil); err != nil {
		t.Fatal(err)
	}
	defer u.closeSession()
	if err := u.saveSession(false); err != nil {
		t.Fatal(err)
	}
	saved, err := store.Load(u.persistence.current.ID)
	if err != nil || saved.Work == nil || len(saved.Work.Events) != 2 {
		t.Fatalf("missing durable events: %+v %v", saved.Work, err)
	}
	v, n := persistenceUI(t)
	n.work = saved.Work
	if err := v.RestorePresentation(saved.Presentation); err != nil {
		t.Fatal(err)
	}
	v.replayConsultationEvents()
	if got := strings.Join(v.views["main"].display.Lines(), "\n"); got != lines {
		t.Fatal("resume duplicated old events")
	}
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
	target.steeringCursor = 2
	u.steeringCursor = 9
	target.views["main"].display.AddLine("saved event and error")
	snap, lock, err := store.New()
	if err != nil {
		t.Fatal(err)
	}
	snap.Agents, _ = target.manager.(savedAgentController).SaveAgents()
	snap.Presentation, _ = json.Marshal(target.snapshotPresentation())
	snap.Saved = time.Now().Add(-time.Hour)
	snap.Left = snap.Saved.Add(time.Minute)
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
	if !u.persistence.current.Recency.Equal(snap.Left) {
		t.Fatalf("resume recency = %v, want legacy departure %v", u.persistence.current.Recency, snap.Left)
	}
	if err := u.saveSession(false); err != nil {
		t.Fatal(err)
	}
	if saved, err := store.Load(snap.ID); err != nil || !saved.Recency.Equal(snap.Left) {
		t.Fatalf("resume checkpoint changed legacy recency: %+v, %v", saved, err)
	}
	if u.steeringCursor != target.steeringCursor {
		t.Fatalf("steering cursor = %d, want restored cursor %d", u.steeringCursor, target.steeringCursor)
	}
	if !strings.Contains(strings.Join(u.views["main"].display.Lines(), "\n"), "saved event and error") {
		t.Fatal("lost transcript")
	}
	if saved, err := store.Load(oldID); err != nil || saved.Left.IsZero() {
		t.Fatalf("previous session not archived: %v", err)
	}
	assertSessions := func(currentID string) {
		t.Helper()
		u.persistence.mu.Lock()
		entries, err := u.persistence.listSessionsLocked()
		u.persistence.mu.Unlock()
		if err != nil || len(entries) != 2 || entries[0].ID != oldID || entries[1].ID != snap.ID {
			t.Fatalf("session switch changed list: %+v, %v", entries, err)
		}
		for _, entry := range entries {
			if entry.Current != (entry.ID == currentID) || entry.Busy {
				t.Fatalf("incorrect current session marker: %+v", entry)
			}
		}
	}
	assertSessions(snap.ID)
	u.resumeSessionID(oldID)
	assertSessions(oldID)
	manager, ownedLock := u.manager, u.persistence.lock
	u.resumeSessionID(oldID)
	u.input.data <- '\r'
	u.resumeSession()
	if u.manager != manager || u.persistence.lock != ownedLock {
		t.Fatal("selecting the current session reloaded it")
	}
	assertSessions(oldID)
}

func TestSessionOrderChangesOnlyAfterAcceptedUserPrompt(t *testing.T) {
	for _, source := range []string{"terminal", "terminal-fallback", "browser"} {
		for _, intent := range []session.SubmissionIntent{session.IntentAutomatic, session.IntentQueue, session.IntentSteer} {
			if source == "terminal-fallback" && intent != session.IntentAutomatic {
				continue
			}
			for _, rejected := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/rejected=%v", source, intent, rejected), func(t *testing.T) {
					u, m := persistenceUI(t)
					if source != "terminal-fallback" {
						u.SetDetachedAgentManager(&persistencePromptManager{m})
					}
					store, err := session.Open(t.TempDir(), u.root)
					if err != nil {
						t.Fatal(err)
					}
					if err := u.EnableSessions(store, nil); err != nil {
						t.Fatal(err)
					}
					defer u.closeSession()
					currentID := u.persistence.current.ID
					legacyTime := time.Now().UTC().Add(-2 * time.Hour)
					u.persistence.current.Saved = legacyTime
					newer, lock, err := store.New()
					if err != nil {
						t.Fatal(err)
					}
					defer session.Release(lock)
					newer.Saved = legacyTime.Add(time.Hour)
					if err := store.Save(newer); err != nil {
						t.Fatal(err)
					}
					assertOrder := func(first, second string) {
						t.Helper()
						entries, err := store.List()
						if err != nil || len(entries) != 2 {
							t.Fatalf("list = %+v, %v", entries, err)
						}
						if entries[0].ID != first || entries[1].ID != second {
							t.Fatalf("order = [%s, %s], want [%s, %s]", entries[0].ID, entries[1].ID, first, second)
						}
					}
					if err := u.saveSession(false); err != nil {
						t.Fatal(err)
					}
					assertOrder(newer.ID, currentID)
					// Drafts, output, settings, and departure still checkpoint without
					// moving the session ahead of more recently prompted sessions.
					u.drafts["main"] = "unfinished draft"
					u.views["main"].display.AddLine("passive output")
					u.verbose = true
					m.work = &session.WorkHistory{Records: []session.WorkRecord{{
						AgentID: "agent-1", Source: "delegation", Created: time.Now().UTC(),
					}}}
					if err := u.saveSession(true); err != nil {
						t.Fatal(err)
					}
					assertOrder(newer.ID, currentID)
					if rejected {
						m.submitErr = errors.New("prompt rejected")
					}
					if intent == session.IntentQueue {
						u.activeAgent = "agent-1"
					}
					if source == "browser" {
						_, err = u.SubmitRemotePrompt("test-browser", session.PromptSubmission{AgentID: u.activeAgent, Prompt: "continue the task", Intent: intent})
					} else {
						u.submissionIntent = intent
						err = u.runActiveTask(context.Background(), "continue the task")
					}
					if !errors.Is(err, m.submitErr) {
						t.Fatalf("submission error = %v, want %v", err, m.submitErr)
					}
					if err := u.saveSession(false); err != nil {
						t.Fatal(err)
					}
					if rejected {
						assertOrder(newer.ID, currentID)
					} else {
						assertOrder(currentID, newer.ID)
					}
				})
			}
		}
	}
}

func TestSessionPickerRetriesUnavailableEntries(t *testing.T) {
	for _, entry := range []session.Entry{
		{Snapshot: session.Snapshot{ID: strings.Repeat("a", 32), Preview: "preview", Saved: time.Now()}, Busy: true},
		{Snapshot: session.Snapshot{ID: strings.Repeat("b", 32)}, Problem: "unreadable snapshot"},
	} {
		u, _ := persistenceUI(t)
		u.input.data <- '\r'
		u.input.data <- ctrlC
		id, accepted, err := u.selectSession([]session.Entry{entry})
		if err != nil || !accepted || id != entry.ID {
			t.Fatalf("Enter did not request a fresh restore attempt: id=%q accepted=%v err=%v", id, accepted, err)
		}
	}
}

func TestResumeBusySessionReportsErrorAndPreservesCurrentSession(t *testing.T) {
	u, _ := persistenceUI(t)
	store, err := session.Open(t.TempDir(), u.root)
	if err != nil {
		t.Fatal(err)
	}
	snap, lock, err := store.New()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Release(lock)
	snap.Preview = "session still open elsewhere"
	if err := store.Save(snap); err != nil {
		t.Fatal(err)
	}
	if err := u.EnableSessions(store, func(session.Snapshot) (*UI, error) {
		t.Fatal("attempted to build a session without acquiring its lock")
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	defer u.closeSession()
	currentID := u.persistence.current.ID
	u.input.data <- '\r'
	u.input.data <- ctrlC
	u.resumeSession()
	if u.persistence.current.ID != currentID {
		t.Fatal("busy session replaced the current session")
	}
	if !strings.Contains(strings.Join(u.display.Lines(), "\n"), "session is open in another process") {
		t.Fatal("Enter silently ignored the busy session")
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
		Saved:   time.Now(),
		Recency: time.Date(2026, time.September, 9, 14, 32, 0, 0, time.Local),
		Preview: "Fix terminal input flicker",
		Agents:  []session.SavedAgent{{}, {}},
	}, Current: true}
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
