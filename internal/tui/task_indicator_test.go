package tui

import (
	"strings"
	"testing"
	"time"

	"qcode/internal/session"
)

func TestTaskIndicatorMessageUsesSharedRunningState(t *testing.T) {
	now := time.UnixMilli(0)
	if got := taskIndicatorMessage(session.StatusIdle, 0, true, now); got != "" {
		t.Fatalf("idle indicator = %q", got)
	}
	if got := taskIndicatorMessage(session.StatusRunning, 2, true, now); !strings.Contains(got, "Waiting (⠋)") || !strings.Contains(got, "2 queued") || !strings.Contains(got, "Ctrl+C to cancel") {
		t.Fatalf("unicode indicator = %q", got)
	}
	if got := taskIndicatorMessage(session.StatusRunning, 0, false, now); !strings.Contains(got, "Waiting (|)") || strings.Contains(got, "⠋") {
		t.Fatalf("ASCII indicator = %q", got)
	}
}
