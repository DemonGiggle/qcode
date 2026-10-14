package tui

import (
	"context"
	"fmt"
	"time"

	"qcode/internal/session"
)

// watchTaskIndicator refreshes the status bar and shared task indicator for
// whichever tab is active, including while input or a tool is blocking.
func (u *UI) watchTaskIndicator() func() {
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		indicatorTicker := time.NewTicker(100 * time.Millisecond)
		defer indicatorTicker.Stop()
		statusTicker := time.NewTicker(2 * time.Second)
		defer statusTicker.Stop()
		for {
			select {
			case <-statusTicker.C:
				u.refreshStatusBar()
			case <-indicatorTicker.C:
				u.drawTaskIndicator()
			case <-done:
				return
			}
		}
	}()
	return func() {
		close(done)
		<-stopped
	}
}

func (u *UI) signalUIEvent() {
	if u.uiEvents == nil {
		return
	}
	select {
	case u.uiEvents <- struct{}{}:
	default:
	}
}

func (u *UI) waitForAgentEvent(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-u.uiEvents:
		return nil
	}
}

func (u *UI) activeAgentRunning() bool {
	u.screenMu.Lock()
	id := u.activeAgent
	manager := u.manager
	u.screenMu.Unlock()
	if manager == nil {
		return false
	}
	summary, err := manager.Summary(id)
	return err == nil && summary.Status == session.StatusRunning
}

func (u *UI) drawTaskIndicator() {
	if u.fixedInput {
		u.screenMu.Lock()
		u.drawFixedTaskIndicatorLocked(time.Now())
		u.screenMu.Unlock()
		return
	}
	u.screenMu.Lock()
	id, height, active := u.activeAgent, u.height, u.statusActive
	manager := u.manager
	u.screenMu.Unlock()
	if manager == nil || !active || height < 4 {
		return
	}
	summary, err := manager.Summary(id)
	if err != nil {
		return
	}
	message := taskIndicatorMessage(summary, u.unicode, time.Now())
	u.screenMu.Lock()
	defer u.screenMu.Unlock()
	if u.manager != manager || u.activeAgent != id || !u.statusActive || u.height < 4 {
		return
	}
	if u.activeViewportLocked().browsing {
		message = truncateDiffLine("History paused | PgUp/PgDn | PgDn to bottom resumes", u.width, u.unicode)
		if summary.Status == session.StatusRunning {
			message = truncateDiffLine("Running | History paused | PgUp/PgDn | Ctrl+C cancel", u.width, u.unicode)
		}
	}
	if u.taskIndicatorText == message {
		return
	}
	u.taskIndicatorText = message
	fmt.Fprintf(u.out, "\x1b[s\x1b[?7l\x1b[%d;1H\x1b[2K%s\x1b[?7h\x1b[u", u.statusTaskRowLocked(), renderThemeStatusBarLine(message, u.width, u.outputTheme(), true))
}

func (u *UI) fixedTaskIndicatorMessageLocked(summary session.Summary, now time.Time) string {
	message := taskIndicatorMessage(summary, u.unicode, now)
	if u.activeQuestionPanelLocked() != nil {
		message = "PgUp/PgDn scroll question | Ctrl+C cancel"
	} else if u.activeViewportLocked().browsing {
		message = "History paused | PgUp/PgDn | PgDn to bottom resumes"
	}
	return truncateDiffLine(message, u.width, u.unicode)
}

// Timer ticks update only the indicator row. Rebuilding the transcript here
// makes idle CPU and every spinner frame grow with the conversation history.
func (u *UI) drawFixedTaskIndicatorLocked(now time.Time) {
	if u.commandViewActive {
		return
	}
	if u.input != nil {
		u.input.mu.Lock()
		raw := u.input.raw
		u.input.mu.Unlock()
		if raw {
			return
		}
	}
	if u.inputFrame == "" || len(u.inputScreenRows) != u.height+1 {
		u.paintFixedLocked(0)
		return
	}
	if u.inputTaskRow <= 0 {
		return
	}
	var summary session.Summary
	if u.manager != nil {
		summary, _ = u.manager.Summary(u.activeAgent)
	}
	message := u.fixedTaskIndicatorMessageLocked(summary, now)
	if message == u.inputScreenRows[u.inputTaskRow] {
		return
	}
	rows := append([]string(nil), u.inputScreenRows...)
	rows[u.inputTaskRow] = message
	u.writeFixedScreenLocked(rows, u.inputCursorRow, u.inputCursorColumn)
}

func taskIndicatorMessage(summary session.Summary, unicodeEnabled bool, now time.Time) string {
	if summary.Status == session.StatusClosing {
		return dim + "Closing agent | Cancellation pending" + reset
	}
	if summary.Status == session.StatusWaitingForApproval {
		return dim + "Waiting for input | Ctrl+C to cancel | Enter steer | Tab queue | Esc review" + reset
	}
	if summary.Status != session.StatusRunning {
		return ""
	}
	hints := "Ctrl+C to cancel | Enter steer | Tab queue"
	queued := ""
	if summary.QueueDepth > 0 {
		queued = fmt.Sprintf("%d queued", summary.QueueDepth)
	}
	progress := summary.Progress
	if progress == nil || progress.Summary == "" {
		if queued != "" {
			hints = queued + "  " + hints
		}
		return dim + hints + reset
	}
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	if !unicodeEnabled {
		frames = []string{"|", "/", "-", "\\"}
	}
	frame := frames[int(now.UnixMilli()/100)%len(frames)]
	separator := interfaceGlyph(unicodeEnabled, " · ", " | ")
	operation := separator + progress.Elapsed(now).String() + separator + sanitizeDiffLine(progress.Summary, "<ESC>")
	if queued != "" {
		queued = separator + queued
	}
	return dim + "Waiting (" + frame + ")" + operation + queued + "  " + hints + reset
}
