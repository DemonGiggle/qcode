package session

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDeleteAllRemovesCorruptSnapshotsAndOrphanMetadataWithinWorkspace(t *testing.T) {
	directory := t.TempDir()
	s, _ := Open(directory, t.TempDir())
	other, _ := Open(directory, t.TempDir())
	otherSnap, _ := other.New()
	if err := other.Save(otherSnap); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(s.dir, "metadata"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"11111111111111111111111111111111", "22222222222222222222222222222222"} {
		for _, path := range []string{filepath.Join(s.dir, id+".json"), filepath.Join(s.dir, "metadata", id+".name.json"), filepath.Join(s.dir, "metadata", id+".pin.json")} {
			if err := os.WriteFile(path, []byte("corrupt"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	orphan := filepath.Join(s.dir, "metadata", "33333333333333333333333333333333.name.json")
	if err := os.WriteFile(orphan, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(s.dir, "unrelated.json")
	if err := os.WriteFile(unrelated, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	s.SetSnapshotFilter(func(Snapshot) (Snapshot, error) { t.Fatal("parsed snapshot"); return Snapshot{}, nil })
	s.SetDefaultNameFilter(func(string) string { t.Fatal("parsed metadata"); return "" })
	if err := s.DeleteAll(); err != nil {
		t.Fatal(err)
	}
	if entries, err := s.List(); err != nil || len(entries) != 0 {
		t.Fatal("snapshots remain", entries, err)
	}
	if files, err := os.ReadDir(filepath.Join(s.dir, "metadata")); err != nil || len(files) != 0 {
		t.Fatal("metadata remains", files, err)
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Fatal("removed unrelated file", err)
	}
	if _, err := other.Load(otherSnap.ID); err != nil {
		t.Fatal("removed another workspace", err)
	}
	if err := s.DeleteAll(); err != nil {
		t.Fatal("empty deletion failed", err)
	}
}

func TestDeleteAllExclusionValidationAndLaterRecreation(t *testing.T) {
	dir, workspace := t.TempDir(), t.TempDir()
	s, _ := Open(dir, workspace)
	other, _ := Open(dir, workspace)
	keep, _ := s.New()
	remove, _ := s.New()
	for _, snap := range []Snapshot{keep, remove} {
		if err := s.Save(snap); err != nil {
			t.Fatal(err)
		}
		if err := s.Rename(snap.ID, "name"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DeleteAll("../invalid"); err == nil {
		t.Fatal("accepted invalid exclusion")
	}
	if _, err := s.Load(remove.ID); err != nil {
		t.Fatal("invalid exclusion removed files", err)
	}
	if err := s.DeleteAll(keep.ID); err != nil {
		t.Fatal(err)
	}
	if entries, err := s.List(); err != nil || len(entries) != 1 || entries[0].ID != keep.ID || entries[0].Name != "name" {
		t.Fatal("excluded snapshot or metadata removed", entries, err)
	}
	if err := other.Save(remove); err != nil {
		t.Fatal(err)
	}
	if metadata, err := s.LoadMetadata(remove.ID); err != nil || metadata != (Metadata{}) {
		t.Fatal("recreation restored removed metadata", metadata, err)
	}
}

func TestDeleteAllStopsOnFilesystemError(t *testing.T) {
	s, _ := Open(t.TempDir(), t.TempDir())
	first, _ := s.New()
	first.ID = "11111111111111111111111111111111"
	blocked := first
	blocked.ID = "22222222222222222222222222222222"
	last := first
	last.ID = "33333333333333333333333333333333"
	for _, snap := range []Snapshot{first, blocked, last} {
		if err := s.Save(snap); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(s.dir, "metadata", blocked.ID+".pin.json")
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "block-removal"), []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteAll(); err == nil {
		t.Fatal("ignored filesystem failure")
	}
	if _, err := s.Load(first.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("earlier snapshot not deleted", err)
	}
	for _, id := range []string{blocked.ID, last.ID} {
		if _, err := s.Load(id); err != nil {
			t.Fatal("continued after failure", err)
		}
	}
}
