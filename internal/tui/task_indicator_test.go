package tui

import (
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"qcode/internal/session"
)

type countedHistory struct {
	historyDisplay
	snapshots int
}

func (h *countedHistory) Snapshot() historySnapshot {
	h.snapshots++
	return h.historyDisplay.Snapshot()
}

func TestFixedTaskIndicatorDoesNotRebuildIdleHistory(t *testing.T) {
	for _, status := range []session.Status{session.StatusIdle, session.StatusCompleted, session.StatusCancelled, session.StatusWaitingForApproval} {
		t.Run(string(status), func(t *testing.T) {
			u, _ := layoutFixture(t)
			u.manager.(*layoutManager).states["main"] = status
			history := &countedHistory{historyDisplay: u.display}
			u.display = history
			u.renderInput(inputPrompt, "draft", 3)
			initial, _ := u.out.Stat()
			history.snapshots = 0
			for i := 0; i < 10; i++ {
				u.drawTaskIndicator()
				u.refreshStatusBar()
			}
			if history.snapshots != 0 {
				t.Fatalf("idle ticks rebuilt history %d times", history.snapshots)
			}
			final, _ := u.out.Stat()
			if final.Size() != initial.Size() {
				t.Fatal("idle ticks wrote unchanged screen rows")
			}
		})
	}
}

type indicatorManager struct {
	*layoutManager
	progress *session.Progress
}

func TestFixedTaskIndicatorRestoresInvalidFrameOnce(t *testing.T) {
	for _, height := range []int{3, 24} {
		t.Run(fmt.Sprintf("height=%d", height), func(t *testing.T) {
			u, _ := layoutFixture(t)
			u.manager.(*layoutManager).states["main"] = session.StatusCompleted
			history := &countedHistory{historyDisplay: u.display}
			u.display = history
			u.renderInput(inputPrompt, "draft", 3)
			u.height = height
			u.inputFrame = ""
			u.drawTaskIndicator()
			if u.inputFrame == "" || len(u.inputScreenRows) != height+1 {
				t.Fatal("invalid screen frame was not restored")
			}
			history.snapshots = 0
			initial, _ := u.out.Stat()
			u.drawTaskIndicator()
			final, _ := u.out.Stat()
			if history.snapshots != 0 || initial.Size() != final.Size() {
				t.Fatal("restored idle screen kept repainting")
			}
		})
	}
}

func (m *indicatorManager) Summary(id string) (session.Summary, error) {
	summary, err := m.layoutManager.Summary(id)
	summary.Progress = m.progress
	return summary, err
}

func TestFixedTaskIndicatorAnimatesOnlyItsRow(t *testing.T) {
	u, _ := layoutFixture(t)
	now := time.UnixMilli(0)
	u.manager = &indicatorManager{layoutManager: u.manager.(*layoutManager),
		progress: &session.Progress{Summary: "Waiting for model response", StartedAt: now}}
	history := &countedHistory{historyDisplay: u.display}
	u.display = history
	u.renderInput(inputPrompt, "unfinished draft", 5)
	u.drawFixedTaskIndicatorLocked(now)
	previous := append([]string(nil), u.inputScreenRows...)
	row, column := u.inputCursorRow, u.inputCursorColumn
	history.snapshots = 0
	u.drawFixedTaskIndicatorLocked(now.Add(1100 * time.Millisecond))
	if history.snapshots != 0 {
		t.Fatal("spinner animation rebuilt history")
	}
	if !strings.Contains(u.inputScreenRows[u.inputTaskRow], "1s") || previous[u.inputTaskRow] == u.inputScreenRows[u.inputTaskRow] {
		t.Fatal("indicator did not advance")
	}
	for i := range previous {
		if i != u.inputTaskRow && previous[i] != u.inputScreenRows[i] {
			t.Fatalf("indicator changed screen row %d", i)
		}
	}
	if u.inputCursorRow != row || u.inputCursorColumn != column {
		t.Fatal("indicator moved the input cursor")
	}
	for _, mode := range []string{"history", "question", "raw"} {
		t.Run(mode, func(t *testing.T) {
			u.activeViewportLocked().browsing = mode == "history"
			u.questionPanel = nil
			if mode == "question" {
				u.questionPanel = &questionPanel{agentID: "main"}
			}
			if mode == "raw" {
				u.commandViewActive = true
			}
			u.drawFixedTaskIndicatorLocked(now)
			initial, _ := u.out.Stat()
			u.drawFixedTaskIndicatorLocked(now.Add(time.Second))
			final, _ := u.out.Stat()
			if final.Size() != initial.Size() || history.snapshots != 0 {
				t.Fatal("animation repainted a paused or modal view")
			}
		})
	}
}

func BenchmarkFixedIdleRefresh(b *testing.B) {
	for _, lines := range []int{10, 5000} {
		b.Run(fmt.Sprintf("lines=%d", lines), func(b *testing.B) {
			out, err := os.CreateTemp(b.TempDir(), "screen")
			if err != nil {
				b.Fatal(err)
			}
			defer out.Close()
			history := newHistoryWriter(io.Discard)
			for i := 0; i < lines; i++ {
				history.AddLine(fmt.Sprintf("\x1b[36mConversation line %d: \x1b[0m%s", i, strings.Repeat("context ", 20)))
			}
			u := &UI{fixedInput: true, out: out, width: 100, height: 30,
				inputLabel: inputPrompt, display: history,
				manager: &layoutManager{states: map[string]session.Status{"main": session.StatusCompleted}}, activeAgent: "main"}
			u.paintFixedLocked(0)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				u.drawTaskIndicator()
				u.refreshStatusBar()
			}
		})
	}
}

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
