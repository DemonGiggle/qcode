package tui

import (
	"fmt"
	"strings"

	"qcode/internal/session"
)

// queuePanel belongs to screenMu, alongside the active tab's history viewport.
type queuePanel struct {
	expanded  bool
	anchorID  string
	anchorRow int
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

func queuePreview(prompt string) string {
	return strings.Join(strings.Fields(sanitizeDiffLine(strings.ReplaceAll(prompt, "\n", " "), "<ESC>")), " ")
}

func queueDisplayRows(items []session.QueuedPrompt, width int) []queuedDisplayRow {
	var rows []queuedDisplayRow
	for i, item := range items {
		rowNumber := 0
		for lineIndex, line := range strings.Split(item.Prompt, "\n") {
			prefix := "   "
			if lineIndex == 0 {
				prefix = fmt.Sprintf("%d. ", i+1)
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
