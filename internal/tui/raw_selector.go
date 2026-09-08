package tui

import "fmt"

// beginRawSelector confines the legacy multi-row selectors to the output
// viewport. Their cursor-relative drawing must never scroll the fixed footer.
func (u *UI) beginRawSelector() {
	if !u.fixedInput || u.out == nil || u.height < 5 {
		return
	}
	u.screenMu.Lock()
	defer u.screenMu.Unlock()
	lastOutputRow := u.height - 3 // prompt, task indicator, and status follow.
	fmt.Fprintf(u.out, "\x1b[2;%dr\x1b[2;1H", lastOutputRow)
	for row := 2; row <= lastOutputRow; row++ {
		fmt.Fprintf(u.out, "\x1b[%d;1H\x1b[2K", row)
	}
	fmt.Fprint(u.out, "\x1b[2;1H")
	u.inputFrame = ""
}

// endRawSelector restores the normal screen region after raw selector output
// has been cleared, then redraws the active output and fixed footer together.
func (u *UI) endRawSelector() {
	if !u.fixedInput || u.out == nil {
		return
	}
	u.screenMu.Lock()
	defer u.screenMu.Unlock()
	fmt.Fprint(u.out, "\x1b[r")
	u.inputFrame = ""
	u.paintFixedLocked(0)
}
