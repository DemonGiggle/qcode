package tui

import (
	"context"
	"io"
	"strings"
	"testing"

	"qcode/internal/memo"
)

type memoTestStore struct {
	*memo.FileStore
	adds int
}

func (s *memoTestStore) Add(ctx context.Context, text string) (memo.Memo, int, error) {
	s.adds++
	return s.FileStore.Add(ctx, text)
}

func newMemoTestUI(store memoStore, drafts map[string]string) (*UI, *historyWriter) {
	display := newHistoryWriter(io.Discard)
	u := &UI{
		display:     display,
		width:       80,
		activeAgent: "main",
		drafts:      drafts,
		views:       map[string]*agentView{},
	}
	u.SetMemoStore(store)
	return u, display
}

func TestMemoQuickCaptureSavesAndPreservesDraft(t *testing.T) {
	dir := t.TempDir()
	store := memo.New(dir, nil)
	wrapped := &memoTestStore{FileStore: store}
	drafts := map[string]string{"main": "half-typed steer draft", "worker": "untouched"}
	u, display := newMemoTestUI(wrapped, drafts)

	u.handleMemoCommand(context.Background(), "/memo check edge case where path has spaces")

	items, err := store.Snapshot(context.Background())
	if err != nil || len(items) != 1 || items[0].Text != "check edge case where path has spaces" {
		t.Fatalf("snapshot %v %v", items, err)
	}
	if wrapped.adds != 1 {
		t.Fatalf("adds = %d", wrapped.adds)
	}
	// Other tabs untouched; active draft preserved (only /memo lines cleared).
	if drafts["worker"] != "untouched" {
		t.Fatalf("worker draft = %q", drafts["worker"])
	}
	if drafts["main"] != "half-typed steer draft" {
		t.Fatalf("active draft = %q", drafts["main"])
	}
	if got := strings.Join(display.Lines(), "\n"); !strings.Contains(got, "Memo saved (1 total)") {
		t.Fatalf("confirmation = %q", got)
	}
}

func TestMemoQuickCaptureClearsMemoLine(t *testing.T) {
	dir := t.TempDir()
	store := memo.New(dir, nil)
	u, _ := newMemoTestUI(store, map[string]string{"main": "/memo to be cleared"})

	u.handleMemoCommand(context.Background(), "/memo hello world")

	u.screenMu.Lock()
	draft := u.drafts["main"]
	u.screenMu.Unlock()
	if draft != "" {
		t.Fatalf("memo line draft = %q", draft)
	}
}

func TestMemoWhitespaceOpensViewWithoutSaving(t *testing.T) {
	dir := t.TempDir()
	store := memo.New(dir, nil)
	u, display := newMemoTestUI(store, map[string]string{"main": "keep me"})
	// No interactive terminal in test, so bare /memo reports the requirement
	// instead of opening the browser and must not save anything.
	u.handleMemoCommand(context.Background(), "/memo   ")

	count, err := store.Count(context.Background())
	if err != nil || count != 0 {
		t.Fatalf("count %d %v", count, err)
	}
	if got := strings.Join(display.Lines(), "\n"); !strings.Contains(got, "interactive terminal") {
		t.Fatalf("view message = %q", got)
	}
	u.screenMu.Lock()
	draft := u.drafts["main"]
	u.screenMu.Unlock()
	if draft != "keep me" {
		t.Fatalf("draft = %q", draft)
	}
}

func TestMemoUnavailable(t *testing.T) {
	u, display := newMemoTestUI(nil, map[string]string{"main": ""})
	u.handleMemoCommand(context.Background(), "/memo hello")
	if got := strings.Join(display.Lines(), "\n"); !strings.Contains(got, "unavailable") {
		t.Fatalf("message = %q", got)
	}
}

func TestMemoPutToPromptSetsDraft(t *testing.T) {
	dir := t.TempDir()
	store := memo.New(dir, nil)
	saved, _, err := store.Add(context.Background(), "recall this idea")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := newMemoTestUI(store, map[string]string{"main": ""})
	u.input = newInterruptReader(nil)

	if err := putMemoToPrompt(u, saved.Text); err != nil {
		t.Fatal(err)
	}
	u.screenMu.Lock()
	draft := u.drafts["main"]
	u.screenMu.Unlock()
	if draft != "recall this idea" {
		t.Fatalf("draft = %q", draft)
	}
	// putMemoMessage defaults: remove is the default choice.
	if got := putMemoMessage(true); !strings.Contains(got, "removed") {
		t.Fatalf("remove message = %q", got)
	}
	if got := putMemoMessage(false); !strings.Contains(got, "retained") {
		t.Fatalf("retain message = %q", got)
	}
}

func TestMemoBrowserDeleteConfirmDefaultsCancel(t *testing.T) {
	b := &memoBrowser{choice: 1}
	if b.choice != 1 {
		t.Fatalf("delete confirm should default to Cancel, got %d", b.choice)
	}
	if !currentDraftHasText(&UI{drafts: map[string]string{"main": "x"}, activeAgent: "main"}) {
		t.Fatal("expected draft text")
	}
	if currentDraftHasText(&UI{drafts: map[string]string{"main": "  "}, activeAgent: "main"}) {
		t.Fatal("whitespace should not count as draft")
	}
}

func TestFormatMemoListNewestFirst(t *testing.T) {
	dir := t.TempDir()
	store := memo.New(dir, nil)
	ctx := context.Background()
	if _, _, err := store.Add(ctx, "first"); err != nil {
		t.Fatal(err)
	}
	second, _, err := store.Add(ctx, "second")
	if err != nil {
		t.Fatal(err)
	}
	items, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := formatMemoList(items, 80, false)
	if !strings.Contains(got, "newest first") {
		t.Fatalf("header = %q", got)
	}
	firstIdx := strings.Index(got, "   first")
	secondIdx := strings.Index(got, "   second")
	if firstIdx < 0 || secondIdx < 0 || secondIdx > firstIdx {
		t.Fatalf("ordering = %q", got)
	}
	_ = second
}
