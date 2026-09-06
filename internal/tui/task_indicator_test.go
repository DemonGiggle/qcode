package tui

import (
	"strings"
	"testing"
	"time"

	"qcode/internal/session"
)

func TestTaskIndicatorMessageUsesSharedRunningState(t *testing.T) {
	now := time.UnixMilli(0)
	if got := taskIndicatorMessage(session.StatusIdle, true, now); got != "" {
		t.Fatalf("idle indicator = %q", got)
	}
	if got := taskIndicatorMessage(session.StatusRunning, true, now); !strings.Contains(got, "Waiting (⠋)") || !strings.Contains(got, "Ctrl+C to cancel") {
		t.Fatalf("unicode indicator = %q", got)
	}
	if got := taskIndicatorMessage(session.StatusRunning, false, now); !strings.Contains(got, "Waiting (|)") || strings.Contains(got, "⠋") {
		t.Fatalf("ASCII indicator = %q", got)
	}
}
