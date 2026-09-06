package learning

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

const testSource = "0123456789abcdef0123456789abcdef"

func testDraft() Draft {
	return Draft{Kind: "add", Topic: "Go testing", Content: "For Go packages, run focused tests before the full test suite.", Tags: []string{"go"}}
}
func addRecord(t *testing.T, s *FileStore) Learning {
	t.Helper()
	snapshot, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	plan, err := NewPlan(snapshot, []Draft{testDraft()}, testSource, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	return *plan.Changes[0].After
}
func TestStateDirectories(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix-shaped path fixtures")
	}
	for _, tc := range []struct{ os, xdg, local, want string }{
		{"linux", "", "", "/home/user/.local/state/qcode/learning"},
		{"linux", "relative", "", "/home/user/.local/state/qcode/learning"},
		{"linux", "/state", "", "/state/qcode/learning"},
		{"darwin", "/ignored", "", "/home/user/Library/Application Support/qcode/learning"},
		{"windows", "", "/local", "/local/qcode/learning"},
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
	root := filepath.Join(t.TempDir(), "learning")
	s := New(root, nil)
	snapshot, err := s.Snapshot(context.Background())
	if err != nil || len(snapshot.Items) != 0 {
		t.Fatalf("%v %v", snapshot, err)
	}
	if _, err := s.Search(context.Background(), "Go testing", 1200); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("empty retrieval created state")
	}
	item := addRecord(t, s)
	// Another instance has the same data regardless of its caller's workspace.
	other := New(root, nil)
	snapshot, err = other.Snapshot(context.Background())
	if err != nil || len(snapshot.Items) != 1 || !reflect.DeepEqual(snapshot.Items[0], item) {
		t.Fatalf("%v %v", snapshot, err)
	}
	path := filepath.Join(root, "v1", "global", item.ID+".json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("record permissions %v", info.Mode())
	}
	before, _ := os.ReadFile(path)
	if _, err := other.Search(context.Background(), "Go testing", 1200); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("retrieval mutated learning")
	}
	if _, err := os.Stat(filepath.Join(root, "v1", "workspaces")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("created workspace state")
	}
}
func TestCorruptRecordsPreserved(t *testing.T) {
	var warnings []string
	root := t.TempDir()
	s := New(root, func(s string) { warnings = append(warnings, s) })
	valid := addRecord(t, s)
	dir := filepath.Join(root, "v1", "global")
	corrupt := map[string][]byte{
		"bad.json":       []byte(`{"not":"a record"}`),
		"oversized.json": []byte(strings.Repeat("x", MaxRecordBytes+1)),
	}
	unsupported := valid
	unsupported.ID = strings.Repeat("a", 32)
	unsupported.Version = 99
	data, _ := json.Marshal(unsupported)
	corrupt[unsupported.ID+".json"] = data
	for name, data := range corrupt {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := s.Snapshot(context.Background())
	if err != nil || len(snapshot.Items) != 1 || len(warnings) != 3 {
		t.Fatalf("items %v warnings %v err %v", snapshot.Items, warnings, err)
	}
	for name, want := range corrupt {
		got, _ := os.ReadFile(filepath.Join(dir, name))
		if string(got) != string(want) {
			t.Fatal("rewrote corrupt record")
		}
	}
	// A bounded malformed record is carried through atomic replacements unchanged.
	os.Remove(filepath.Join(dir, "oversized.json"))
	addRecord(t, s)
	got, _ := os.ReadFile(filepath.Join(dir, "bad.json"))
	if string(got) != string(corrupt["bad.json"]) {
		t.Fatal("discarded corrupt record during write")
	}
}
func TestStaleReviewAndDeletion(t *testing.T) {
	s := New(t.TempDir(), nil)
	item := addRecord(t, s)
	old, _ := s.Snapshot(context.Background())
	deletion := Plan{Revision: old.Revision, Changes: []Change{{Before: &item}}}
	update := testDraft()
	update.Kind = "update"
	update.ID = item.ID
	update.Content = "For Go tests, use go test ./... after focused checks."
	plan, err := NewPlan(old, []Draft{update}, testSource, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(context.Background(), deletion); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale deletion: %v", err)
	}
	fresh, _ := s.Snapshot(context.Background())
	deletion = Plan{Revision: fresh.Revision, Changes: []Change{{Before: &fresh.Items[0]}}}
	if _, err := s.Apply(context.Background(), deletion); err != nil {
		t.Fatal(err)
	}
	empty, _ := s.Snapshot(context.Background())
	if len(empty.Items) != 0 {
		t.Fatal("delete failed")
	}
}
func TestCompactionBackupAndRollback(t *testing.T) {
	root := t.TempDir()
	s := New(root, nil)
	item := addRecord(t, s)
	snapshot, _ := s.Snapshot(context.Background())
	draft := testDraft()
	draft.Kind = "update"
	draft.ID = item.ID
	draft.Content = "For Go testing, start with the affected package."
	plan, err := NewPlan(snapshot, []Draft{draft}, testSource, true)
	if err != nil {
		t.Fatal(err)
	}
	s.rename = func(from, to string) error {
		if strings.HasPrefix(filepath.Base(from), ".stage-") {
			return errors.New("injected commit failure")
		}
		return os.Rename(from, to)
	}
	backup, err := s.Apply(context.Background(), plan)
	if err == nil || backup == "" {
		t.Fatalf("backup %q, err %v", backup, err)
	}
	restored, err := s.Snapshot(context.Background())
	if err != nil || !reflect.DeepEqual(restored, snapshot) {
		t.Fatal("failed transaction was not rolled back")
	}
	data, err := os.ReadFile(filepath.Join(backup, item.ID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved Learning
	if err := Decode(data, &saved); err != nil || saved.Content != item.Content {
		t.Fatal("backup did not preserve original")
	}
	s.rename = os.Rename
	backup, err = s.Apply(context.Background(), plan)
	if err != nil || backup == "" {
		t.Fatalf("%s %v", backup, err)
	}
	current, _ := s.Snapshot(context.Background())
	if current.Items[0].Content != draft.Content {
		t.Fatal("compaction not applied")
	}
}
func TestInterruptedSwapRecovery(t *testing.T) {
	root := t.TempDir()
	s := New(root, nil)
	original := addRecord(t, s)
	if err := os.Rename(filepath.Join(root, "v1", "global"), filepath.Join(root, "v1", ".previous")); err != nil {
		t.Fatal(err)
	}
	snapshot, err := New(root, nil).Snapshot(context.Background())
	if err != nil || len(snapshot.Items) != 1 || snapshot.Items[0].ID != original.ID {
		t.Fatalf("recovery: %v %v", snapshot, err)
	}
}
func TestProposalValidation(t *testing.T) {
	snapshot := Snapshot{}
	for _, draft := range []Draft{
		{Kind: "add", ID: "../escape", Topic: "x", Content: "x"},
		{Kind: "update", ID: strings.Repeat("a", 32), Topic: "x", Content: "x"},
		{Kind: "delete", ID: strings.Repeat("a", 32)},
		{Kind: "add", Topic: "credential", Content: "api_key = abcdef1234567890"},
		{Kind: "add", Topic: "bad\x1b", Content: "x"},
		{Kind: "add", Topic: "long", Content: strings.Repeat("x", 4097)},
	} {
		if _, err := NewPlan(snapshot, []Draft{draft}, testSource, false); err == nil {
			t.Fatalf("accepted %+v", draft)
		}
	}
	var l Learning
	if err := Decode([]byte(`{"scope":"workspace"}`), &l); err == nil {
		t.Fatal("accepted a scope field")
	}
}
func TestLockAcrossProcesses(t *testing.T) {
	if root := os.Getenv("QCODE_LEARNING_LOCK_TEST"); root != "" {
		s := New(root, nil)
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		_, err := s.Snapshot(ctx)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("child lock: %v", err)
		}
		return
	}
	root := t.TempDir()
	s := New(root, nil)
	addRecord(t, s)
	err := s.locked(context.Background(), true, func() error {
		cmd := exec.Command(os.Args[0], "-test.run=^TestLockAcrossProcesses$")
		cmd.Env = append(os.Environ(), "QCODE_LEARNING_LOCK_TEST="+root)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("child: %s: %v", output, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
func TestRankingAndBudget(t *testing.T) {
	items := []Learning{
		{ID: "a", Topic: "Go testing", Content: "For Go packages, run focused tests.", Tags: []string{"go"}},
		{ID: "b", Topic: "Go testing", Content: "Run Go tests before committing.", Tags: []string{"go"}},
		{ID: "c", Topic: "Go testing", Content: "Use Go race checks for concurrency.", Tags: []string{"go"}},
		{ID: "d", Topic: "Go testing", Content: "Keep Go tests deterministic.", Tags: []string{"go"}},
		{ID: "e", Topic: "Python testing", Content: "Use pytest for Python tests.", Tags: []string{"python"}},
	}
	got := Rank(items, "Go testing", 1200)
	if len(got) != 3 || got[0].ID != "a" {
		t.Fatalf("ranking %v", got)
	}
	for _, query := range []string{"hello", "testing", "Python unrelated"} {
		if got := Rank(items, query, 1200); len(got) != 0 {
			t.Fatalf("weak match %q: %v", query, got)
		}
	}
	if got := Rank(items, "Python testing", 1200); len(got) != 1 || got[0].ID != "e" {
		t.Fatalf("applicability %v", got)
	}
	for budget := 0; budget < 200; budget++ {
		got := Rank(items, "Go testing", budget)
		if EstimatedTokens(Context(got)) > budget {
			t.Fatalf("exceeded budget %d", budget)
		}
	}
}

func TestConcurrentApprovedPlansCannotOverwrite(t *testing.T) {
	root := t.TempDir()
	s := New(root, nil)
	addRecord(t, s)
	snapshot, _ := s.Snapshot(context.Background())
	first, err := NewPlan(snapshot, []Draft{testDraft()}, testSource, false)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewPlan(snapshot, []Draft{testDraft()}, testSource, false)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, plan := range []Plan{first, second} {
		go func(plan Plan) { <-start; _, err := New(root, nil).Apply(context.Background(), plan); results <- err }(plan)
	}
	close(start)
	one, two := <-results, <-results
	if !((one == nil && errors.Is(two, ErrConflict)) || (two == nil && errors.Is(one, ErrConflict))) {
		t.Fatalf("results %v %v", one, two)
	}
	final, err := s.Snapshot(context.Background())
	if err != nil || len(final.Items) != 2 {
		t.Fatalf("final %v %v", final, err)
	}
}
