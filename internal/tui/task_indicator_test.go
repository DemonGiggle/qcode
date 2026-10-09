package tui

import (
	"strings"
	"testing"
	"time"

	"qcode/internal/session"
)

func TestTaskIndicatorMessageUsesSharedRunningState(t *testing.T) {
	now := time.UnixMilli(0)
	if got := taskIndicatorMessage(session.Summary{Status: session.StatusIdle}, true, now); got != "" {
		t.Fatalf("idle indicator = %q", got)
	}
	progress := &session.Progress{Summary: "Waiting for model response", StartedAt: now}
	if got := taskIndicatorMessage(session.Summary{Status: session.StatusRunning, QueueDepth: 2, Progress: progress}, true, now); !strings.Contains(got, "Waiting (⠋)") || !strings.Contains(got, "2 queued") || !strings.Contains(got, "Ctrl+C to cancel") {
		t.Fatalf("unicode indicator = %q", got)
	}
	if got := taskIndicatorMessage(session.Summary{Status: session.StatusRunning, Progress: progress}, false, now); !strings.Contains(got, "Waiting (|)") || strings.Contains(got, "⠋") {
		t.Fatalf("ASCII indicator = %q", got)
	}
	if got := taskIndicatorMessage(session.Summary{Status: session.StatusRunning, QueueDepth: 2}, true, now); strings.Contains(got, "Waiting") || !strings.Contains(got, "2 queued") || !strings.Contains(got, "Ctrl+C to cancel") {
		t.Fatalf("tool indicator = %q", got)
	}
}

func TestTaskIndicatorExplainsOperationAndElapsedTime(t *testing.T) {
	now := time.UnixMilli(0)
	summary := session.Summary{
		Status: session.StatusRunning, QueueDepth: 1,
		Progress: &session.Progress{Summary: "Waiting for model response", StartedAt: now.Add(-65 * time.Second)},
	}
	for _, unicodeEnabled := range []bool{true, false} {
		got := taskIndicatorMessage(summary, unicodeEnabled, now)
		for _, want := range []string{summary.Progress.Summary, "1m5s", "1 queued", "Ctrl+C to cancel"} {
			if !strings.Contains(got, want) {
				t.Errorf("indicator missing %q: %q", want, got)
			}
		}
	}
	summary.Status = session.StatusWaitingForApproval
	got := taskIndicatorMessage(summary, true, now)
	if !strings.Contains(got, "Waiting for input") || strings.Contains(got, "model response") {
		t.Fatalf("approval indicator = %q", got)
	}
	summary.Status = session.StatusCompleted
	if got := taskIndicatorMessage(summary, true, now); got != "" {
		t.Fatalf("completed indicator = %q", got)
	}
}
