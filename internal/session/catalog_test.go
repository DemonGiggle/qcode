package session

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCatalogManagesSessionsInTheirOwnWorkspace(t *testing.T) {
	dir := t.TempDir()
	first, err := Open(dir, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	second, err := Open(dir, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a, _ := first.New()
	b, _ := second.New()
	b.ID = a.ID // Identity is scoped to the workspace directory.
	a.Recency = time.Now()
	b.Recency = a.Recency.Add(-time.Hour)
	if err := first.Save(a); err != nil {
		t.Fatal(err)
	}
	if err := second.Save(b); err != nil {
		t.Fatal(err)
	}
	// Catalog management must still find this workspace after it is removed.
	if err := os.Remove(second.workspace); err != nil {
		t.Fatal(err)
	}
	c := NewCatalog(dir, nil, nil)
	entries, err := c.List()
	if err != nil || len(entries) != 2 || entries[0].Workspace != a.Workspace || entries[1].Workspace != b.Workspace {
		t.Fatalf("initial catalog = %+v, %v", entries, err)
	}
	if err := c.Rename(entries[1], "Archived workspace"); err != nil {
		t.Fatal(err)
	}
	if err := c.SetPinned(entries[1], true); err != nil {
		t.Fatal(err)
	}
	entries, err = c.List()
	if err != nil || len(entries) != 2 || entries[0].Workspace != b.Workspace || !entries[0].Pinned ||
		entries[0].Name != "Archived workspace" || entries[1].Pinned || entries[1].Name != "" {
		t.Fatalf("edited catalog = %+v, %v", entries, err)
	}
	if err := c.Delete(entries[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Load(a.ID); err != nil {
		t.Fatal("deleted the other workspace's matching ID", err)
	}
	if _, err := second.Load(b.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("selected session remains: %v", err)
	}
	if metadata, err := second.LoadMetadata(b.ID); err != nil || metadata != (Metadata{}) {
		t.Fatalf("selected metadata remains: %+v, %v", metadata, err)
	}

	if err := os.WriteFile(filepath.Join(second.dir, b.ID+".json"), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	entries, err = c.List()
	if err != nil || len(entries) != 2 || entries[0].Problem == "" && entries[1].Problem == "" {
		t.Fatalf("catalog with corrupt snapshot = %+v, %v", entries, err)
	}
	unrelated := filepath.Join(dir, "unrelated")
	if err := os.Mkdir(unrelated, 0700); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteAll(); err != nil {
		t.Fatal(err)
	}
	if entries, err = c.List(); err != nil || len(entries) != 0 {
		t.Fatalf("after delete all = %+v, %v", entries, err)
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Fatal("removed unrelated directory", err)
	}
}
