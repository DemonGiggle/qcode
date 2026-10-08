package tui

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"qcode/internal/session"
)

func TestDeleteCurrentSessionSerializesBrowserPromptWithHandoff(t *testing.T) {
	u, old := persistenceUI(t)
	old.submitErr = errors.New("submitted to departing manager")
	u.SetDetachedAgentManager(&persistencePromptManager{old})
	store, err := session.Open(t.TempDir(), u.root)
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	if err := u.EnableSessions(store, nil, func(session.SavedAgent) (*UI, error) {
		close(entered)
		<-release
		v, fresh := freshPersistenceUI(u)
		v.SetDetachedAgentManager(&persistencePromptManager{fresh})
		return v, nil
	}); err != nil {
		t.Fatal(err)
	}
	oldID := u.persistence.current.ID
	if err := u.saveSession(false); err != nil {
		t.Fatal(err)
	}
	deleted, submitted := make(chan error, 1), make(chan error, 1)
	go func() { _, err := u.deleteSession(oldID); deleted <- err }()
	<-entered
	go func() {
		_, err := u.SubmitRemotePrompt("browser", session.PromptSubmission{AgentID: "main", Prompt: "new task"})
		submitted <- err
	}()
	close(release)
	if err := <-deleted; err != nil {
		t.Fatal(err)
	}
	if err := <-submitted; err != nil {
		t.Fatal("prompt did not wait for replacement manager", err)
	}
	u.shutdownAgentManager()
	if u.persistence.current.ID == oldID || u.persistence.current.Recency.IsZero() {
		t.Fatal("prompt recency was applied to departing session")
	}
}

func freshPersistenceUI(u *UI) (*UI, *persistenceManager) {
	v := New(u.in, u.out, statusRunner{}, "test", "fresh-model", u.root)
	v.AddAgentView("main", "test", "fresh-model")
	m := &persistenceManager{events: make(chan session.Event), agents: []session.SavedAgent{{
		Summary: session.Summary{ID: "main", Model: "fresh-model", Status: session.StatusIdle},
		State:   json.RawMessage(`{"Messages":[{"Role":"system","Content":"fresh system"}]}`),
	}}}
	v.SetDetachedAgentManager(m)
	return v, m
}

func TestDeleteCurrentSessionPickerReturnsFreshComposerAndOnlySavesNewID(t *testing.T) {
	u, old := persistenceUI(t)
	u.activeAgent, u.verbose = "agent-1", true
	u.drafts["main"], u.drafts["agent-1"] = "main draft", "tab draft"
	u.views["main"].display.AddLine("old transcript")
	u.consultationCursor, u.steeringCursor = 7, 9
	u.queue.expanded, u.pendingTab = true, 1
	store, err := session.Open(t.TempDir(), u.root)
	if err != nil {
		t.Fatal(err)
	}
	var replacement *persistenceManager
	if err := u.EnableSessions(store, nil, func(main session.SavedAgent) (*UI, error) {
		if !reflect.DeepEqual(main, old.agents[0]) {
			t.Fatal("builder did not receive main checkpoint", main)
		}
		v, m := freshPersistenceUI(u)
		replacement = m
		return v, nil
	}); err != nil {
		t.Fatal(err)
	}
	oldID := u.persistence.current.ID
	if err := u.saveSession(false); err != nil {
		t.Fatal(err)
	}
	if err := store.Rename(oldID, "delete current"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetPinned(oldID, true); err != nil {
		t.Fatal(err)
	}
	for _, key := range []byte("\t" + arrowUpSequence + "\r" + arrowDownSequence + "\r") {
		u.input.data <- key
	}
	u.resumeSession()
	defer u.shutdownAgentManager()
	newID := u.persistence.current.ID
	if newID == oldID || u.activeAgent != "main" || len(u.views) != 1 || len(u.drafts) != 0 || u.consultationCursor != 0 || u.steeringCursor != 0 || u.pendingTab != 0 || u.queue.expanded || !u.verbose || u.inputLabel != inputPrompt || u.model != "fresh-model" {
		t.Fatal("fresh composer retained departing state")
	}
	select {
	case <-old.events:
	default:
		t.Fatal("departing manager was not shut down")
	}
	if u.persistence.lastContent != "" || u.persistence.lastError != "" || u.persistence.current.Preview != "" || !u.persistence.current.Saved.IsZero() || !u.persistence.current.Left.IsZero() {
		t.Fatal("autosave bookkeeping not reset", u.persistence.current)
	}
	if lines := strings.Join(u.display.Lines(), "\n"); strings.Contains(lines, "old transcript") {
		t.Fatal("old transcript still shown", lines)
	}
	if _, err := store.Load(oldID); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("departing session saved again", err)
	}
	if metadata, err := store.LoadMetadata(oldID); err != nil || metadata != (session.Metadata{}) {
		t.Fatal("departing metadata remains", metadata, err)
	}
	// Queued autosave requests and clean exit cannot persist an empty replacement.
	if err := u.saveSession(false); err != nil {
		t.Fatal(err)
	}
	u.closeSession()
	if entries, err := store.List(); err != nil || len(entries) != 0 {
		t.Fatal("empty replacement was persisted", entries, err)
	}
	replacement.agents[0].State = json.RawMessage(`{"Messages":[{"Role":"system","Content":"fresh system"},{"Role":"user","Content":"new conversation"}]}`)
	if err := u.saveSession(false); err != nil {
		t.Fatal(err)
	}
	u.closeSession()
	entries, err := store.List()
	if err != nil || len(entries) != 1 || entries[0].ID != newID || entries[0].Left.IsZero() || entries[0].Preview != "new conversation" {
		t.Fatal("autosave/exit did not use only replacement ID", entries, err)
	}
}

func TestDeleteCurrentSessionGuardsAndPreparationFailuresKeepConversation(t *testing.T) {
	for _, failure := range []string{"running main", "running tab", "waiting tab", "no builder", "missing checkpoint", "prepare", "invalid replacement", "filesystem"} {
		t.Run(failure, func(t *testing.T) {
			u, old := persistenceUI(t)
			dir := t.TempDir()
			store, err := session.Open(dir, u.root)
			if err != nil {
				t.Fatal(err)
			}
			u.drafts["main"] = "keep draft"
			u.views["main"].display.AddLine("keep history")
			var staged *persistenceManager
			builds := 0
			builder := func(session.SavedAgent) (*UI, error) {
				builds++
				v, m := freshPersistenceUI(u)
				staged = m
				if failure == "prepare" {
					return v, errors.New("builder failed")
				}
				if failure == "invalid replacement" {
					v.AddAgentView("agent-1", "test", "extra")
				}
				return v, nil
			}
			if failure == "no builder" {
				builder = nil
			}
			if err := u.EnableSessions(store, nil, builder); err != nil {
				t.Fatal(err)
			}
			id := u.persistence.current.ID
			if err := u.saveSession(false); err != nil {
				t.Fatal(err)
			}
			switch failure {
			case "running main":
				old.agents[0].Summary.Status = session.StatusRunning
			case "running tab":
				old.agents[1].Summary.Status = session.StatusRunning
			case "waiting tab":
				old.agents[1].Summary.Status = session.StatusWaitingForApproval
			case "missing checkpoint":
				old.agents = old.agents[1:]
			case "filesystem":
				if err := store.Rename(id, "name removed first"); err != nil {
					t.Fatal(err)
				}
				paths, _ := filepath.Glob(filepath.Join(dir, "*", "metadata"))
				path := filepath.Join(paths[0], id+".pin.json")
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(path, "block-removal"), []byte("data"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			closed, err := u.deleteSession(id)
			if err == nil || closed || u.persistence.current.ID != id || u.manager != old || u.drafts["main"] != "keep draft" || !strings.Contains(strings.Join(u.views["main"].display.Lines(), "\n"), "keep history") {
				t.Fatal("failure lost conversation", closed, err)
			}
			if _, err := store.Load(id); err != nil {
				t.Fatal("preparation failure deleted snapshot", err)
			}
			if staged != nil {
				select {
				case <-staged.events:
				default:
					t.Fatal("failed replacement manager was not disposed")
				}
			} else if builds != 0 {
				t.Fatal("builder ran before guard")
			}
			if failure == "filesystem" {
				entries, err := store.List()
				if err != nil || len(entries) != 1 || entries[0].Name != "" || entries[0].MetadataProblem == "" {
					t.Fatal("partial metadata removal not reflected", entries, err)
				}
			}
			old.Shutdown()
		})
	}
}

func TestDeleteOtherSessionWhileCurrentAgentBusy(t *testing.T) {
	u, old := persistenceUI(t)
	old.agents[0].Summary.Status = session.StatusRunning
	store, err := session.Open(t.TempDir(), u.root)
	if err != nil {
		t.Fatal(err)
	}
	if err := u.EnableSessions(store, nil); err != nil {
		t.Fatal(err)
	}
	other, _ := store.New()
	if err := store.Save(other); err != nil {
		t.Fatal(err)
	}
	id := u.persistence.current.ID
	closed, err := u.deleteSession(other.ID)
	if err != nil || closed || u.persistence.current.ID != id || u.manager != old {
		t.Fatal("could not delete another session while busy", closed, err)
	}
	old.Shutdown()
}

func TestDeleteCurrentSessionSerializesConcurrentAutosaves(t *testing.T) {
	u, _ := persistenceUI(t)
	store, err := session.Open(t.TempDir(), u.root)
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	if err := u.EnableSessions(store, nil, func(session.SavedAgent) (*UI, error) {
		close(entered)
		<-release
		v, _ := freshPersistenceUI(u)
		return v, nil
	}); err != nil {
		t.Fatal(err)
	}
	oldID := u.persistence.current.ID
	if err := u.saveSession(false); err != nil {
		t.Fatal(err)
	}
	deleted := make(chan error, 1)
	go func() { _, err := u.deleteSession(oldID); deleted <- err }()
	<-entered
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); u.requestSessionSave(); u.closeSession() }()
	}
	close(release)
	if err := <-deleted; err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	u.shutdownAgentManager()
	if entries, err := store.List(); err != nil || len(entries) != 0 {
		t.Fatal("concurrent save recreated deleted or empty session", entries, err)
	}
}
