package tui

import (
	"fmt"
	"strings"

	"qcode/internal/lineedit"
	"qcode/internal/redaction"
)

// todoPreviewRows caps the collapsed todo panel so the fixed window keeps
// room for the transcript, steer/queue section, and composer.
const todoPreviewRows = 3

// todoPanel belongs to screenMu, alongside the active tab's history viewport
// and queue panel. Each agent tab owns one; hasLive/lastText track the latest
// observed live list so a cleared list can still be archived to the
// transcript exactly once.
type todoPanel struct {
	expanded    bool
	anchor      int
	hasLive     bool
	completed   bool
	lastText    string
	lastVersion uint64
}

type todoItem struct {
	id     string
	title  string
	status string
}

// todoSnapshotController exposes the agent's execution checklist without
// coupling terminal code to the agent package. Agent implements it.
type todoSnapshotController interface {
	TodoSnapshot() (ids, titles, statuses []string, version uint64, ok bool)
	LatestTodoText() (string, bool)
}

func todoAllDone(items []todoItem) bool {
	if len(items) == 0 {
		return false
	}
	for _, item := range items {
		if item.status != "done" {
			return false
		}
	}
	return true
}

func (u *UI) activeTodoLocked() *todoPanel {
	if view := u.views[u.activeAgent]; view != nil {
		return &view.todo
	}
	return &u.todo
}

// todoSnapshotLocked reads the active agent's checklist. Callers must hold
// screenMu. It returns the redacted items, the done count, the mutation
// version, and whether a list exists.
func (u *UI) todoSnapshotLocked() (items []todoItem, done int, version uint64, ok bool) {
	source, ok := u.runner.(todoSnapshotController)
	if !ok || source == nil {
		return nil, 0, 0, false
	}
	ids, titles, statuses, version, ok := source.TodoSnapshot()
	if !ok || len(ids) == 0 {
		return nil, 0, version, false
	}
	items = make([]todoItem, len(ids))
	for i := range ids {
		status := ""
		if i < len(statuses) {
			status = statuses[i]
		}
		title := ""
		if i < len(titles) {
			title = u.redaction.Text(redaction.Terminal, titles[i])
		}
		items[i] = todoItem{id: ids[i], title: title, status: status}
		if status == "done" {
			done++
		}
	}
	return items, done, version, true
}

// todoTextLocked returns the redacted rendered checklist for archival and
// /todo show. Callers must hold screenMu.
func (u *UI) todoTextLocked() (string, bool) {
	source, ok := u.runner.(todoSnapshotController)
	if !ok || source == nil {
		return "", false
	}
	text, ok := source.LatestTodoText()
	if !ok {
		return "", false
	}
	return u.redaction.Text(redaction.Terminal, text), true
}

type todoDisplayRow struct {
	text   string
	done   bool
	status string
}

func todoStatusMarker(status string, unicodeEnabled bool) string {
	switch status {
	case "doing":
		return interfaceGlyph(unicodeEnabled, "◐ ", "[-] ")
	case "done":
		return "[x] "
	default:
		return "[ ] "
	}
}

// todoDisplayRows wraps checklist items into terminal rows, remembering
// each row's status so the renderer can apply theme-governed coloring
// (done/success, doing/warning) instead of plain dimming.
func todoDisplayRows(items []todoItem, width int, unicodeEnabled bool) []todoDisplayRow {
	rows := make([]todoDisplayRow, 0, len(items))
	for _, item := range items {
		plain := sanitizeDiffLine(strings.ReplaceAll(item.title, "\t", " "), "<ESC>")
		plain = strings.ReplaceAll(plain, "\r", " ")
		plain = strings.ReplaceAll(plain, "\n", " ")
		marker := todoStatusMarker(item.status, unicodeEnabled)
		for _, wrapped := range strings.Split(wrapANSI(marker+plain, max(1, width), "    "), "\n") {
			rows = append(rows, todoDisplayRow{text: wrapped, done: item.status == "done", status: item.status})
		}
	}
	return rows
}

func todoHeader(done, total, width int, expanded bool) string {
	action := "expand"
	if expanded {
		action = "close"
	}
	if width < 30 {
		return fmt.Sprintf("Todo %d/%d Alt+T", done, total)
	}
	return fmt.Sprintf("Todo %d/%d | Alt+T %s", done, total, action)
}

func (u *UI) toggleTodoPanel() {
	u.screenMu.Lock()
	defer u.screenMu.Unlock()
	if u.questionnaireActive {
		return
	}
	items, _, _, ok := u.todoSnapshotLocked()
	if !ok || len(items) == 0 || todoAllDone(items) {
		return
	}
	panel := u.activeTodoLocked()
	panel.expanded = !panel.expanded
	if !panel.expanded {
		panel.anchor = 0
	}
	u.repaintActiveLocked(0)
}

// handleTodoPanelKey collapses an expanded todo panel on Esc. It runs after
// the queue panel handler so an expanded queue keeps its Up/Down/Delete
// navigation. Callers must not hold screenMu.
func (u *UI) handleTodoPanelKey(key rune) bool {
	u.screenMu.Lock()
	defer u.screenMu.Unlock()
	if u.questionnaireActive {
		return false
	}
	panel := u.activeTodoLocked()
	if !panel.expanded {
		return false
	}
	if key != lineedit.KeyEscape {
		return false
	}
	panel.expanded = false
	panel.anchor = 0
	u.repaintActiveLocked(0)
	return true
}

// maybeArchiveTodo locks screenMu and runs the completion check for one view.
func (u *UI) maybeArchiveTodo(viewID string) {
	u.screenMu.Lock()
	defer u.screenMu.Unlock()
	u.maybeArchiveTodoLocked(viewID)
}

// discardActiveTodo drops the active tab's checklist panel state without
// archiving it to the transcript. /new uses it so a fresh session starts
// with no visible or cached todo.
func (u *UI) discardActiveTodo() {
	u.screenMu.Lock()
	defer u.screenMu.Unlock()
	panel := u.activeTodoLocked()
	panel.hasLive = false
	panel.completed = false
	panel.expanded = false
	panel.anchor = 0
	panel.lastText = ""
	panel.lastVersion = 0
}

// maybeArchiveTodoLocked archives the finished checklist to the transcript
// once, then hides the panel. A list counts as finished when every item is
// done or when the list disappears after being visible (cleared via /new).
// Only the active tab archives directly; background tabs archive when they
// become active via switchAgent. Callers must hold screenMu.
func (u *UI) maybeArchiveTodoLocked(viewID string) {
	if u.todoArchiving {
		return
	}
	view := u.views[viewID]
	if view == nil || view.display == nil {
		return
	}
	if viewID != u.activeAgent {
		return
	}
	panel := &view.todo
	items, _, version, ok := u.todoSnapshotLocked()
	if ok && len(items) > 0 {
		if todoAllDone(items) {
			if !panel.completed {
				panel.completed = true
				panel.expanded = false
				panel.anchor = 0
				panel.hasLive = false
				panel.lastText = ""
				if text, ok := u.todoTextLocked(); ok && strings.TrimSpace(text) != "" {
					u.archiveTodoLocked(view, text)
				}
			}
			return
		}
		panel.completed = false
		if !panel.hasLive || panel.lastVersion != version {
			panel.hasLive = true
			panel.lastVersion = version
			if text, ok := u.todoTextLocked(); ok {
				panel.lastText = text
			}
		}
		return
	}
	if panel.hasLive {
		panel.hasLive = false
		panel.expanded = false
		panel.anchor = 0
		last := panel.lastText
		panel.lastText = ""
		if strings.TrimSpace(last) != "" {
			u.archiveTodoLocked(view, last)
		}
	}
}

// archiveTodoLocked appends the finished checklist to the tab's transcript as
// its own group. Callers must hold screenMu.
func (u *UI) archiveTodoLocked(view *agentView, text string) {
	if view == nil || view.display == nil || strings.TrimSpace(text) == "" {
		return
	}
	u.todoArchiving = true
	defer func() { u.todoArchiving = false }()
	view.display.writeNoticeLocked(text)
}
