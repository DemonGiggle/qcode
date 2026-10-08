package session

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"qcode/internal/redaction"
)

func TestSanitizedListingAndLegacyRewrite(t *testing.T) {
	store, _ := Open(t.TempDir(), t.TempDir())
	snap, _ := store.New()
	snap.Preview = "password=legacy-secret"
	if err := store.Save(snap); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(store.dir, snap.ID+".json")
	original, _ := os.ReadFile(path)
	store.SetSnapshotFilter(func(s Snapshot) (Snapshot, error) {
		s.Preview = (*redaction.Policy)(nil).Text(redaction.Persistence, s.Preview)
		return s, nil
	})
	entries, err := store.List()
	if err != nil || len(entries) != 1 || entries[0].Preview != "password= "+redaction.Marker {
		t.Fatalf("listing: %v %v", entries, err)
	}
	untouched, _ := os.ReadFile(path)
	if !bytes.Equal(untouched, original) {
		t.Fatal("listing rewrote legacy file")
	}
	filtered, err := store.LoadForResume(snap.ID)
	if err != nil || filtered.Preview != "password= "+redaction.Marker {
		t.Fatal(filtered, err)
	}
	data, _ := os.ReadFile(path)
	if bytes.Contains(data, []byte("legacy-secret")) {
		t.Fatal("legacy file still contains secret")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("rewrite lost private permissions")
	}
	files, _ := os.ReadDir(store.dir)
	for _, file := range files {
		if file.Name() != snap.ID+".json" {
			t.Fatal("unexpected backup")
		}
	}
}
func TestResumeRewriteFailureAndSaveFailureDoNotExposeSnapshot(t *testing.T) {
	store, _ := Open(t.TempDir(), t.TempDir())
	snap, _ := store.New()

	snap.Preview = "password=legacy-secret"
	store.Save(snap)
	path := filepath.Join(store.dir, snap.ID+".json")
	before, _ := os.ReadFile(path)
	failure := errors.New("filter rejected checkpoint")
	store.SetSnapshotFilter(func(Snapshot) (Snapshot, error) { return Snapshot{}, failure })
	if result, err := store.LoadForResume(snap.ID); !errors.Is(err, failure) || result.ID != "" {
		t.Fatal("returned unsanitized state after failure")
	}
	if err := store.Save(snap); !errors.Is(err, failure) {
		t.Fatal("save bypassed failing filter")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("failed filtering changed legacy file")
	}
	// Simulate an atomic replacement failure without relying on chmod under root.
	first := true
	store.SetSnapshotFilter(func(s Snapshot) (Snapshot, error) {
		if !first {
			return s, nil
		}
		first = false
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		return s, nil
	})
	if result, err := store.LoadForResume(snap.ID); err == nil || result.ID != "" {
		t.Fatal("rewrite failure returned resumable state")
	}
}
