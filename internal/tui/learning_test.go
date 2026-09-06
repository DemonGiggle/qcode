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
	r.approved, err = approve(ctx, []learning.Change{{After: &learning.Learning{Topic: "Go testing", Content: "Run focused tests first.", Tags: []string{"go"}}}})
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
			if !strings.Contains(string(output), "Add: Go testing") || !strings.Contains(string(output), "Run focused tests first.") || !strings.Contains(string(output), "Apply these global learning changes?") {
				t.Fatalf("missing review: %s", output)
			}
		})
	}
}

func TestLearningReviewShowsContentWithoutStorageMetadata(t *testing.T) {
	item := &learning.Learning{Version: 1, ID: "hidden-record-id", Source: "hidden-session-id", Topic: "Separate game logic from DOM", Content: "Keep game rules in a module without DOM dependencies so they can be unit tested.", Tags: []string{"javascript", "testing"}}
	changes := []learning.Change{{After: item}}
	for _, color := range []bool{false, true} {
		output := formatLearningReview(changes, 60, color)
		for _, want := range []string{"Add: Separate game logic from DOM", "javascript, testing", "Available in every workspace"} {
			if !strings.Contains(output, want) {
				t.Fatalf("missing %q: %s", want, output)
			}
		}
		for _, hidden := range []string{item.ID, item.Source, `"After"`, `"version"`, `"created_at"`} {
			if strings.Contains(output, hidden) {
				t.Fatalf("leaked metadata %q", hidden)
			}
		}
		if strings.Contains(output, "\x1b[") != color {
			t.Fatal("incorrect color behavior")
		}
		for _, line := range strings.Split(output, "\n") {
			if visibleWidth(line) > 60 {
				t.Fatalf("line exceeds terminal: %q", line)
			}
		}
	}
}

func TestLearningReviewPreservesUpdatesAndRemovals(t *testing.T) {
	old := &learning.Learning{Topic: "Old title", Content: "Original guidance.", Tags: []string{"go"}}
	updated := &learning.Learning{Topic: "New title", Content: "Replacement guidance.\nKeep this condition explicit.", Tags: []string{"testing"}}
	output := formatLearningReview([]learning.Change{{Before: old, After: updated}, {Before: old}}, 100, false)
	for _, want := range []string{"Update: New title", "Previous title: Old title", "Was: Original guidance.", "Replacement guidance.", "Keep this condition explicit.", "Previous tags: go", "Tags: testing", "Remove: Old title"} {
		if !strings.Contains(output, want) {
			t.Fatalf("review omitted %q: %s", want, output)
		}
	}
	updated.Content = "untrusted\x1b[2Jtext"
	output = formatLearningReview([]learning.Change{{After: updated}}, 100, false)
	if strings.Contains(output, "\x1b") || !strings.Contains(output, "<ESC>") {
		t.Fatal("untrusted terminal controls passed through")
	}
}

func TestLearningListIsReadableAndKeepsForgetID(t *testing.T) {
	item := learning.Learning{Version: 1, ID: "record-123", Source: "hidden-session", Topic: "Go testing", Content: "Run focused tests first.\nRun the full suite before merging.", Tags: []string{"go", "testing"}}
	for _, color := range []bool{false, true} {
		output := formatLearningList([]learning.Learning{item}, 60, color)
		for _, want := range []string{"Global learning: 1 record(s)", "Use an ID with /learn forget", "<id>.", "1. Go testing", "ID: record-123", "Run focused tests first.", "Run the full suite before merging.", "Tags: go, testing"} {
			if !strings.Contains(output, want) {
				t.Fatalf("missing %q: %s", want, output)
			}
		}
		for _, hidden := range []string{item.Source, `"version"`, "created_at"} {
			if strings.Contains(output, hidden) {
				t.Fatalf("leaked metadata %q: %s", hidden, output)
			}
		}
		if strings.Contains(output, "\x1b[") != color {
			t.Fatal("incorrect color behavior")
		}
		for _, line := range strings.Split(output, "\n") {
			if visibleWidth(line) > 60 {
				t.Fatalf("line exceeds terminal: %q", line)
			}
		}
	}
	if got := formatLearningList(nil, 80, false); got != "No global learning stored." {
		t.Fatalf("empty list = %q", got)
	}
}
