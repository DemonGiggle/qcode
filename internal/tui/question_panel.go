package tui

import (
	"fmt"
	"strings"

	"qcode/internal/lineedit"
	"qcode/internal/question"
	"qcode/internal/redaction"
)

// questionPanel is the sticky status area for a multi-question questionnaire.
// It stays above the transcript while the user moves between questions, so
// paging through earlier output never hides the current question, the answer
// count, or the navigation hints. All fields belong to screenMu. An index of
// len(questions) is the review step, where answers can still be revised.
type questionPanel struct {
	questions []question.Question // redacted for display
	answers   []string            // redacted for display; "" means unanswered
	index     int
}

// questionPanelRowsLocked renders up to limit rows of the sticky area. The
// caller holds screenMu. One-question questionnaires have no panel: their
// inline question and prompt keep the compact original layout.
func (u *UI) questionPanelRowsLocked(limit int) []string {
	panel := u.questionPanel
	if panel == nil || limit <= 0 {
		return nil
	}
	width := max(1, u.width)
	total := len(panel.questions)
	answered := 0
	for _, answer := range panel.answers {
		if answer != "" {
			answered++
		}
	}
	header := ""
	if panel.index >= total {
		header = fmt.Sprintf("Review %d/%d | Enter submit | /back or /1-%d revise | Ctrl+C cancel", total, total, total)
	} else {
		header = fmt.Sprintf("Question %d/%d | %d/%d answered | /back /next /1-%d | /done submit | Ctrl+C cancel",
			panel.index+1, total, answered, total, total)
	}
	header = truncateDiffLine(header, width, u.unicode)
	if ColorEnabled(u.out) {
		header = cyan + bold + header + reset
	}
	rows := []string{header}
	if limit < 2 {
		return rows
	}
	// Keep the question itself pinned as well: paging through older output must
	// not hide what is being answered.
	questionRow := "All answers are listed in the transcript below."
	if panel.index < total {
		text := strings.Join(strings.Fields(panel.questions[panel.index].Text), " ")
		questionRow = fmt.Sprintf("Q%d: %s", panel.index+1, sanitizeDiffLine(text, "<ESC>"))
		if limit < 3 && panel.index < len(panel.answers) && panel.answers[panel.index] != "" {
			questionRow += " | Answer: " + panel.answers[panel.index] + " (Enter keeps)"
		}
	}
	questionRow = truncateDiffLine(questionRow, width, u.unicode)
	if ColorEnabled(u.out) {
		questionRow = cyan + questionRow + reset
	}
	rows = append(rows, questionRow)
	if limit < 3 {
		return rows
	}

	var progress strings.Builder
	for i := range panel.questions {
		if i > 0 {
			progress.WriteByte(' ')
		}
		marker := interfaceGlyph(u.unicode, "○", "o")
		if i < len(panel.answers) && panel.answers[i] != "" {
			marker = interfaceGlyph(u.unicode, "✓", "+")
		}
		if i == panel.index {
			marker = interfaceGlyph(u.unicode, "●", "*")
		}
		fmt.Fprintf(&progress, "%d%s", i+1, marker)
	}
	detail := ""
	switch {
	case panel.index >= total:
		detail = " | " + interfaceGlyph(u.unicode, "●", "*") + " current, " +
			interfaceGlyph(u.unicode, "✓", "+") + " answered, " +
			interfaceGlyph(u.unicode, "○", "o") + " unanswered"
	case panel.index < len(panel.answers) && panel.answers[panel.index] != "":
		detail = " | Answer: " + panel.answers[panel.index] + " (Enter keeps it)"
	default:
		detail = " | No answer yet"
	}
	row := truncateDiffLine("Answers "+progress.String()+detail, width, u.unicode)
	if ColorEnabled(u.out) {
		row = dim + row + reset
	}
	return append(rows, row)
}

// setQuestionPanel publishes questionnaire state for the sticky area. The
// caller must not hold screenMu.
func (u *UI) setQuestionPanel(index int, questions []question.Question, answers []string) {
	u.screenMu.Lock()
	defer u.screenMu.Unlock()
	u.questionPanel = &questionPanel{questions: questions, answers: answers, index: index}
}

// displayQuestionAnswers keeps secrets and control characters out of the
// sticky area and the review summary without changing the raw answers sent
// back to the agent.
func (u *UI) displayQuestionAnswers(answers []string) []string {
	displayed := make([]string, len(answers))
	for i, answer := range answers {
		displayed[i] = u.displayQuestionAnswer(answer)
	}
	return displayed
}

func (u *UI) displayQuestionAnswer(answer string) string {
	text := sanitizeDiffLine(u.redaction.Text(redaction.Terminal, answer), "<ESC>")
	return strings.Join(strings.Fields(text), " ")
}

// questionNavKeyLocked turns Alt+Left/Right into questionnaire navigation.
// It submits the uncommitted answer line so the questionnaire loop can move to
// the previous or next question; the submitted text is discarded before answer
// handling. Injection is asynchronous, so a full input buffer cannot block
// the line editor that called this key handler. The caller holds screenMu.
func (u *UI) questionNavKeyLocked(key rune) bool {
	if !u.questionnaireActive || u.questionPanel == nil || u.input == nil {
		return false
	}
	switch key {
	case lineedit.KeyAltLeft:
		u.questionNav = -1
	case lineedit.KeyAltRight:
		u.questionNav = 1
	default:
		return false
	}
	u.input.inject([]byte("\r"))
	return true
}

// takeQuestionNav consumes a pending Alt+Left/Right navigation request.
func (u *UI) takeQuestionNav() int {
	u.screenMu.Lock()
	defer u.screenMu.Unlock()
	direction := u.questionNav
	u.questionNav = 0
	return direction
}
