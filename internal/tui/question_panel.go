package tui

import (
	"fmt"
	"strings"

	"qcode/internal/question"
)

// questionPanel occupies the transcript area while an answer is being read.
// Its scroll position is independent of the agent's retained history viewport.
// All fields belong to screenMu.
type questionPanel struct {
	agentID  string
	item     question.Question
	index    int
	total    int
	footer   string
	top      int
	feedback string
}

func (u *UI) activeQuestionPanelLocked() *questionPanel {
	if u.fixedInput && u.questionPanel != nil && u.questionPanel.agentID == u.activeAgent {
		return u.questionPanel
	}
	return nil
}

func (p *questionPanel) rows(width, height, direction int, unicode bool) []string {
	if height <= 0 {
		return nil
	}
	formatted := formatQuestionWithFooter(p.item, p.index, p.total, width, p.footer)
	heading, body, _ := strings.Cut(formatted, "\n")
	if p.feedback != "" {
		body = p.feedback + "\n\n" + body
	}
	lines := strings.Split(wrapANSI(body, max(1, width), ""), "\n")
	visible := max(1, height-1)
	// Match transcript paging: Page Up is positive, Page Down is negative.
	p.top = max(0, min(p.top-direction*max(1, visible-2), max(0, len(lines)-visible)))
	if len(lines) > visible {
		heading = fmt.Sprintf("%s | %d-%d/%d", heading, p.top+1, min(len(lines), p.top+visible), len(lines))
	}
	if height == 1 {
		return []string{lines[p.top]}
	}
	rows := []string{truncateDiffLine(heading, width, unicode)}
	return append(rows, lines[p.top:min(len(lines), p.top+visible)]...)
}

func (u *UI) showQuestionFeedback(panel *questionPanel, message string) {
	u.screenMu.Lock()
	panel.feedback, panel.top = message, 0
	u.screenMu.Unlock()
	u.printSystemMessage(message)
}
