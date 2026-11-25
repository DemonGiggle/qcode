package tui

import (
	"strings"
	"testing"

	"qcode/internal/question"
)

func TestParseQuestionCommand(t *testing.T) {
	for _, test := range []struct {
		answer string
		want   questionCommand
		target int
	}{
		{"/back", questionCmdBack, 0},
		{"/prev", questionCmdBack, 0},
		{"/next", questionCmdNext, 0},
		{"/done", questionCmdDone, 0},
		{"/submit", questionCmdDone, 0},
		{"/2", questionCmdJump, 1},
		{"/4", questionCmdJump, 3},
		{"/5", questionCmdNone, 0},
		{"/0", questionCmdNone, 0},
		{"/next question", questionCmdNone, 0},
		{"2", questionCmdNone, 0},
		{"a/path", questionCmdNone, 0},
	} {
		got, target := parseQuestionCommand(test.answer, 4)
		if got != test.want || target != test.target {
			t.Errorf("parseQuestionCommand(%q) = %v, %d; want %v, %d", test.answer, got, target, test.want, test.target)
		}
	}
}

func TestNextQuestionIndexWrapsToUnansweredAndReview(t *testing.T) {
	if got := nextQuestionIndex([]string{"a", "", ""}, 0); got != 1 {
		t.Errorf("sequential next = %d, want 1", got)
	}
	if got := nextQuestionIndex([]string{"a", "", "c"}, 2); got != 1 {
		t.Errorf("wrapped next = %d, want 1", got)
	}
	if got := nextQuestionIndex([]string{"a", "b", "c"}, 1); got != 3 {
		t.Errorf("complete next = %d, want review step 3", got)
	}
	if got := firstUnanswered([]string{"a", "b"}); got != -1 {
		t.Errorf("firstUnanswered = %d, want -1", got)
	}
}

func TestFormatQuestionWithAnswerAddsCurrentAnswer(t *testing.T) {
	item := question.Question{Text: "Which store?", Options: []string{"SQLite", "Postgres"}, AllowCustom: true}
	plain := formatQuestionWithFooter(item, 0, 2, 80, "Ctrl+C cancels.")
	if withAnswer := formatQuestionWithAnswer(item, 0, 2, 80, "Ctrl+C cancels.", ""); withAnswer != plain {
		t.Fatal("empty current answer changed the inline question layout")
	}
	withAnswer := formatQuestionWithAnswer(item, 0, 2, 80, "Ctrl+C cancels.", "SQLite")
	if !strings.Contains(withAnswer, "Current answer: SQLite") || !strings.Contains(withAnswer, "2) Postgres") {
		t.Fatalf("revisited question lost its options or current answer:\n%s", withAnswer)
	}
}

func TestFormatQuestionReviewShowsUnansweredQuestions(t *testing.T) {
	items := navigationQuestions()
	review := formatQuestionReview(items, []string{"Postgres", "", "Redis"}, 60, "Ctrl+C cancels.")
	for _, want := range []string{"Review your answers (3/3)", "Which database?", "Postgres", "(unanswered)", "Redis", "/back or /1-3"} {
		if !strings.Contains(review, want) {
			t.Errorf("review missing %q:\n%s", want, review)
		}
	}
}

func TestQuestionPanelRowsFitNarrowScreen(t *testing.T) {
	u, _ := layoutFixture(t)
	u.width, u.height = 20, 24
	u.screenMu.Lock()
	u.questionPanel = &questionPanel{questions: navigationQuestions(), answers: []string{"SQLite", "", ""}, index: 1}
	rows := u.questionPanelRowsLocked(3)
	u.screenMu.Unlock()
	if len(rows) != 3 {
		t.Fatalf("sticky rows = %d, want 3", len(rows))
	}
	if !strings.Contains(rows[0], "Question 2/3") || !strings.Contains(rows[1], "Q2: Why that one?") || !strings.Contains(rows[2], "2*") {
		t.Fatalf("sticky rows lost progress: %q", rows)
	}
	for _, row := range rows {
		if width := visibleWidth(plainHistoryText(row)); width > 20 {
			t.Errorf("sticky row is %d cells wide on a 20-cell screen: %q", width, row)
		}
	}
	u.screenMu.Lock()
	reviewRows := u.questionPanelRowsLocked(0)
	u.questionPanel.index = 3
	oneRow := u.questionPanelRowsLocked(1)
	u.screenMu.Unlock()
	if len(reviewRows) != 0 || len(oneRow) != 1 {
		t.Fatalf("sticky limit ignored: limit0=%d limit1=%d", len(reviewRows), len(oneRow))
	}
}
