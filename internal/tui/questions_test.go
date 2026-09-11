package tui

import (
	"strings"
	"testing"

	"qcode/internal/question"
)

func TestNormalizeQuestionAnswer(t *testing.T) {
	options := []string{"SQLite", "Postgres"}
	for _, test := range []struct {
		answer string
		want   string
		valid  bool
	}{
		{answer: "1", want: "SQLite", valid: true},
		{answer: "postgres", want: "Postgres", valid: true},
		{answer: "Something else", want: "Something else", valid: true},
	} {
		got, valid := normalizeQuestionAnswer(test.answer, options)
		if got != test.want || valid != test.valid {
			t.Errorf("normalizeQuestionAnswer(%q) = %q, %v; want %q, %v", test.answer, got, valid, test.want, test.valid)
		}
	}
}

func TestFormatQuestion(t *testing.T) {
	got := formatQuestion(question.Question{Text: "Which store?", Options: []string{"SQLite", "Postgres"}}, 0, 2, 80)
	for _, want := range []string{"Question 1/2", "Which store?", "1) SQLite", "2) Postgres", "type your own answer", "Ctrl+C cancels planning"} {
		if !strings.Contains(got, want) {
			t.Fatalf("questionnaire %q does not contain %q", got, want)
		}
	}
}
