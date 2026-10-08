package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"

	"qcode/internal/redaction"
)

func TestMetadataPersistsIndependentlyAndSortsWithoutChangingRecency(t *testing.T) {
	dir, workspace := t.TempDir(), t.TempDir()
	s, err := Open(dir, workspace)
	if err != nil {
		t.Fatal(err)
	}
	other, err := Open(dir, workspace)
	if err != nil {
		t.Fatal(err)
	}
	var snapshots []Snapshot
	for i := 0; i < 4; i++ {
		snap, err := s.New()
		if err != nil {
			t.Fatal(err)
		}
		snap.Preview = fmt.Sprintf("automatic %d", i)
		snap.Recency = time.Now().UTC().Add(time.Duration(i) * time.Hour)
		if err := s.Save(snap); err != nil {
			t.Fatal(err)
		}
		snapshots = append(snapshots, snap)
	}
	if metadata, err := s.LoadMetadata(snapshots[0].ID); err != nil || metadata != (Metadata{}) {
		t.Fatalf("legacy defaults: %+v, %v", metadata, err)
	}
	before, _ := os.ReadFile(filepath.Join(s.dir, snapshots[0].ID+".json"))
	if err := s.Rename(snapshots[0].ID, "  設計 😀  "); err != nil {
		t.Fatal(err)
	}
	if err := other.SetPinned(snapshots[0].ID, true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPinned(snapshots[1].ID, true); err != nil {
		t.Fatal(err)
	}
	if err := other.Rename(snapshots[0].ID, "新名稱"); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(s.dir, snapshots[0].ID+".json"))
	if !bytes.Equal(before, after) {
		t.Fatal("metadata edit rewrote snapshot or recency")
	}
	if metadata, err := s.LoadMetadata(snapshots[0].ID); err != nil || metadata != (Metadata{Name: "新名稱", Pinned: true}) {
		t.Fatalf("independent updates: %+v, %v", metadata, err)
	}
	entries, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []int{1, 0, 3, 2} {
		if entries[i].ID != snapshots[want].ID {
			t.Fatalf("pin ordering: %+v", entries)
		}
	}
	// Stale autosaves and sanitized resume rewrites never touch metadata.
	if err := other.Save(snapshots[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := other.LoadForResume(snapshots[0].ID); err != nil {
		t.Fatal(err)
	}
	restarted, _ := Open(dir, workspace)
	if metadata, err := restarted.LoadMetadata(snapshots[0].ID); err != nil || metadata != entries[1].Metadata {
		t.Fatalf("metadata lost across restart/resume: %+v, %v", metadata, err)
	}
	for _, field := range []string{"name", "pin"} {
		info, err := os.Stat(filepath.Join(s.dir, "metadata", snapshots[0].ID+"."+field+".json"))
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
			t.Fatal("metadata permissions", info.Mode())
		}
	}
	info, _ := os.Stat(filepath.Join(s.dir, "metadata"))
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0700 {
		t.Fatal("metadata directory permissions", info.Mode())
	}
	if err := other.SetPinned(snapshots[0].ID, false); err != nil {
		t.Fatal(err)
	}
	if err := s.Rename(snapshots[0].ID, " \t\n "); err != nil {
		t.Fatal(err)
	}
	if metadata, err := restarted.LoadMetadata(snapshots[0].ID); err != nil || metadata != (Metadata{}) {
		t.Fatalf("cleared defaults: %+v, %v", metadata, err)
	}
	entries, _ = s.List()
	if entries[0].ID != snapshots[1].ID || entries[3].ID != snapshots[0].ID {
		t.Fatal("unpin ordering", entries)
	}
}

func TestMetadataRedactionValidationAndWriteFailures(t *testing.T) {
	s, _ := Open(t.TempDir(), t.TempDir())
	snap, _ := s.New()
	if err := s.Save(snap); err != nil {
		t.Fatal(err)
	}
	policy, _ := redaction.New(redaction.Config{}, []string{"private-name"})
	s.SetDefaultNameFilter(func(name string) string { return policy.Text(redaction.Persistence, name) })
	if err := s.Rename(snap.ID, " private-name password=secret "); err != nil {
		t.Fatal(err)
	}
	namePath := filepath.Join(s.dir, "metadata", snap.ID+".name.json")
	before, _ := os.ReadFile(namePath)
	if bytes.Contains(before, []byte("private-name")) || bytes.Contains(before, []byte("secret")) {
		t.Fatal("name not redacted", string(before))
	}
	if err := s.Rename(snap.ID, string([]byte{255})); err == nil {
		t.Fatal("invalid UTF-8 accepted")
	}
	for _, id := range []string{"../escape", "", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"} {
		if err := s.Rename(id, "name"); err == nil {
			t.Fatal("invalid/missing session renamed")
		}
		if err := s.SetPinned(id, true); err == nil {
			t.Fatal("invalid/missing session pinned")
		}
		if id != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
			if _, err := s.LoadMetadata(id); err == nil {
				t.Fatal("invalid metadata ID accepted")
			}
		}
	}
	metadataDir := filepath.Dir(namePath)
	backup := filepath.Join(s.dir, "metadata-backup")
	if err := os.Rename(metadataDir, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(metadataDir, []byte("obstruction"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.Rename(snap.ID, "unsaved"); err == nil {
		t.Fatal("name write failure ignored")
	}
	if err := s.SetPinned(snap.ID, true); err == nil {
		t.Fatal("pin write failure ignored")
	}
	if err := os.Remove(metadataDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(backup, metadataDir); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(namePath)
	if !bytes.Equal(before, after) {
		t.Fatal("failed rename changed previous name")
	}
	files, _ := os.ReadDir(metadataDir)
	if len(files) != 1 {
		t.Fatal("unexpected metadata temporary files", files)
	}
}

func TestMetadataProblemsKeepValidFieldsAndConversationResumable(t *testing.T) {
	s, _ := Open(t.TempDir(), t.TempDir())
	snap, _ := s.New()
	if err := s.Save(snap); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPinned(snap.ID, true); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"broken", "{}", "null", `{"name":null}`, `{"name":42}`} {
		if err := os.WriteFile(filepath.Join(s.dir, "metadata", snap.ID+".name.json"), []byte(bad), 0600); err != nil {
			t.Fatal(err)
		}
		entries, err := s.List()
		if err != nil || len(entries) != 1 || !entries[0].Pinned || entries[0].MetadataProblem == "" || entries[0].Problem != "" {
			t.Fatalf("metadata corruption blocked listing: %+v, %v", entries, err)
		}
		if loaded, err := s.LoadForResume(snap.ID); err != nil || loaded.ID != snap.ID {
			t.Fatalf("metadata blocked resume: %+v, %v", loaded, err)
		}
	}
	if err := s.Rename(snap.ID, "repaired"); err != nil {
		t.Fatal(err)
	}
	if metadata, err := s.LoadMetadata(snap.ID); err != nil || metadata != (Metadata{Name: "repaired", Pinned: true}) {
		t.Fatalf("metadata repair: %+v %v", metadata, err)
	}
}

// Run two real processes against the same workspace while reading checkpoints.
// Readers must always see a complete snapshot from one writer, never a merge.
func TestConcurrentProcessesResumeAndSaveCompleteSnapshots(t *testing.T) {
	dir, workspace := t.TempDir(), t.TempDir()
	s, _ := Open(dir, workspace)
	snap, _ := s.New()
	snap.Recency = time.Now().UTC().Add(-time.Hour)
	snap.Preview = "original"
	if err := s.Save(snap); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	for _, marker := range []string{"process-a", "process-b"} {
		cmd := exec.Command(executable, "-test.run=^TestStoreProcessWriter$")
		cmd.Env = append(os.Environ(), "QCODE_TEST_SESSION_DIR="+dir, "QCODE_TEST_WORKSPACE="+workspace, "QCODE_TEST_SESSION_ID="+snap.ID, "QCODE_TEST_MARKER="+marker)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		go func() { results <- cmd.Wait() }()
	}
	completed, reads := 0, 0
	deadline := time.After(30 * time.Second)
	for completed < 2 {
		select {
		case err := <-results:
			if err != nil {
				t.Fatal("writer process", err)
			}
			completed++
		case <-deadline:
			t.Fatal("writer processes timed out")
		default:
			loaded, err := s.Load(snap.ID)
			if err != nil {
				t.Fatal("partial checkpoint", err)
			}
			if loaded.Preview != "original" {
				var presentation, state struct{ Marker string }
				if err := json.Unmarshal(loaded.Presentation, &presentation); err != nil {
					t.Fatal(err)
				}
				if len(loaded.Agents) != 1 {
					t.Fatal("merged checkpoint", loaded)
				}
				if err := json.Unmarshal(loaded.Agents[0].State, &state); err != nil {
					t.Fatal(err)
				}
				if presentation.Marker != loaded.Preview || state.Marker != loaded.Preview || !loaded.Recency.Equal(snap.Recency) {
					t.Fatal("mixed checkpoint", loaded)
				}
			}
			reads++
			time.Sleep(time.Millisecond)
		}
	}
	if reads == 0 {
		t.Fatal("did not observe concurrent saves")
	}
	metadata, err := s.LoadMetadata(snap.ID)
	if err != nil || metadata != (Metadata{Name: "process-a-49", Pinned: true}) {
		t.Fatalf("field updates lost: %+v, %v", metadata, err)
	}
	// The original stale checkpoint wins in full when saved last, without
	// undoing either metadata field written by the other processes.
	if err := s.Save(snap); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.Load(snap.ID)
	wantJSON, _ := json.Marshal(snap)
	loadedJSON, _ := json.Marshal(loaded)
	if err != nil || !bytes.Equal(loadedJSON, wantJSON) {
		t.Fatalf("last snapshot did not win in full: %+v, %v", loaded, err)
	}
	if after, err := s.LoadMetadata(snap.ID); err != nil || after != metadata {
		t.Fatal("autosave overwrote metadata", after, err)
	}
}

func TestStoreProcessWriter(t *testing.T) {
	dir := os.Getenv("QCODE_TEST_SESSION_DIR")
	if dir == "" {
		return
	}
	s, err := Open(dir, os.Getenv("QCODE_TEST_WORKSPACE"))
	if err != nil {
		t.Fatal(err)
	}
	snap, err := s.LoadForResume(os.Getenv("QCODE_TEST_SESSION_ID"))
	if err != nil {
		t.Fatal(err)
	}
	marker := os.Getenv("QCODE_TEST_MARKER")
	for i := 0; i < 50; i++ {
		text := marker + "-" + strconv.Itoa(i)
		data, _ := json.Marshal(struct{ Marker string }{text})
		snap.Preview, snap.Presentation = text, data
		snap.Agents = []SavedAgent{{Summary: Summary{ID: marker}, State: data}}
		if err := s.Save(snap); err != nil {
			t.Fatal(err)
		}
		if marker == "process-a" {
			if err := s.Rename(snap.ID, text); err != nil {
				t.Fatal(err)
			}
		} else {
			if err := s.SetPinned(snap.ID, i%2 == 1); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestConcurrentStoreUpdatesKeepFieldsAndSnapshotsIndependent(t *testing.T) {
	s, _ := Open(t.TempDir(), t.TempDir())
	snap, _ := s.New()
	s.SetSnapshotFilter(func(s Snapshot) (Snapshot, error) { return s, nil })
	if err := s.Save(snap); err != nil {
		t.Fatal(err)
	}
	errors := make(chan error, 4)
	var workers sync.WaitGroup
	for _, task := range []func(int) error{
		func(i int) error { return s.Rename(snap.ID, fmt.Sprintf("name-%d", i)) },
		func(i int) error { return s.SetPinned(snap.ID, i%2 == 1) },
		func(i int) error {
			checkpoint := snap
			checkpoint.Preview = fmt.Sprintf("snapshot-%d", i)
			return s.Save(checkpoint)
		},
		func(int) error { _, err := s.LoadForResume(snap.ID); return err },
	} {
		workers.Add(1)
		go func(task func(int) error) {
			defer workers.Done()
			for i := 0; i < 20; i++ {
				if err := task(i); err != nil {
					errors <- err
					return
				}
			}
		}(task)
	}
	workers.Wait()
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
	loaded, err := s.Load(snap.ID)
	if err != nil || loaded.Preview != "snapshot-19" {
		t.Fatal("in-process resume rewrote a stale snapshot", loaded, err)
	}
	metadata, err := s.LoadMetadata(snap.ID)
	if err != nil || metadata != (Metadata{Name: "name-19", Pinned: true}) {
		t.Fatal("concurrent fields lost", metadata, err)
	}
}
