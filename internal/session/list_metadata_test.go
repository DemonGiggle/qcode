package session

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// redactMarkerFilter mimics production snapshot filters: it redacts a marker
// from the preview while tolerating metadata-only snapshots (no
// Agents/Presentation/Work), like filterSessionSnapshot in cmd/qcode.
func redactMarkerFilter(seen *[]Snapshot) SnapshotFilter {
	return func(snap Snapshot) (Snapshot, error) {
		if seen != nil {
			*seen = append(*seen, snap)
		}
		snap.Preview = strings.ReplaceAll(snap.Preview, "secret", "[redacted]")
		return snap, nil
	}
}

func TestListRedactsPreviewAndKeepsMetadataOrdering(t *testing.T) {
	s, err := Open(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	newSnap := func(preview string, recency time.Time, agents int) Snapshot {
		t.Helper()
		snap, err := s.New()
		if err != nil {
			t.Fatal(err)
		}
		snap.Preview = preview
		snap.Recency = recency
		snap.Saved = recency
		for i := 0; i < agents; i++ {
			snap.Agents = append(snap.Agents, SavedAgent{
				Summary: Summary{ID: "agent", CurrentTask: "secret agent task"},
				State:   []byte(`{"Messages":[{"Role":"user","Content":"secret agent state"}]}`),
			})
		}
		snap.Presentation = []byte(`{"Views":[{"History":{"Lines":["secret terminal history"]}}]}`)
		snap.Work = &WorkHistory{Records: []WorkRecord{{Prompt: "secret work prompt"}}}
		// Save before any filter is installed so the file on disk still holds
		// the raw payload, like a session written before redaction existed.
		if err := s.Save(snap); err != nil {
			t.Fatal(err)
		}
		return snap
	}
	now := time.Now().UTC()
	pinned := newSnap("pinned secret preview", now.Add(-2*time.Hour), 2)
	newest := newSnap("plain preview", now, 0)
	oldest := newSnap("older secret preview", now.Add(-3*time.Hour), 1)
	if err := s.SetPinned(pinned.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := s.Rename(pinned.ID, "Pinned Name"); err != nil {
		t.Fatal(err)
	}

	var seen []Snapshot
	s.SetSnapshotFilter(redactMarkerFilter(&seen))
	entries, err := s.List()
	if err != nil || len(entries) != 3 {
		t.Fatalf("listing: %+v, %v", entries, err)
	}
	// Pinned first, then resume recency descending.
	if entries[0].ID != pinned.ID || entries[1].ID != newest.ID || entries[2].ID != oldest.ID {
		t.Fatalf("ordering: %+v", entries)
	}
	if entries[0].Metadata != (Metadata{Name: "Pinned Name", Pinned: true}) {
		t.Fatalf("metadata: %+v", entries[0].Metadata)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Preview, "secret") {
			t.Fatalf("raw payload surfaced in preview: %+v", entry)
		}
		if entry.Problem != "" || entry.MetadataProblem != "" {
			t.Fatalf("healthy entry marked: %+v", entry)
		}
	}
	if entries[0].Preview != "pinned [redacted] preview" || entries[1].Preview != "plain preview" {
		t.Fatalf("previews: %+v", entries)
	}
	// The filter only ever saw metadata: no agent, presentation, or work
	// payload reached it, even for the session that has them on disk.
	if len(seen) != 3 {
		t.Fatalf("filter calls: %d", len(seen))
	}
	for _, snap := range seen {
		if len(snap.Agents) != 0 || len(snap.Presentation) != 0 || snap.Work != nil {
			t.Fatalf("filter saw payload: %+v", snap)
		}
	}
	// Tab counts survive for the picker ("N agents") and remote catalog
	// without retaining any checkpoint bytes.
	if len(entries[0].Agents) != 2 || len(entries[1].Agents) != 0 || len(entries[2].Agents) != 1 {
		t.Fatalf("agent counts: %d %d %d", len(entries[0].Agents), len(entries[1].Agents), len(entries[2].Agents))
	}
	for _, agent := range entries[0].Agents {
		if !reflect.DeepEqual(agent, SavedAgent{}) {
			t.Fatalf("placeholder carried state: %+v", agent)
		}
	}
	if entries[0].Presentation != nil || entries[0].Work != nil {
		t.Fatalf("payload materialized: %+v", entries[0].Snapshot)
	}
	// The full snapshot is untouched and still resumable in full.
	loaded, err := s.Load(pinned.ID)
	if err != nil || len(loaded.Agents) != 2 || loaded.Work == nil || len(loaded.Presentation) == 0 {
		t.Fatalf("full snapshot lost: %+v, %v", loaded, err)
	}
	if !strings.Contains(string(loaded.Agents[0].State), "secret agent state") {
		t.Fatalf("checkpoint changed: %+v", loaded)
	}
}

func TestListIsolatesUnreadableSnapshots(t *testing.T) {
	s, err := Open(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	healthy, err := s.New()
	if err != nil {
		t.Fatal(err)
	}
	healthy.Preview = "healthy"
	healthy.Saved = time.Now().UTC()
	if err := s.Save(healthy); err != nil {
		t.Fatal(err)
	}

	broken, err := s.New()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.dir, broken.ID+".json"), []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	foreign, err := s.New()
	if err != nil {
		t.Fatal(err)
	}
	// Valid JSON carrying another workspace's secret preview: incompatible,
	// and the rejected preview must never surface.
	if err := os.WriteFile(filepath.Join(s.dir, foreign.ID+".json"),
		[]byte(`{"Version":1,"ID":"`+foreign.ID+`","Workspace":"elsewhere","Preview":"secret foreign preview"}`), 0600); err != nil {
		t.Fatal(err)
	}

	entries, err := s.List()
	if err != nil || len(entries) != 3 {
		t.Fatalf("corrupt files hid sessions: %+v, %v", entries, err)
	}
	byID := map[string]Entry{}
	for _, entry := range entries {
		byID[entry.ID] = entry
	}
	if byID[healthy.ID].Problem != "" || byID[healthy.ID].Preview != "healthy" {
		t.Fatalf("healthy session changed: %+v", byID[healthy.ID])
	}
	for _, id := range []string{broken.ID, foreign.ID} {
		entry := byID[id]
		if entry.Problem == "" {
			t.Fatalf("unreadable snapshot listed as healthy: %+v", entry)
		}
		if entry.Preview != "" || len(entry.Agents) != 0 || entry.Presentation != nil || entry.Work != nil {
			t.Fatalf("rejected payload exposed: %+v", entry)
		}
		if strings.Contains(entry.Problem, "secret") {
			t.Fatalf("problem leaked payload: %+v", entry)
		}
		info, err := os.Stat(filepath.Join(s.dir, id+".json"))
		if err != nil {
			t.Fatal(err)
		}
		if !entry.Saved.Equal(info.ModTime()) {
			t.Fatalf("saved time not file ModTime: %v vs %v", entry.Saved, info.ModTime())
		}
	}
}

func TestListWithoutFilterPassesPreviewThrough(t *testing.T) {
	s, err := Open(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	snap, err := s.New()
	if err != nil {
		t.Fatal(err)
	}
	snap.Preview = "raw secret preview"
	snap.Recency = time.Now().UTC()
	snap.Agents = []SavedAgent{{State: []byte(`{"Messages":[]}`)}}
	if err := s.Save(snap); err != nil {
		t.Fatal(err)
	}
	entries, err := s.List()
	if err != nil || len(entries) != 1 {
		t.Fatalf("listing: %+v, %v", entries, err)
	}
	if entries[0].Preview != "raw secret preview" {
		t.Fatalf("preview changed without filter: %+v", entries[0])
	}
	if len(entries[0].Agents) != 1 || entries[0].Agents[0].State != nil {
		t.Fatalf("payload materialized without filter: %+v", entries[0].Snapshot)
	}
}

func TestListMarksFilterRejectedEntriesWithoutExposingData(t *testing.T) {
	s, err := Open(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	snap, err := s.New()
	if err != nil {
		t.Fatal(err)
	}
	snap.Preview = "blocked preview with secret"
	if err := s.Save(snap); err != nil {
		t.Fatal(err)
	}
	if err := s.Rename(snap.ID, "kept name"); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("policy rejects everything")
	s.SetSnapshotFilter(func(Snapshot) (Snapshot, error) { return Snapshot{}, failure })

	entries, err := s.List()
	if err != nil || len(entries) != 1 {
		t.Fatalf("rejection hid session: %+v, %v", entries, err)
	}
	entry := entries[0]
	if entry.ID != snap.ID {
		t.Fatalf("identity lost: %+v", entry)
	}
	// The rejected preview never surfaces: the entry keeps its ID, the
	// metadata name, and a generic problem instead of payload.
	if entry.Preview != "" || strings.Contains(entry.Problem, "secret") || strings.Contains(entry.Problem, "blocked") {
		t.Fatalf("rejected payload exposed: %+v", entry)
	}
	if entry.Problem == "" {
		t.Fatalf("rejected entry listed as healthy: %+v", entry)
	}
	if entry.Metadata != (Metadata{Name: "kept name"}) || entry.MetadataProblem != "" {
		t.Fatalf("metadata lost on rejection: %+v", entry)
	}
}
