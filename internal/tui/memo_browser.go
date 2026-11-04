package tui

import (
	"context"
	"fmt"
	"io"
	"strings"

	"qcode/internal/memo"
	"qcode/internal/redaction"
)

type memoBrowserMode int

const (
	memoModeList memoBrowserMode = iota
	memoModeDeleteConfirm
	memoModeReplaceConfirm
	memoModePutChoice
)

type memoBrowser struct {
	memos    []memo.Memo
	selected int
	start    int
	mode     memoBrowserMode
	choice   int // selection within confirm/choice modes
}

func (b *memoBrowser) clamp() {
	if len(b.memos) == 0 {
		b.selected, b.start, b.choice = 0, 0, 0
		return
	}
	if b.selected < 0 {
		b.selected = 0
	}
	if b.selected >= len(b.memos) {
		b.selected = len(b.memos) - 1
	}
	if b.choice < 0 {
		b.choice = 0
	}
}

func (b *memoBrowser) move(delta, visible int) {
	if len(b.memos) == 0 {
		return
	}
	b.selected = (b.selected + delta + len(b.memos)) % len(b.memos)
	b.start = selectorStart(b.selected, len(b.memos), max(1, visible), b.start)
}

func (b *memoBrowser) page(direction, visible int) {
	if len(b.memos) == 0 {
		return
	}
	b.selected, b.start = selectorPage(b.selected, b.start, len(b.memos), max(1, visible), direction)
}

func showMemoBrowser(ctx context.Context, u *UI, store memoStore, in io.Reader, out io.Writer, memos []memo.Memo, width, height int, color, unicodeEnabled bool) (string, error) {
	width, height = max(1, width), max(2, height)
	b := &memoBrowser{memos: memos, choice: 1}
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		renderMemoBrowser(out, b, width, height, color, unicodeEnabled)
		key, err := readSelectorKey(in)
		if err != nil {
			return "", err
		}
		visible := max(1, height-1)
		switch b.mode {
		case memoModeDeleteConfirm:
			switch key {
			case string([]byte{ctrlC}), "\x1b":
				b.mode = memoModeList
				b.choice = 1
			case arrowUpSequence, arrowDownSequence:
				if b.choice == 0 {
					b.choice = 1
				} else {
					b.choice = 0
				}
			case "\r", "\n":
				if b.choice == 0 {
					if len(b.memos) == 0 {
						b.mode = memoModeList
						continue
					}
					id := b.memos[b.selected].ID
					remaining, err := store.Delete(ctx, id)
					_ = remaining
					if err != nil {
						return "", err
					}
					fresh, err := store.Snapshot(ctx)
					if err != nil {
						return "", err
					}
					b.memos = redactMemos(u, fresh)
					b.clamp()
					if b.selected >= len(b.memos) {
						b.selected = max(0, len(b.memos)-1)
					}
					b.start = selectorStart(b.selected, len(b.memos), visible, 0)
				}
				b.mode = memoModeList
				b.choice = 1
			}
			continue
		case memoModeReplaceConfirm:
			switch key {
			case string([]byte{ctrlC}), "\x1b":
				b.mode = memoModeList
			case arrowUpSequence, arrowDownSequence:
				if b.choice == 0 {
					b.choice = 1
				} else {
					b.choice = 0
				}
			case "\r", "\n":
				if b.choice == 0 {
					b.mode = memoModePutChoice
					b.choice = 0
				} else {
					b.mode = memoModeList
				}
			}
			continue
		case memoModePutChoice:
			switch key {
			case string([]byte{ctrlC}), "\x1b":
				b.mode = memoModeList
				b.choice = 0
			case arrowUpSequence:
				b.choice = (b.choice + 2) % 3
			case arrowDownSequence:
				b.choice = (b.choice + 1) % 3
			case "\r", "\n":
				switch b.choice {
				case 0, 1:
					if len(b.memos) == 0 {
						b.mode = memoModeList
						b.choice = 0
						continue
					}
					chosen := b.memos[b.selected]
					remove := b.choice == 0
					if err := putMemoToPrompt(u, chosen.Text); err != nil {
						return "", err
					}
					if remove {
						if _, err := store.Delete(ctx, chosen.ID); err != nil {
							return "", err
						}
					}
					return putMemoMessage(remove), nil
				default:
					b.mode = memoModeList
					b.choice = 0
				}
			}
			continue
		}

		switch key {
		case string([]byte{ctrlC}), "\x1b":
			return "", nil
		case "q", "Q":
			return "", nil
		case arrowUpSequence:
			b.move(-1, visible)
		case arrowDownSequence:
			b.move(1, visible)
		case selectorPageUp:
			b.page(-1, visible)
		case selectorPageDown:
			b.page(1, visible)
		case "\r", "\n", "v", "V":
			if len(b.memos) == 0 {
				continue
			}
			res, err := showMemoDetail(in, out, b.memos[b.selected], b.selected+1, len(b.memos), width, height, color, unicodeEnabled)
			if err != nil {
				return "", err
			}
			if res == memoDetailClose {
				return "", nil
			}
		case "a", "A":
			if err := memoAddFlow(ctx, u, store, b); err != nil {
				return "", err
			}
		case "e", "E":
			if len(b.memos) == 0 {
				continue
			}
			if err := memoEditFlow(ctx, u, store, b); err != nil {
				return "", err
			}
		case "d", "D", string([]byte{127}), string([]byte{8}):
			if len(b.memos) == 0 {
				continue
			}
			b.mode = memoModeDeleteConfirm
			b.choice = 1 // Cancel default
		case "p", "P":
			if len(b.memos) == 0 {
				continue
			}
			if currentDraftHasText(u) {
				b.mode = memoModeReplaceConfirm
				b.choice = 1 // Cancel default; explicit opt-in to replace
				continue
			}
			b.mode = memoModePutChoice
			b.choice = 0 // Put and remove default
		}
	}
}

func currentDraftHasText(u *UI) bool {
	u.screenMu.Lock()
	defer u.screenMu.Unlock()
	if strings.TrimSpace(u.drafts[u.activeAgent]) != "" {
		return true
	}
	return strings.TrimSpace(u.inputText) != ""
}

func putMemoToPrompt(u *UI, text string) error {
	u.screenMu.Lock()
	agent := u.activeAgent
	u.drafts[agent] = text
	u.screenMu.Unlock()
	if u.input != nil {
		u.input.injectDraft(text)
	}
	return nil
}

func putMemoMessage(remove bool) string {
	if remove {
		return "Memo put to prompt and removed."
	}
	return "Memo put to prompt and retained."
}

func renderMemoBrowser(out io.Writer, b *memoBrowser, width, height int, color, unicodeEnabled bool) {
	header := fmt.Sprintf("Memos %d/%d newest-first | Up/Down PgUp/PgDn Enter view | a add e edit d del p put | Esc close", min(b.selected+1, len(b.memos)), len(b.memos))
	if len(b.memos) == 0 {
		header = "Memos 0/0 | a add memo | Esc close"
	}
	lines := make([]string, height)
	lines[0] = header
	visible := max(1, height-1)
	bodyRows := visible
	footer := ""
	switch b.mode {
	case memoModeDeleteConfirm:
		footer = "Delete selected memo? Up/Down Enter | Cancel default | Esc back"
	case memoModeReplaceConfirm:
		footer = "Composer has a draft. Replace with memo? Up/Down Enter | Cancel default | Esc back"
	case memoModePutChoice:
		footer = "Put memo: Up/Down Enter | Put+remove default | Esc back"
	default:
		if len(b.memos) == 0 {
			footer = "No memos yet. Press a to add, Esc to close. Usage: /memo {text}"
		} else {
			footer = "Enter view | a add e edit d del p put | Esc close"
		}
	}
	// Reserve last row for footer when possible.
	listRows := bodyRows
	if height > 2 {
		listRows = bodyRows - 1
		lines[height-1] = footer
	}
	if len(b.memos) == 0 {
		if listRows > 1 {
			lines[1] = "  No memos yet. Use /memo {text} to save an idea."
		}
	} else {
		if b.start > max(0, len(b.memos)-listRows) {
			b.start = max(0, len(b.memos)-listRows)
		}
		for row := 0; row < listRows && b.start+row < len(b.memos); row++ {
			idx := b.start + row
			m := b.memos[idx]
			marker := "  "
			if idx == b.selected {
				marker = "> "
			}
			stamp := m.CreatedAt.Local().Format("2006-01-02 15:04")
			line := marker + stamp + "  " + memoPreview(m.Text)
			if color && idx == b.selected {
				line = cyan + bold + line + reset
			}
			lines[row+1] = line
		}
	}
	// Choice rows overlay the footer area for confirm/put modes.
	if b.mode != memoModeList && height > 3 {
		var options []string
		switch b.mode {
		case memoModeDeleteConfirm:
			options = []string{"Delete", "Cancel"}
		case memoModeReplaceConfirm:
			options = []string{"Replace draft", "Cancel"}
		case memoModePutChoice:
			options = []string{"Put and remove", "Put and retain", "Cancel"}
		}
		optLine := ""
		for i, opt := range options {
			marker := "  "
			label := opt
			if i == b.choice {
				marker = "> "
				if color {
					label = cyan + bold + opt + reset
				}
			}
			if i > 0 {
				optLine += "   "
			}
			optLine += marker + label
		}
		lines[height-2] = optLine
	}
	renderMemoRows(out, lines, width, unicodeEnabled)
}

func renderMemoRows(out io.Writer, lines []string, width int, unicodeEnabled bool) {
	var output strings.Builder
	for row, line := range lines {
		fmt.Fprintf(&output, "\x1b[%d;1H\x1b[2K%s\x1b[0m", row+selectorFirstOutputRow(out), truncateDiffLine(line, width, unicodeEnabled))
	}
	_, _ = io.WriteString(out, output.String())
}

type memoDetailResult int

const (
	memoDetailBack memoDetailResult = iota
	memoDetailClose
)

func showMemoDetail(in io.Reader, out io.Writer, m memo.Memo, position, total, width, height int, color, unicodeEnabled bool) (memoDetailResult, error) {
	pager := newPlanPager(m.Text, width)
	for {
		renderMemoDetail(out, pager, position, total, width, height, color, unicodeEnabled)
		key, err := readSelectorKey(in)
		if err != nil {
			return memoDetailBack, err
		}
		visible := max(1, height-2)
		switch key {
		case string([]byte{ctrlC}), "q", "Q":
			return memoDetailClose, nil
		case "\x1b", string([]byte{8}), string([]byte{127}):
			return memoDetailBack, nil
		case selectorPageUp:
			pager.page(-1, visible)
		case selectorPageDown:
			pager.page(1, visible)
		case arrowUpSequence:
			pager.move(-1, visible)
		case arrowDownSequence:
			pager.move(1, visible)
		case "\x1b[H", "g":
			pager.home()
		case "\x1b[F", "G":
			pager.end(visible)
		}
	}
}

func renderMemoDetail(out io.Writer, pager planPager, position, total, width, height int, color, unicodeEnabled bool) {
	header := fmt.Sprintf("Memo %d/%d | Up/Down PgUp/PgDn Home/End | Esc back | q close", position, total)
	visible := max(1, height-2)
	if pager.maxTop(visible) > 0 {
		header += fmt.Sprintf(" | Lines %d-%d/%d", pager.top+1, min(len(pager.lines), pager.top+visible), len(pager.lines))
	}
	if color {
		header = dim + header + reset
	}
	lines := make([]string, height)
	if height > 1 {
		lines[1] = header
	}
	for row := 2; row < height; row++ {
		idx := pager.top + row - 2
		if idx < len(pager.lines) {
			lines[row] = pager.lines[idx]
		}
	}
	renderMemoRows(out, lines, width, unicodeEnabled)
}

// memoAddFlow and memoEditFlow temporarily leave raw-selector mode to reuse
// the normal line editor (with bracketed-paste multiline support), then
// re-enter the browser. Empty input cancels.
func memoAddFlow(ctx context.Context, u *UI, store memoStore, b *memoBrowser) error {
	text, ok, err := memoPromptForText(u, "", "New memo (empty cancels): ")
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	if _, _, err := store.Add(ctx, text); err != nil {
		return err
	}
	fresh, err := store.Snapshot(ctx)
	if err != nil {
		return err
	}
	b.memos = redactMemos(u, fresh)
	b.selected = 0
	b.start = 0
	return nil
}

func memoEditFlow(ctx context.Context, u *UI, store memoStore, b *memoBrowser) error {
	if len(b.memos) == 0 {
		return nil
	}
	current := b.memos[b.selected].Text
	text, ok, err := memoPromptForText(u, current, "Edit memo (empty cancels): ")
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	updated, err := store.Update(ctx, b.memos[b.selected].ID, text)
	if err != nil {
		return err
	}
	if u.redaction != nil {
		updated.Text = u.redaction.Text(redaction.Terminal, updated.Text)
	}
	b.memos[b.selected] = updated
	return nil
}

func memoPromptForText(u *UI, prefill, prompt string) (string, bool, error) {
	if u.input == nil || u.terminal == nil {
		return "", false, fmt.Errorf("memo editing requires an interactive terminal")
	}
	// Leave the modal screen region so the composer is visible for editing.
	u.input.setRaw(false)
	u.endRawSelector()
	defer func() {
		u.input.setRaw(true)
		u.beginRawSelector()
	}()
	if prefill != "" {
		u.input.injectDraft(prefill)
	}
	u.terminal.SetPrompt(yellow + prompt + reset)
	line, err := u.readLine()
	u.terminal.SetPrompt(u.currentInputPrompt())
	if u.terminal.SubmissionKey == 27 {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if strings.TrimSpace(line) == "" {
		return "", false, nil
	}
	return line, true, nil
}
