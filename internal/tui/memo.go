package tui

import (
	"context"
	"fmt"
	"io"
	"strings"

	"qcode/internal/memo"
	"qcode/internal/redaction"
)

// memoStore is the UI-facing subset of *memo.FileStore. The concrete store is
// injected at startup; nil means memos are unavailable (demo/one-shot).
type memoStore interface {
	Snapshot(context.Context) ([]memo.Memo, error)
	Add(context.Context, string) (memo.Memo, int, error)
	Update(context.Context, string, string) (memo.Memo, error)
	Delete(context.Context, string) (int, error)
}

// SetMemoStore injects the global memo store. Nil disables /memo with a
// friendly message instead of failing.
func (u *UI) SetMemoStore(store memoStore) {
	u.screenMu.Lock()
	defer u.screenMu.Unlock()
	u.memoStore = store
}

func (u *UI) memoStoreOrNil() memoStore {
	u.screenMu.Lock()
	defer u.screenMu.Unlock()
	return u.memoStore
}

// handleMemoCommand implements /memo as a local UI-only command. It never
// submits model work, never touches the agent manager, and (except for
// put-to-prompt) never modifies drafts.
func (u *UI) handleMemoCommand(ctx context.Context, line string) {
	store := u.memoStoreOrNil()
	if store == nil {
		u.printSystemMessage(yellow + "Memos are unavailable in this run." + reset)
		return
	}
	arg := strings.TrimSpace(strings.TrimPrefix(line, "/memo"))
	if arg == "" {
		u.showMemoView(ctx)
		return
	}
	saved, total, err := store.Add(ctx, arg)
	if err != nil {
		u.printSystemMessage(yellow + "Unable to save memo: " + sanitizeDiffLine(err.Error(), "<ESC>") + reset)
		return
	}
	_ = saved
	u.clearMemoCommandDraft()
	u.printSystemMessage(green + fmt.Sprintf("Memo saved (%d total).", total) + reset)
}

// clearMemoCommandDraft drops the consumed "/memo ..." line from the draft
// without touching any prior unfinished draft.
func (u *UI) clearMemoCommandDraft() {
	u.screenMu.Lock()
	defer u.screenMu.Unlock()
	if draft, ok := u.drafts[u.activeAgent]; ok && strings.HasPrefix(strings.TrimSpace(draft), "/memo") {
		u.drafts[u.activeAgent] = ""
	}
}

func memoPreview(text string) string {
	text = sanitizeDiffLine(text, "<ESC>")
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return "(empty memo)"
	}
	return text
}

func formatMemoList(items []memo.Memo, width int, color bool) string {
	if len(items) == 0 {
		return "No memos yet. Use /memo {text} to save an idea."
	}
	style := func(text, code string) string {
		if color && code != "" {
			return code + text + reset
		}
		return text
	}
	var out strings.Builder
	out.WriteString(style(fmt.Sprintf("Memos: %d (newest first)", len(items)), bold))
	out.WriteString("\n")
	out.WriteString(style("Global scratchpad. Never sent to the model.", dim))
	for i, item := range items {
		preview := memoPreview(item.Text)
		stamp := item.CreatedAt.Local().Format("2006-01-02 15:04")
		out.WriteString("\n\n")
		fmt.Fprintf(&out, "%s", style(fmt.Sprintf("%d. %s", i+1, stamp), bold+cyan))
		out.WriteString("\n   " + style(preview, ""))
	}
	return out.String()
}

// showMemoView lists memos newest-first and supports view/edit/del/add/put.
func (u *UI) showMemoView(ctx context.Context) {
	store := u.memoStoreOrNil()
	if store == nil {
		u.printSystemMessage(yellow + "Memos are unavailable in this run." + reset)
		return
	}
	if u.input == nil || u.out == nil || !u.fixedInput {
		u.printSystemMessage(yellow + "Memo view requires an interactive terminal." + reset)
		return
	}
	memos, err := store.Snapshot(ctx)
	if err != nil {
		u.printSystemMessage(yellow + "Unable to load memos: " + sanitizeDiffLine(err.Error(), "<ESC>") + reset)
		return
	}
	memos = redactMemos(u, memos)
	if len(memos) == 0 {
		u.printSystemMessage(dim + "No memos yet. Use /memo {text} to save an idea." + reset)
		// Still open the view so add/put hints and Esc behavior are
		// discoverable even when empty.
	}

	u.input.setRaw(true)
	u.beginRawSelector()
	defer func() {
		u.input.setRaw(false)
		u.endRawSelector()
	}()

	u.screenMu.Lock()
	width, start, visible := u.width, u.latestPromptStartLocked(), u.commandViewHeightLocked()
	u.screenMu.Unlock()
	writer := memoViewWriter{ui: u, start: start}
	msg, err := showMemoBrowser(ctx, u, store, u.input, writer, memos, width, max(2, visible), ColorEnabled(u.out), u.unicode)
	u.input.setRaw(false)
	u.endRawSelector()
	// Balance the deferred endRawSelector/setRaw(false) which will now be
	// no-ops for an already-closed view.
	if err != nil && err != io.EOF {
		if ctx.Err() == nil {
			u.printSystemMessage(yellow + "Unable to show memos: " + sanitizeDiffLine(err.Error(), "<ESC>") + reset)
		}
	}
	u.clearMemoCommandDraft()
	if msg != "" {
		u.printSystemMessage(green + sanitizeDiffLine(msg, "<ESC>") + reset)
	}
}

func redactMemos(u *UI, memos []memo.Memo) []memo.Memo {
	if u.redaction == nil {
		return memos
	}
	out := make([]memo.Memo, len(memos))
	for i, m := range memos {
		m.Text = u.redaction.Text(redaction.Terminal, m.Text)
		out[i] = m
	}
	return out
}

type memoViewWriter struct {
	ui    *UI
	start int
}

func (w memoViewWriter) firstOutputRow() int { return max(2, w.start) }

func (w memoViewWriter) Write(data []byte) (int, error) {
	w.ui.screenMu.Lock()
	defer w.ui.screenMu.Unlock()
	return w.ui.out.Write(data)
}
