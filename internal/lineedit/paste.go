package lineedit

import (
	"fmt"
	"sort"
	"strings"
)

// pastedRange uses rune offsets into the full input. The editor treats each
// range as one block while retaining its original text for submission.
type pastedRange struct{ start, end int }

// Dual collapse rule: only long or tall pastes become [paste N].
// Short pastes stay inline and editable; the placeholder itself is ~22 runes,
// so collapsing anything shorter would hide useful context to save no space.
const (
	pasteCollapseThreshold = 100
	pasteCollapseMaxLines  = 3
)

func pasteLineCount(runes []rune) int {
	if len(runes) == 0 {
		return 0
	}
	s := string(runes)
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	// A single trailing newline is the usual clipboard terminator, not an
	// extra empty line: "a\n" is 1 line, "a\nb\nc\n" is 3 lines.
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return 1
	}
	return strings.Count(s, "\n") + 1
}

func shouldCollapsePaste(runes []rune) bool {
	if len(runes) >= pasteCollapseThreshold {
		return true
	}
	return pasteLineCount(runes) > pasteCollapseMaxLines
}

// ShouldCollapsePaste reports whether pasted runes meet the dual collapse
// rule (long or tall). The prompt restores long single-line drafts through
// the same bracketed path so they stay collapsed after a session restore.
func ShouldCollapsePaste(runes []rune) bool { return shouldCollapsePaste(runes) }

func (t *Terminal) editableLength() int {
	length := len(t.line)
	for _, block := range t.pastes {
		length -= block.end - block.start
	}
	return length
}

func pasteLabel(characters int) []rune {
	return []rune(fmt.Sprintf("[paste %d characters]", characters))
}

func (t *Terminal) finishPaste() {
	if len(t.pasteBuffer) > 0 {
		start := t.pos
		collapse := shouldCollapsePaste(t.pasteBuffer)
		t.replaceRunes(start, start, t.pasteBuffer)
		t.pos = start + len(t.pasteBuffer)
		if collapse {
			t.pastes = append(t.pastes, pastedRange{start, t.pos})
			sort.Slice(t.pastes, func(i, j int) bool { return t.pastes[i].start < t.pastes[j].start })
		}
	}
	t.pasteBuffer = t.pasteBuffer[:0]
	t.pasteActive = false
	if t.echo && t.RenderInput == nil {
		t.clearAndRepaintLinePlusNPrevious(t.maxLine)
	}
}

// replaceRunes updates the offsets of paste blocks after an edit. Callers
// place edit boundaries outside blocks so deleting a block removes it whole.
func (t *Terminal) replaceRunes(start, end int, inserted []rune) {
	delta := len(inserted) - (end - start)
	kept := t.pastes[:0]
	for _, block := range t.pastes {
		switch {
		case block.end <= start:
			kept = append(kept, block)
		case block.start >= end:
			block.start += delta
			block.end += delta
			kept = append(kept, block)
		}
	}
	t.pastes = kept
	if delta > 0 {
		t.line = append(t.line, make([]rune, delta)...)
		copy(t.line[start+len(inserted):], t.line[end:len(t.line)-delta])
	} else {
		copy(t.line[start+len(inserted):], t.line[end:])
		t.line = t.line[:len(t.line)+delta]
	}
	copy(t.line[start:], inserted)
}

func (t *Terminal) blockBoundary(pos int, right bool) int {
	for _, block := range t.pastes {
		if pos > block.start && pos < block.end {
			if right {
				return block.end
			}
			return block.start
		}
	}
	return pos
}

// replaceLine preserves paste blocks when autocomplete inserts ordinary text.
func (t *Terminal) replaceLine(line []rune, pos int) {
	start := 0
	for start < len(t.line) && start < len(line) && t.line[start] == line[start] {
		start++
	}
	oldEnd, newEnd := len(t.line), len(line)
	for oldEnd > start && newEnd > start && t.line[oldEnd-1] == line[newEnd-1] {
		oldEnd--
		newEnd--
	}
	t.replaceRunes(start, oldEnd, line[start:newEnd])
	t.pos = t.blockBoundary(pos, true)
	if t.echo && t.RenderInput == nil {
		t.clearAndRepaintLinePlusNPrevious(t.maxLine)
	}
}

func (t *Terminal) displayLine() ([]rune, int) {
	if len(t.pastes) == 0 && !t.pasteActive {
		return t.line, t.pos
	}
	var display []rune
	pos, start := 0, 0
	for _, block := range t.pastes {
		if t.pos >= start && t.pos <= block.start {
			pos = len(display) + t.pos - start
		}
		display = append(display, t.line[start:block.start]...)
		display = append(display, pasteLabel(block.end-block.start)...)
		if t.pos > block.start && t.pos <= block.end {
			pos = len(display)
		}
		start = block.end
	}
	if t.pos >= start {
		pos = len(display) + t.pos - start
	}
	display = append(display, t.line[start:]...)
	if t.pasteActive && len(t.pasteBuffer) > 0 {
		if !shouldCollapsePaste(t.pasteBuffer) {
			tail := append([]rune(nil), display[pos:]...)
			display = append(display[:pos], t.pasteBuffer...)
			pos += len(t.pasteBuffer)
			display = append(display, tail...)
			return display, pos
		}
		label := pasteLabel(len(t.pasteBuffer))
		tail := append([]rune(nil), display[pos:]...)
		display = append(display[:pos], label...)
		pos = len(display)
		display = append(display, tail...)
	}
	return display, pos
}

func (t *Terminal) displayLineAt(pos int) ([]rune, int) {
	previous := t.pos
	t.pos = pos
	display, displayPos := t.displayLine()
	t.pos = previous
	return display, displayPos
}

// DisplayInput projects a RenderInput snapshot into its compact display and
// cursor position. The full line passed to RenderInput remains the draft.
func (t *Terminal) DisplayInput(line string, pos int) (string, int) {
	t.lock.Lock()
	defer t.lock.Unlock()
	if !t.echo {
		return "", 0
	}
	if line != string(t.line) || pos != t.pos {
		return line, pos
	}
	display, displayPos := t.displayLine()
	return string(display), displayPos
}

func (t *Terminal) collapseMultiline() {
	t.pastes = nil
	if strings.ContainsAny(string(t.line), "\r\n") && shouldCollapsePaste(t.line) {
		t.pastes = []pastedRange{{0, len(t.line)}}
	}
}
