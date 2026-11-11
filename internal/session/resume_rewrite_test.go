package session

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const resumeTestSecret = "SECRET-abc-123"
const resumeTestRedacted = "[REDACTED]"

// (a) With a filter installed, LoadForResume returns the sanitized snapshot,
// runs the filter exactly once (single-pass rewrite), and the bytes on disk
// are sanitized.
func TestResumeRewriteWritesSanitizedSinglePass(t *testing.T) {
	store, err := Open(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	snap, err := store.New()
	if err != nil {
		t.Fatal(err)
	}
	snap.Preview = "token " + resumeTestSecret
	if err := store.Save(snap); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.dir, snap.ID+".json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(resumeTestSecret)) {
		t.Fatal("setup: raw file should still contain the secret before filtering")
	}

	calls := 0
	store.SetSnapshotFilter(func(s Snapshot) (Snapshot, error) {
		calls++
		s.Preview = strings.ReplaceAll(s.Preview, resumeTestSecret, resumeTestRedacted)
		return s, nil
	})

	got, err := store.LoadForResume(snap.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got.Preview, resumeTestSecret) {
		t.Fatalf("returned snapshot not sanitized: %q", got.Preview)
	}
	if !strings.Contains(got.Preview, resumeTestRedacted) {
		t.Fatalf("returned snapshot missing redaction: %q", got.Preview)
	}
	if calls != 1 {
		t.Fatalf("filter ran %d times, want exactly 1 (double-filter regression)", calls)
	}

	// Reload the raw file bytes: the rejected pattern must be gone from disk.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(resumeTestSecret)) {
		t.Fatal("disk bytes still contain rejected pattern after resume rewrite")
	}
	if !bytes.Contains(data, []byte(resumeTestRedacted)) {
		t.Fatal("disk bytes missing sanitized content after resume rewrite")
	}

	// The public Save API must keep filtering snapshots from unsanitized callers.
	calls = 0
	second, err := store.New()
	if err != nil {
		t.Fatal(err)
	}
	second.Preview = "again " + resumeTestSecret
	if err := store.Save(second); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("Save ran filter %d times, want 1", calls)
	}
	saved, err := os.ReadFile(filepath.Join(store.dir, second.ID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(saved, []byte(resumeTestSecret)) {
		t.Fatal("Save wrote unsanitized bytes to disk")
	}
}

// (b) With no filter, LoadForResume performs no rewrite (matches current
// behavior: rewrite only happens when s.filter != nil).
func TestResumeRewriteNoFilterSkipsRewrite(t *testing.T) {
	store, err := Open(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	snap, err := store.New()
	if err != nil {
		t.Fatal(err)
	}
	snap.Preview = "plain preview"
	if err := store.Save(snap); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.dir, snap.ID+".json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.LoadForResume(snap.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Preview != "plain preview" {
		t.Fatalf("unexpected preview: %q", got.Preview)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("no-filter resume rewrote the file; want no rewrite")
	}
}

// (c) Identity validation still rejects a tampered snapshot without writing.
func TestResumeRewriteRejectsTamperedIdentity(t *testing.T) {
	t.Run("workspace", func(t *testing.T) {
		store, err := Open(t.TempDir(), t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		snap, _ := store.New()
		snap.Preview = "hello"
		if err := store.Save(snap); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(store.dir, snap.ID+".json")
		before, _ := os.ReadFile(path)
		store.SetSnapshotFilter(func(s Snapshot) (Snapshot, error) {
			s.Workspace = "/tampered/workspace"
			return s, nil
		})
		got, err := store.LoadForResume(snap.ID)
		if err == nil || !strings.Contains(err.Error(), "rewrite sanitized session") {
			t.Fatalf("want wrapped rewrite error, got %v", err)
		}
		if got.ID != "" {
			t.Fatalf("must not return tampered snapshot: %+v", got)
		}
		after, _ := os.ReadFile(path)
		if !bytes.Equal(before, after) {
			t.Fatal("tampered snapshot was written to disk")
		}
	})
	t.Run("version", func(t *testing.T) {
		store, err := Open(t.TempDir(), t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		snap, _ := store.New()
		snap.Preview = "hello"
		if err := store.Save(snap); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(store.dir, snap.ID+".json")
		before, _ := os.ReadFile(path)
		store.SetSnapshotFilter(func(s Snapshot) (Snapshot, error) {
			s.Version = SnapshotVersion + 99
			return s, nil
		})
		got, err := store.LoadForResume(snap.ID)
		if err == nil || !strings.Contains(err.Error(), "rewrite sanitized session") {
			t.Fatalf("want wrapped rewrite error, got %v", err)
		}
		if got.ID != "" {
			t.Fatalf("must not return tampered snapshot: %+v", got)
		}
		after, _ := os.ReadFile(path)
		if !bytes.Equal(before, after) {
			t.Fatal("tampered snapshot was written to disk")
		}
	})
}
