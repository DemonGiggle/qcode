package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreWorkspaceRoundTrip(t *testing.T) {
	dir, workspace := t.TempDir(), t.TempDir()
	s, err := Open(dir, workspace)
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.New()
	if err != nil {
		t.Fatal(err)
	}

	a.Preview = "latest response"
	a.Saved = time.Now()
	a.Left = a.Saved
	if err := s.Save(a); err != nil {
		t.Fatal(err)
	}
	b, err := s.New()
	if err != nil {
		t.Fatal(err)
	}

	b.Saved = a.Saved.Add(time.Minute)
	if err := s.Save(b); err != nil {
		t.Fatal(err)
	}
	entries, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].ID != b.ID || entries[1].Preview != a.Preview {
		t.Fatalf("entries: %+v", entries)
	}
	separate, err := Open(dir, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	empty, err := separate.List()
	if err != nil || len(empty) != 0 {
		t.Fatalf("workspace leak: %v %v", empty, err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(workspace, alias); err == nil {
		same, err := Open(dir, alias)
		if err != nil || same.dir != s.dir {
			t.Fatalf("alias: %v", err)
		}
	}
}

func TestStoreRejectsInvalidAndPreservesPreviousSnapshot(t *testing.T) {
	s, _ := Open(t.TempDir(), t.TempDir())
	snap, _ := s.New()

	snap.Preview = "original"
	if err := s.Save(snap); err != nil {
		t.Fatal(err)
	}
	bad := snap
	bad.Version++
	if err := s.Save(bad); err == nil {
		t.Fatal("accepted future version")
	}
	loaded, err := s.Load(snap.ID)
	if err != nil || loaded.Preview != "original" {
		t.Fatal("lost previous snapshot")
	}
	if _, err := s.Load("../escape"); err == nil {
		t.Fatal("accepted invalid ID")
	}
	if err := os.WriteFile(filepath.Join(s.dir, snap.ID+".json"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(snap.ID); err == nil {
		t.Fatal("accepted corrupt snapshot")
	}
	entries, err := s.List()
	if err != nil || len(entries) != 1 || entries[0].Problem == "" {
		t.Fatalf("corrupt session not isolated: %v %v", entries, err)
	}
}

func TestStoreIgnoresOldLocks(t *testing.T) {
	s, err := Open(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	snap, err := s.New()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Save(snap); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(s.dir, snap.ID+".lock"), 0700); err != nil {
		t.Fatal(err)
	}
	entries, err := s.List()
	if err != nil || len(entries) != 1 || entries[0].Problem != "" {
		t.Fatalf("old lock affected list: %+v, %v", entries, err)
	}
	if _, err := s.LoadForResume(snap.ID); err != nil {
		t.Fatal(err)
	}
}
