package session

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestListIsolatesSnapshotFilterFailures(t *testing.T) {
	for _, rejectDiagnostic := range []bool{false, true} {
		t.Run(map[bool]string{false: "filtered diagnostic", true: "fallback diagnostic"}[rejectDiagnostic], func(t *testing.T) {
			store, err := Open(t.TempDir(), t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			healthy, _ := store.New()
			broken, _ := store.New()
			healthy.Preview, broken.Preview = "healthy", "private-conversation"
			for _, snap := range []Snapshot{healthy, broken} {
				if err := store.Save(snap); err != nil {
					t.Fatal(err)
				}
			}
			store.SetSnapshotFilter(func(s Snapshot) (Snapshot, error) {
				if s.ID == broken.ID && (s.Preview == broken.Preview || rejectDiagnostic) {
					return Snapshot{}, errors.New("bad payload: private-conversation")
				}
				s.Preview = strings.ReplaceAll(s.Preview, "private-conversation", "[redacted]")
				return s, nil
			})
			entries, err := store.List()
			if err != nil || len(entries) != 2 {
				t.Fatalf("one bad payload hid saved sessions: %+v, %v", entries, err)
			}
			for _, entry := range entries {
				if entry.ID == healthy.ID && (entry.Preview != "healthy" || entry.Problem != "") {
					t.Fatalf("healthy session changed: %+v", entry)
				}
				if entry.ID == broken.ID && (entry.Problem == "" || entry.Preview != "" || strings.Contains(entry.Problem, "private-conversation")) {
					t.Fatalf("unavailable entry leaked rejected payload: %+v", entry)
				}
			}
			if _, err := store.LoadForResume(healthy.ID); err != nil {
				t.Fatal("healthy session could not resume", err)
			}
			if err := store.Delete(broken.ID); err != nil {
				t.Fatal("unavailable session could not be deleted", err)
			}
		})
	}
}

func TestResumeRewriteSerializesWithOtherStores(t *testing.T) {
	directory, workspace := t.TempDir(), t.TempDir()
	reader, err := Open(directory, workspace)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := Open(directory, workspace)
	if err != nil {
		t.Fatal(err)
	}
	original, _ := reader.New()
	original.Preview = "old conversation"
	if err := reader.Save(original); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	first := true
	reader.SetSnapshotFilter(func(s Snapshot) (Snapshot, error) {
		if first {
			first = false
			close(entered)
			<-release
		}
		return s, nil
	})
	resumed := make(chan error, 1)
	go func() { _, err := reader.LoadForResume(original.ID); resumed <- err }()
	<-entered
	newer := original
	newer.Preview = "newer conversation"
	saved := make(chan error, 1)
	go func() { saved <- writer.Save(newer) }()
	select {
	case err := <-saved:
		close(release)
		<-resumed
		t.Fatalf("writer bypassed an in-progress resume rewrite: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := <-resumed; err != nil {
		t.Fatal(err)
	}
	if err := <-saved; err != nil {
		t.Fatal(err)
	}
	loaded, err := writer.Load(original.ID)
	if err != nil || loaded.Preview != newer.Preview {
		t.Fatalf("resume reverted a newer save: %+v, %v", loaded, err)
	}
}

func TestSnapshotLockFailurePreservesSavedFile(t *testing.T) {
	store, _ := Open(t.TempDir(), t.TempDir())
	snap, _ := store.New()
	snap.Preview = "original"
	if err := store.Save(snap); err != nil {
		t.Fatal(err)
	}
	// A directory in place of the transaction lock fails even under root.
	lockPath := filepath.Join(store.dir, snapshotLockName)
	if err := os.Remove(lockPath); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.Mkdir(lockPath, 0700); err != nil {
		t.Fatal(err)
	}
	snap.Preview = "replacement"
	if err := store.Save(snap); err == nil {
		t.Fatal("save ignored transaction lock failure")
	}
	if _, err := store.LoadForResume(snap.ID); err == nil {
		t.Fatal("resume ignored transaction lock failure")
	}
	if err := store.Delete(snap.ID); err == nil {
		t.Fatal("deletion ignored transaction lock failure")
	}
	if err := store.DeleteAll(); err == nil {
		t.Fatal("bulk deletion ignored transaction lock failure")
	}
	loaded, err := store.Load(snap.ID)
	if err != nil || loaded.Preview != "original" {
		t.Fatalf("failed transaction changed snapshot: %+v, %v", loaded, err)
	}
}
