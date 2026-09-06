package tui

import (
	"context"
	"os"
	"qcode/internal/learning"
	"strings"
	"testing"
)

func TestLearningApprovalAnswer(t *testing.T) {
	for _, answer := range []string{"y", "yes", " YES "} {
		if !approveLearningAnswer(answer) {
			t.Fatalf("rejected %q", answer)
		}
	}
	for _, answer := range []string{"", "no", "n", "continue", "yes please", "\x03"} {
		if approveLearningAnswer(answer) {
			t.Fatalf("accepted %q", answer)
		}
	}
}
func TestLearningDisplayPreservesReviewLines(t *testing.T) {
	got := learningDisplay("Before:\nold\nAfter:\nnew\x1b[31m")
	if strings.Count(got, "\n") != 3 || strings.Contains(got, "\x1b") || !strings.Contains(got, "<ESC>") {
		t.Fatal(got)
	}
}
func TestLearnCommandCompletion(t *testing.T) {
	got := matchingSlashCommands("/lea")
	if len(got) != 1 || got[0].name != "/learn" {
		t.Fatal(got)
	}
}

type reviewRunner struct{ approved bool }

func (*reviewRunner) Run(context.Context, string) error { return nil }
func (r *reviewRunner) Learn(ctx context.Context, _ string, approve learning.Approver) (string, error) {
	var err error
	r.approved, err = approve(ctx, "Before:\nold\nAfter:\nnew")
	if r.approved {
		return "Saved approved learning.", err
	}
	return "Learning cancelled.", err
}
func TestLearningTerminalReview(t *testing.T) {
	for _, answer := range []string{"y\r", "\r", "n\r"} {
		t.Run(answer, func(t *testing.T) {
			in, err := os.Open(os.DevNull)
			if err != nil {
				t.Fatal(err)
			}
			defer in.Close()
			out, err := os.CreateTemp(t.TempDir(), "terminal")
			if err != nil {
				t.Fatal(err)
			}
			defer out.Close()
			runner := &reviewRunner{}
			ui := New(in, out, runner, "test", "test", t.TempDir())
			for _, b := range []byte(answer) {
				ui.input.data <- b
			}
			ui.learn(context.Background(), "")
			if runner.approved != (answer == "y\r") {
				t.Fatal("terminal approval not respected")
			}
			output, err := os.ReadFile(out.Name())
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(output), "Before:") || !strings.Contains(string(output), "After:") || !strings.Contains(string(output), "Apply these global learning changes?") {
				t.Fatalf("missing review: %s", output)
			}
		})
	}
}
