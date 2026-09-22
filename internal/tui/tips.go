package tui

import (
	"fmt"
	"strings"
	"time"
)

// tipVivid is the vivid 256-color orange-gold used for the startup tip.
// It is intentionally distinct from the dim/green/cyan/yellow banner text.
const tipVivid = "\x1b[1;38;5;220m"

// tipTexts holds every startup tip. Each entry is written to render as one or
// two wrapped lines at 80 columns once prefixed with "Tip: ".
var tipTexts = []string{
	"Type `/` to see matching slash commands, keep typing to filter, and press Tab to complete the first match.",
	"Run `/help` to list commands, or `/help <command>` (e.g. `/help model`) for arguments and examples.",
	"Run `/model` to search the provider catalog with Up/Down + Enter, or `/model <id> [thinking]` to set it directly.",
	"Run `/tool` to toggle tools with Space + Enter, or `/tool <name> on|off`; web tools start disabled until you enable them.",
	"Run `/skill` to pick reusable instruction bundles; only selected skills reach the model and its `skill` tool.",
	"Run `/agent new [name]`, `/agent list`, `/agent switch <id>`, `/agent rename <id> <name>`, `/agent cancel <id>`, `/agent close <id>`.",
	"Switch agent tabs with Ctrl+PgUp/PgDn or Alt+,/. — each tab keeps its own draft, history, model, and tool settings.",
	"Press PgUp/PgDn to scroll history while running; scrolling pauses live output, PgDn to the bottom resumes it.",
	"Press Ctrl+C to cancel the running prompt, a picker, or a `/bash` command without exiting qcode.",
	"Keep typing while the agent works — extra prompts queue as `Queued #N` and run in FIFO order.",
	"Run `/history` to search completed prompts newest-first; Enter views one response, Esc returns to the live view.",
	"`write` and `edit` show 10-line diff previews; run `/diff` or `/diff N` to expand one up to 200 lines.",
	"Run `/plan` for read-only Plan mode, `/plan show` to review, `/plan act` to implement, `/plan off` to leave.",
	"Run `/resume` to restore autosaved sessions, `/new` for a fresh start, `/clear` to redraw this header, `/exit` or `/quit` to leave.",
	"Run `/bash <cmd>` to run a shell command, `/export [path]` to save HTML, `/remote` for browser control.",
	"Run `/learn` to propose durable preferences, `/learn list` for IDs, `/learn forget <id>` to remove one, `/learn compact` to deduplicate.",
	"Run `/verbose` for timestamped tool traces, `/maxsteps [N]` to show or change the per-request turn limit.",
	"Edit input with Left/Right, Home/End, Ctrl/Alt+arrows or Alt+B/F by word, Ctrl+W to delete a word, Ctrl+A/E for line ends.",
	"In pickers type to filter, move with Up/Down or PgUp/PgDn, Enter to apply, Esc to go back, Ctrl+C to cancel all.",
	"Run `/compact` to summarize old context; watch STEP, CONTEXT % left, and MODE PLAN in the status bar.",
}

// startupTipIndex is chosen once per process launch so the header tip stays
// stable across /clear redraws but varies between restarts.
var startupTipIndex = pickStartupTipIndex()

func pickStartupTipIndex() int {
	if len(tipTexts) == 0 {
		return 0
	}
	return int(time.Now().UnixNano()%int64(len(tipTexts))) % len(tipTexts)
}

// startupTip returns the single random tip selected for this process.
func startupTip() string {
	if len(tipTexts) == 0 {
		return ""
	}
	index := startupTipIndex % len(tipTexts)
	if index < 0 {
		index = 0
	}
	return tipTexts[index]
}

// formatTip renders the startup tip for the given width. The result is at
// most two visual lines and uses vivid styling only when color is enabled.
func formatTip(width int, colorEnabled bool) string {
	tip := startupTip()
	if tip == "" {
		return ""
	}
	line := "Tip: " + tip
	if width > 0 {
		line = wrapANSI(line, width, "  ")
		lines := strings.Split(line, "\n")
		if len(lines) > 2 {
			lines = lines[:2]
		}
		line = strings.Join(lines, "\n")
	}
	if !colorEnabled {
		return line
	}
	return tipVivid + line + reset
}

// printTip prints the startup tip below the Tools enabled/disabled block.
func (u *UI) printTip() {
	color := false
	if u.out != nil {
		color = ColorEnabled(u.out)
	}
	formatted := formatTip(u.width, color)
	if formatted == "" {
		return
	}
	fmt.Fprintln(u.display, formatted)
	fmt.Fprintln(u.display)
}
