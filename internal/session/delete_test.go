package session

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDeleteRemovesSnapshotAndMetadataWithoutParsing(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(map[bool]string{false: "valid", true: "corrupt"}[corrupt], func(t *testing.T) {
			s, err := Open(t.TempDir(), t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			snap, _ := s.New()
			if err := s.Save(snap); err != nil {
				t.Fatal(err)
			}
			if err := s.Rename(snap.ID, "custom"); err != nil {
				t.Fatal(err)
			}
			if err := s.SetPinned(snap.ID, true); err != nil {
				t.Fatal(err)
			}
			paths := []string{filepath.Join(s.dir, snap.ID+".json"),
				filepath.Join(s.dir, "metadata", snap.ID+".name.json"),
				filepath.Join(s.dir, "metadata", snap.ID+".pin.json")}
			if corrupt {
				for _, path := range paths {
					if err := os.WriteFile(path, []byte("broken"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			// Deletion must not invoke snapshot or name sanitizers either.
			s.SetSnapshotFilter(func(Snapshot) (Snapshot, error) { t.Fatal("parsed snapshot"); return Snapshot{}, nil })
			s.SetDefaultNameFilter(func(string) string { t.Fatal("parsed name"); return "" })
			if err := s.Delete(snap.ID); err != nil {
				t.Fatal(err)
			}
			for _, path := range paths {
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("file remains: %s: %v", path, err)
				}
			}
			if err := s.Delete(snap.ID); err != nil {
				t.Fatal("missing files must succeed", err)
			}
		})
	}
}

func TestDeleteValidatesIDAndScopesWorkspace(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir, t.TempDir())
	other, _ := Open(dir, t.TempDir())
	snap, _ := other.New()
	if err := other.Save(snap); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"", "../escape", snap.ID + "/../" + snap.ID, "0000", "gggggggggggggggggggggggggggggggg"} {
		if err := s.Delete(id); err == nil {
			t.Fatalf("accepted invalid ID %q", id)
		}
	}
	if err := s.Delete(snap.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := other.Load(snap.ID); err != nil {
		t.Fatal("deleted another workspace's snapshot", err)
	}
}

func TestDeleteStopsAtFirstFilesystemError(t *testing.T) {
	for _, failing := range []int{0, 1, 2} {
		t.Run([]string{"name", "pin", "snapshot"}[failing], func(t *testing.T) {
			s, _ := Open(t.TempDir(), t.TempDir())
			snap, _ := s.New()
			paths := []string{filepath.Join(s.dir, "metadata", snap.ID+".name.json"),
				filepath.Join(s.dir, "metadata", snap.ID+".pin.json"), filepath.Join(s.dir, snap.ID+".json")}
			if err := os.MkdirAll(filepath.Join(s.dir, "metadata"), 0700); err != nil {
				t.Fatal(err)
			}
			for i, path := range paths {
				if i == failing {
					if err := os.Mkdir(path, 0700); err != nil {
						t.Fatal(err)
					}
					path = filepath.Join(path, "block-removal")
				}
				if err := os.WriteFile(path, []byte("data"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.Delete(snap.ID); err == nil {
				t.Fatal("ignored removal failure")
			}
			for i, path := range paths {
				_, err := os.Stat(path)
				if i < failing && !errors.Is(err, os.ErrNotExist) || i >= failing && err != nil {
					t.Fatalf("incorrect deletion order at %s: %v", path, err)
				}
			}
		})
	}
}

func TestDeleteSerializedWithSaveAndAllowsLaterRecreation(t *testing.T) {
	dir, workspace := t.TempDir(), t.TempDir()
	s, _ := Open(dir, workspace)
	other, _ := Open(dir, workspace)
	snap, _ := s.New()
	entered, release := make(chan struct{}), make(chan struct{})
	s.SetSnapshotFilter(func(snap Snapshot) (Snapshot, error) {
		close(entered)
		<-release
		return snap, nil
	})
	saved, deleted := make(chan error, 1), make(chan error, 1)
	go func() { saved <- s.Save(snap) }()
	<-entered
	go func() { deleted <- s.Delete(snap.ID) }()
	close(release)
	if err := <-saved; err != nil {
		t.Fatal(err)
	}
	if err := <-deleted; err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(snap.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("delete did not follow in-process save", err)
	}
	if err := other.Save(snap); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(snap.ID); err != nil {
		t.Fatal("later save did not recreate snapshot", err)
	}
	metadata, err := s.LoadMetadata(snap.ID)
	if err != nil || metadata != (Metadata{}) {
		t.Fatal("recreation restored deleted metadata", metadata, err)
	}
}
