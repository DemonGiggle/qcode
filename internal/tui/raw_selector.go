package tui

import "fmt"

const commandViewHint = "Ctrl+C closes view | Esc back"

// beginRawSelector gives command views the area between tabs and status.
// Conversation controls are hidden until the view returns to the composer.
func (u *UI) beginRawSelector() {
	if !u.fixedInput || u.out == nil || u.height < 5 {
		return
	}
	u.screenMu.Lock()
	defer u.screenMu.Unlock()
	u.commandViewStatusRows = u.statusLinesLocked()
	u.commandViewActive = true
	lastOutputRow := u.statusScrollBottomLocked()
	firstOutputRow := 2
	fmt.Fprintf(u.out, "\x1b[%d;%dr\x1b[%d;1H\x1b[?7l", firstOutputRow, lastOutputRow, firstOutputRow)
	for row := firstOutputRow; row <= u.statusTaskRowLocked(); row++ {
		fmt.Fprintf(u.out, "\x1b[%d;1H\x1b[2K%s", row, (themeWriter{palette: u.outputTheme(), color: true, width: u.width}).paintRow(""))
	}
	fmt.Fprintf(u.out, "\x1b[?7h\x1b[%d;1H", firstOutputRow)
	u.statusBarText = ""
	u.drawStatusBarLocked()
	u.inputFrame = ""
}

func (u *UI) commandViewHeightLocked() int {
	return max(1, u.statusScrollBottomLocked()-1)
}

func (u *UI) drawCommandViewHintLocked() {
	hint := truncateDiffLine(commandViewHint, u.width, u.unicode)
	hint = renderThemeStatusBarLine(hint, u.width, u.outputTheme(), ColorEnabled(u.out))
	fmt.Fprintf(u.out, "\x1b[s\x1b[?7l\x1b[%d;1H\x1b[2K%s\x1b[?7h\x1b[u", u.statusTaskRowLocked(), hint)
}

func selectorFirstOutputRow(out interface{}) int {
	if positioned, ok := out.(interface{ firstOutputRow() int }); ok {
		return positioned.firstOutputRow()
	}
	return 2
}

// endRawSelector restores the normal screen region after raw selector output
// has been cleared, then redraws the active output and fixed footer together.
func (u *UI) endRawSelector() {
	if !u.fixedInput || u.out == nil {
		return
	}
	u.screenMu.Lock()
	defer u.screenMu.Unlock()
	u.commandViewActive = false
	u.commandViewStatusRows = 0
	u.statusBarText = ""
	fmt.Fprint(u.out, "\x1b[r")
	u.inputFrame = ""
	u.paintFixedLocked(0)
}
