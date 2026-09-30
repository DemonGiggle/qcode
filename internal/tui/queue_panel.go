package tui

import (
	"fmt"
	"strings"

	"qcode/internal/lineedit"
	"qcode/internal/session"
)

// queuePanel belongs to screenMu, alongside the active tab's history viewport.
type queuePanel struct {
	selectedID string
	expanded   bool
	anchorID   string
	anchorRow  int
}

type queuedDisplayRow struct {
	id   string
	row  int
	text string
}

type queuedPromptReader interface {
	QueuedPrompts(string) []session.QueuedPrompt
}

func (u *UI) queuedPromptsLocked() []session.QueuedPrompt {
	if source, ok := u.manager.(promptController); ok {
		return source.PendingInputs(u.activeAgent)
	}
	if source, ok := u.manager.(queuedPromptReader); ok {
		return source.QueuedPrompts(u.activeAgent)
	}
	return nil
}

func (u *UI) activeQueueLocked() *queuePanel {
	if view := u.views[u.activeAgent]; view != nil {
		return &view.queue
	}
	return &u.queue
}

func (u *UI) toggleQueuePanel() {
	u.screenMu.Lock()
	defer u.screenMu.Unlock()
	if len(u.queuedPromptsLocked()) == 0 {
		return
	}
	panel := u.activeQueueLocked()
	panel.expanded = !panel.expanded
	if panel.expanded {
		panel.anchorID, panel.anchorRow = "", 0
	}
	u.repaintActiveLocked(0)
}

func queueDisplayRows(items []session.QueuedPrompt, width int) []queuedDisplayRow {
	var rows []queuedDisplayRow
	queueNumber := 0
	for _, item := range items {
		if item.Intent != session.IntentSteer {
			queueNumber++
		}
		rowNumber := 0
		for lineIndex, line := range strings.Split(item.Prompt, "\n") {
			prefix := "   "
			if lineIndex == 0 {
				prefix = fmt.Sprintf("%d. ", queueNumber)
				if item.Intent == session.IntentSteer {
					prefix = "Steer (" + string(item.State) + "): "
				}
			}
			if lineIndex == 0 && item.Source != "" {
				prefix += "[" + item.Source + ": " + item.Actor + "] "
			}
			plain := sanitizeDiffLine(strings.ReplaceAll(line, "\t", " "), "<ESC>")
			for _, wrapped := range strings.Split(wrapANSI(prefix+plain, max(1, width), "   "), "\n") {
				rows = append(rows, queuedDisplayRow{id: item.RequestID, row: rowNumber, text: wrapped})
				rowNumber++
			}
		}
	}
	return rows
}

func (p *queuePanel) page(rows []queuedDisplayRow, size, direction int) []queuedDisplayRow {
	if len(rows) == 0 || size <= 0 {
		return nil
	}
	start := 0
	if p.anchorID != "" {
		for i, row := range rows {
			if row.id == p.anchorID && row.row <= p.anchorRow {
				start = i
			}
		}
	}
	start = max(0, min(start-direction*size, max(0, len(rows)-size)))
	p.anchorID, p.anchorRow = rows[start].id, rows[start].row
	return rows[start:min(len(rows), start+size)]
}

func (u *UI) handlePendingPanelKey(key rune) bool {
	u.screenMu.Lock()
	panel := u.activeQueueLocked()
	if !panel.expanded {
		u.screenMu.Unlock()
		return false
	}
	items := u.queuedPromptsLocked()
	if len(items) == 0 {
		panel.expanded = false
		u.screenMu.Unlock()
		return false
	}
	selected := 0
	for i, item := range items {
		if item.RequestID == panel.selectedID {
			selected = i
			break
		}
	}
	switch key {
	case lineedit.KeyEscape:
		panel.expanded = false
	case lineedit.KeyUp:
		selected = max(0, selected-1)
	case lineedit.KeyDown:
		selected = min(len(items)-1, selected+1)
	case lineedit.KeyDelete:
		id := items[selected].RequestID
		agentID := u.activeAgent
		u.screenMu.Unlock()
		if host, ok := u.manager.(promptController); ok {
			if err := host.CancelInput(agentID, id); err != nil {
				u.printSystemMessage("Unable to remove pending input: " + err.Error())
			}
		}
		u.repaintActive()
		return true
	default:
		u.screenMu.Unlock()
		return false
	}
	panel.selectedID = items[selected].RequestID
	panel.anchorID = panel.selectedID
	panel.anchorRow = 0
	u.repaintActiveLocked(0)
	u.screenMu.Unlock()
	return true
}
