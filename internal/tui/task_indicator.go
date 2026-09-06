package tui

import (
	"context"
	"fmt"
	"time"

	"qcode/internal/session"
)

// watchTaskIndicator redraws the shared task indicator for whichever tab is
// active. Agent output never owns this row, which keeps background and main
// tabs visually consistent.
func (u *UI) watchTaskIndicator() func() {
	if u.manager == nil {
		return func() {}
	}
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				u.drawTaskIndicator()
			case <-done:
				return
			}
		}
	}()
	return func() { close(done) }
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
	if u.manager == nil {
		return false
	}
	u.screenMu.Lock()
	id := u.activeAgent
	u.screenMu.Unlock()
	summary, err := u.manager.Summary(id)
	return err == nil && summary.Status == session.StatusRunning
}

func (u *UI) drawTaskIndicator() {
	if u.manager == nil {
		return
	}
	u.screenMu.Lock()
	id, height, active := u.activeAgent, u.height, u.statusActive
	u.screenMu.Unlock()
	if !active || height < 4 {
		return
	}
	summary, err := u.manager.Summary(id)
	if err != nil {
		return
	}
	message := taskIndicatorMessage(summary.Status, u.unicode, time.Now())
	u.screenMu.Lock()
	defer u.screenMu.Unlock()
	if u.activeAgent != id || !u.statusActive || u.height < 4 {
		return
	}
	if u.taskIndicatorText == message {
		return
	}
	u.taskIndicatorText = message
	fmt.Fprintf(u.out, "\x1b[s\x1b[%d;1H\x1b[2K%s\x1b[u", u.height-1, message)
}

func taskIndicatorMessage(status session.Status, unicodeEnabled bool, now time.Time) string {
	if status != session.StatusRunning {
		return ""
	}
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	if !unicodeEnabled {
		frames = []string{"|", "/", "-", "\\"}
	}
	frame := frames[int(now.UnixMilli()/100)%len(frames)]
	return dim + "Waiting (" + frame + ")  Ctrl+C to cancel" + reset
}
