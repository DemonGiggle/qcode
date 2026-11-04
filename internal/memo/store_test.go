package memo

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestStateDirectories(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix-shaped path fixtures")
	}
	for _, tc := range []struct{ os, xdg, local, want string }{
		{"linux", "", "", "/home/user/.local/state/qcode/memos"},
		{"linux", "relative", "", "/home/user/.local/state/qcode/memos"},
		{"linux", "/state", "", "/state/qcode/memos"},
		{"darwin", "/ignored", "", "/home/user/Library/Application Support/qcode/memos"},
		{"windows", "", "/local", "/local/qcode/memos"},
	} {
		got, err := StateDirectory(tc.os, "/home/user", tc.xdg, tc.local)
		if err != nil || got != tc.want {
			t.Fatalf("got %q, %v; want %q", got, err, tc.want)
		}
	}
	if _, err := StateDirectory("windows", "/home/user", "", ""); err == nil {
		t.Fatal("accepted missing Windows state directory")
	}
}

func TestStoreRoundTripAndReadOnlyEmpty(t *testing.T) {
	root := filepath.Join(t.TempDir(), "memos")
	s := New(root, nil)
	items, err := s.Snapshot(context.Background())
	if err != nil || len(items) != 0 {
		t.Fatalf("%v %v", items, err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("empty retrieval created state")
	}
	saved, total, err := s.Add(context.Background(), "check edge case where path has spaces")
	if err != nil || total != 1 || saved.Text != "check edge case where path has spaces" {
		t.Fatalf("add %v %d %v", saved, total, err)
	}
	// Another instance sees the same global data regardless of workspace.
	other := New(root, nil)
	items, err = other.Snapshot(context.Background())
	if err != nil || len(items) != 1 || items[0].ID != saved.ID {
		t.Fatalf("%v %v", items, err)
	}
	path := filepath.Join(root, "v1", "global", saved.ID+".json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("record permissions %v", info.Mode())
	}
}

func TestSnapshotNewestFirst(t *testing.T) {
	s := New(t.TempDir(), nil)
	ctx := context.Background()
	first, _, err := s.Add(ctx, "first")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	second, _, err := s.Add(ctx, "second")
	if err != nil {
		t.Fatal(err)
	}
	items, err := s.Snapshot(ctx)
	if err != nil || len(items) != 2 {
		t.Fatalf("%v %v", items, err)
	}
	if items[0].ID != second.ID || items[1].ID != first.ID {
		t.Fatalf("order = %v %v", items[0].ID, items[1].ID)
	}
}

func TestAddValidation(t *testing.T) {
	s := New(t.TempDir(), nil)
	ctx := context.Background()
	for _, text := range []string{"", "   ", "\n\t ", strings.Repeat("x", MaxMemoBytes+1), "bad\x1btext", "bad\x00text"} {
		if _, _, err := s.Add(ctx, text); err == nil {
			t.Fatalf("accepted %q", text)
		}
	}
	if _, _, err := s.Add(ctx, "ok\nmultiline\tallowed"); err != nil {
		t.Fatalf("multiline rejected: %v", err)
	}
}

func TestUpdateAndDelete(t *testing.T) {
	s := New(t.TempDir(), nil)
	ctx := context.Background()
	saved, _, err := s.Add(ctx, "original")
	if err != nil {
		t.Fatal(err)
	}
	updated, err := s.Update(ctx, saved.ID, "edited\nwith newline")
	if err != nil || updated.Text != "edited\nwith newline" || !updated.UpdatedAt.After(updated.CreatedAt.Add(-time.Second)) {
		t.Fatalf("update %v %v", updated, err)
	}
	if _, err := s.Update(ctx, saved.ID, "   "); err == nil {
		t.Fatal("accepted empty update")
	}
	if _, err := s.Update(ctx, strings.Repeat("0", 32), "nope"); err == nil {
		t.Fatal("accepted unknown ID update")
	}
	total, err := s.Delete(ctx, saved.ID)
	if err != nil || total != 0 {
		t.Fatalf("delete %d %v", total, err)
	}
	if _, err := s.Delete(ctx, saved.ID); err == nil {
		t.Fatal("accepted double delete")
	}
}

func TestCorruptRecordsPreserved(t *testing.T) {
	var warnings []string
	root := t.TempDir()
	s := New(root, func(msg string) { warnings = append(warnings, msg) })
	valid, _, err := s.Add(context.Background(), "valid memo")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "v1", "global")
	if err := os.WriteFile(filepath.Join(dir, "bad.json"), []byte(`{"not":"a memo"}`), 0600); err != nil {
		t.Fatal(err)
	}
	items, err := s.Snapshot(context.Background())
	if err != nil || len(items) != 1 || items[0].ID != valid.ID || len(warnings) != 1 {
		t.Fatalf("items %v warnings %v err %v", items, warnings, err)
	}
	// Valid records remain writable after corruption.
	if _, _, err := s.Add(context.Background(), "second"); err != nil {
		t.Fatalf("add after corruption: %v", err)
	}
}
