package tui

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"qcode/internal/session"
)

func TestSessionPickerDeleteAllDefaultsCancelAndIgnoresFilter(t *testing.T) {
	for _, cancel := range []string{"\r", "\x1b", "\x03", "confirm"} {
		t.Run(cancel, func(t *testing.T) {
			s, entries, options := sessionPickerFixture(t)
			if err := s.Rename(entries[0].ID, "one match"); err != nil {
				t.Fatal(err)
			}
			entries, _ = s.List()
			calls := 0
			options.deleteAllSessions = func() (bool, error) {
				calls++
				return false, s.DeleteAll()
			}
			keys := []string{"one match", "\x04"}
			if cancel == "confirm" {
				keys = append(keys, arrowDownSequence, "\r", "\x03")
			} else {
				keys = append(keys, cancel)
				if cancel != "\x03" {
					keys = append(keys, "\x04", "\x03")
				}
			}
			var output strings.Builder
			_, accepted, err := runSessionPicker(&sessionPickerKeys{keys: keys}, &output, entries, options)
			text := output.String()
			if err != nil || accepted || !strings.Contains(text, "Delete all sessions | 3 saved sessions in this workspace") || !strings.Contains(text, "> Cancel") || !strings.Contains(text, "including sessions hidden by the filter") {
				t.Fatal("bulk confirmation", accepted, err, text)
			}
			remaining, err := s.List()
			if err != nil {
				t.Fatal(err)
			}
			if cancel == "confirm" {
				if calls != 1 || len(remaining) != 0 || !strings.Contains(text, "No matching sessions") {
					t.Fatal("bulk deletion did not remove hidden entries", calls, remaining)
				}
			} else if calls != 0 || len(remaining) != 3 {
				t.Fatal("cancellation deleted sessions", calls, remaining)
			}
		})
	}
}

func TestSessionPickerDeleteAllAvailableWithNoMatchingEntriesAndShortViews(t *testing.T) {
	s, entries, options := sessionPickerFixture(t)
	options.deleteAllSessions = func() (bool, error) { return false, s.DeleteAll() }
	_, accepted, err := runSessionPicker(&sessionPickerKeys{keys: []string{"no matches", "\t", "\x04", arrowDownSequence, "\r", "\x03"}}, io.Discard, entries, options)
	if err != nil || accepted {
		t.Fatal("bulk action unavailable without a matching row", accepted, err)
	}
	if entries, err := s.List(); err != nil || len(entries) != 0 {
		t.Fatal("bulk action did not delete sessions", entries, err)
	}
	for height := 1; height <= 6; height++ {
		p := sessionPicker{entries: entries, mode: sessionPickerDeleteAll}
		for choice, want := range []string{"Cancel", "Delete all"} {
			p.confirmation = choice
			lines, _ := p.render(options, 100, height)
			if !strings.Contains(strings.Join(lines, "\n"), "> "+want) {
				t.Fatal("short bulk confirmation lost selection", height, want, lines)
			}
		}
	}
}

func TestDeleteAllSavedSessionsStartsFreshAndDeletesOtherEntries(t *testing.T) {
	u, old := persistenceUI(t)
	store, err := session.Open(t.TempDir(), u.root)
	if err != nil {
		t.Fatal(err)
	}
	if err := u.EnableSessions(store, nil, func(session.SavedAgent) (*UI, error) {
		v, _ := freshPersistenceUI(u)
		return v, nil
	}); err != nil {
		t.Fatal(err)
	}
	oldID := u.persistence.current.ID
	if err := u.saveSession(false); err != nil {
		t.Fatal(err)
	}
	other, _ := store.New()
	if err := store.Save(other); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{oldID, other.ID} {
		if err := store.Rename(id, "name"); err != nil {
			t.Fatal(err)
		}
	}
	for _, key := range []byte("\x04" + arrowDownSequence + "\r") {
		u.input.data <- key
	}
	u.resumeSession()
	defer u.shutdownAgentManager()
	if u.persistence.current.ID == oldID || u.manager == old || len(u.views) != 1 || u.activeAgent != "main" {
		t.Fatal("bulk deletion did not start fresh")
	}
	u.closeSession()
	if entries, err := store.List(); err != nil || len(entries) != 0 {
		t.Fatal("deleted or empty sessions were saved again", entries, err)
	}
	for _, id := range []string{oldID, other.ID} {
		if metadata, err := store.LoadMetadata(id); err != nil || metadata != (session.Metadata{}) {
			t.Fatal("metadata remains", metadata, err)
		}
	}
	output, _ := os.ReadFile(u.out.Name())
	if !strings.Contains(string(output), "All tabs, drafts, and conversation history will be cleared.") {
		t.Fatal("bulk confirmation did not warn about current session")
	}
}

func TestDeleteAllPreparationAndFilesystemFailuresRetainCurrentConversation(t *testing.T) {
	for _, failure := range []string{"busy", "prepare", "filesystem"} {
		t.Run(failure, func(t *testing.T) {
			u, old := persistenceUI(t)
			dir := t.TempDir()
			store, _ := session.Open(dir, u.root)
			var staged *persistenceManager
			if err := u.EnableSessions(store, nil, func(session.SavedAgent) (*UI, error) {
				v, m := freshPersistenceUI(u)
				staged = m
				if failure == "prepare" {
					return v, errors.New("prepare failed")
				}
				return v, nil
			}); err != nil {
				t.Fatal(err)
			}
			id := u.persistence.current.ID
			if err := u.saveSession(false); err != nil {
				t.Fatal(err)
			}
			other, _ := store.New()
			if err := store.Save(other); err != nil {
				t.Fatal(err)
			}
			if failure == "busy" {
				old.agents[1].Summary.Status = session.StatusRunning
			}
			if failure == "filesystem" {
				if err := store.Rename(other.ID, "name removed first"); err != nil {
					t.Fatal(err)
				}
				paths, _ := filepath.Glob(filepath.Join(dir, "*", "metadata"))
				path := filepath.Join(paths[0], other.ID+".pin.json")
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(path, "block-removal"), []byte("data"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			closed, err := u.deleteAllSessions()
			if err == nil || closed || u.manager != old || u.persistence.current.ID != id {
				t.Fatal("bulk failure lost conversation", closed, err)
			}
			for _, id := range []string{id, other.ID} {
				if _, err := store.Load(id); err != nil {
					t.Fatal("failure deleted protected snapshot", err)
				}
			}
			if staged != nil {
				select {
				case <-staged.events:
				default:
					t.Fatal("failed bulk replacement not disposed")
				}
			}
			old.Shutdown()
		})
	}
}

func TestDeleteAllKeepsUnsavedCurrentConversation(t *testing.T) {
	u, old := persistenceUI(t)
	store, _ := session.Open(t.TempDir(), u.root)
	if err := u.EnableSessions(store, nil); err != nil {
		t.Fatal(err)
	}
	other, _ := store.New()
	if err := store.Save(other); err != nil {
		t.Fatal(err)
	}
	id := u.persistence.current.ID
	closed, err := u.deleteAllSessions()
	if err != nil || closed || u.manager != old || u.persistence.current.ID != id {
		t.Fatal("unsaved current session was reset", closed, err)
	}
	old.Shutdown()
}

func TestSessionPickerDeleteAllFailureRefreshesListInline(t *testing.T) {
	s, entries, options := sessionPickerFixture(t)
	options.deleteAllSessions = func() (bool, error) {
		if err := s.Delete(entries[0].ID); err != nil {
			t.Fatal(err)
		}
		return false, errors.New("later removal failed")
	}
	var output strings.Builder
	_, accepted, err := runSessionPicker(&sessionPickerKeys{keys: []string{"\x04", arrowDownSequence, "\r", "\x03"}}, &output, entries, options)
	frames := strings.Split(output.String(), "\x1b[2;1H")
	failed := frames[len(frames)-2]
	if err != nil || accepted || !strings.Contains(failed, "Resume (2/2)") || !strings.Contains(failed, "Cannot delete all: later removal failed") || strings.Contains(failed, entries[0].Preview) {
		t.Fatal("bulk failure not refreshed inline", accepted, err, failed)
	}
}

func TestSessionPickerDeleteAllHotkeyCancellationKeepsFilterAndSelection(t *testing.T) {
	for _, cancel := range []string{"\r", "\x1b"} {
		_, entries, options := sessionPickerFixture(t)
		options.deleteAllSessions = func() (bool, error) { t.Fatal("cancellation deleted sessions"); return false, nil }
		var output strings.Builder
		id, accepted, err := runSessionPicker(&sessionPickerKeys{keys: []string{"automatic preview", arrowDownSequence, "\x04", "\x04", cancel, "\r"}}, &output, entries, options)
		if err != nil || !accepted || id != entries[1].ID || !strings.Contains(output.String(), "Filter: automatic preview") {
			t.Fatal("bulk cancellation did not return to the same list selection", id, accepted, err)
		}
		if strings.Contains(output.String(), "Session actions") || strings.Count(output.String(), "Delete all sessions") != 2 {
			t.Fatal("hotkey reopened or skipped confirmation", output.String())
		}
	}
}

func TestSessionPickerDeleteAllHotkeyHintAndScope(t *testing.T) {
	s, entries, options := sessionPickerFixture(t)
	p := sessionPicker{entries: entries, matches: matchingSessionIndices(entries, "")}
	for _, width := range []int{80, 120} {
		lines, _ := p.render(options, width, 18)
		for _, hint := range []string{"Ctrl+D delete all", "Tab actions", "Esc back", "Ctrl+C cancel"} {
			if !strings.Contains(lines[len(lines)-1], hint) || visibleWidth(lines[len(lines)-1]) > width {
				t.Fatal("footer hotkey hint does not fit", width, lines[len(lines)-1])
			}
		}
	}
	p.mode = sessionPickerActions
	lines, _ := p.render(options, 120, 18)
	if strings.Contains(strings.Join(lines, "\n"), "Delete all") {
		t.Fatal("bulk deletion still appears in per-session actions", lines)
	}
	options.deleteAllSessions = func() (bool, error) { t.Fatal("hotkey acted outside list"); return false, nil }
	var output strings.Builder
	_, _, err := runSessionPicker(&sessionPickerKeys{keys: []string{"\t", "\x04", "\r", "\x04", "label", "\r", "\x03"}}, &output, entries, options)
	metadata, metadataErr := s.LoadMetadata(entries[0].ID)
	if err != nil || metadataErr != nil || metadata.Name != "label" || strings.Contains(output.String(), "Delete all sessions") {
		t.Fatal("hotkey interfered with actions or rename editing", metadata, err, metadataErr)
	}
}
