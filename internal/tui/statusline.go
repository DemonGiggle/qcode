package tui

import (
	"fmt"
	"io"
	"strings"
)

// Status line segments. The model ID controls the combined provider prefix and
// [MODEL name] unit as a single toggle.
const (
	statusSegmentRemote = "remote"
	statusSegmentMode   = "mode"
	statusSegmentModel  = "model"
	statusSegmentThink  = "think"
	statusSegmentWS     = "ws"
	statusSegmentCtx    = "ctx"
	statusSegmentStep   = "step"
	statusSegmentTok    = "tok"
)

// statusDisplayOrder is the left-to-right order. It follows priority:
// remote > mode > model > think > ws > ctx > step > tok.
var statusDisplayOrder = []string{
	statusSegmentRemote,
	statusSegmentMode,
	statusSegmentModel,
	statusSegmentThink,
	statusSegmentWS,
	statusSegmentCtx,
	statusSegmentStep,
	statusSegmentTok,
}

// statusDropOrder lists segments from lowest to highest priority. Narrow
// terminals drop from the front of this list, so token totals go first and
// the remote badge goes last.
var statusDropOrder = []string{
	statusSegmentTok,
	statusSegmentStep,
	statusSegmentCtx,
	statusSegmentWS,
	statusSegmentThink,
	statusSegmentModel,
	statusSegmentMode,
	statusSegmentRemote,
}

// statusPersistOrder is the canonical high-to-low priority order used when
// storing statusline_hidden so the config stays deterministic.
var statusPersistOrder = []string{
	statusSegmentRemote,
	statusSegmentMode,
	statusSegmentModel,
	statusSegmentThink,
	statusSegmentWS,
	statusSegmentCtx,
	statusSegmentStep,
	statusSegmentTok,
}

type statuslineOption struct {
	id          string
	label       string
	description string
}

// statuslineOptions returns the toggle list in display (priority) order.
func statuslineOptions() []statuslineOption {
	return []statuslineOption{
		{id: statusSegmentRemote, label: "remote", description: "REMOTE badge while browser control is running"},
		{id: statusSegmentMode, label: "mode", description: "MODE PLAN / INTERACTIVE when active"},
		{id: statusSegmentModel, label: "model", description: "Provider prefix with [MODEL name] as one unit"},
		{id: statusSegmentThink, label: "think", description: "THINK level when the model reports one"},
		{id: statusSegmentWS, label: "ws", description: "Workspace path, shortened before segments drop"},
		{id: statusSegmentCtx, label: "ctx", description: "Remaining context, e.g. CTX 73% left"},
		{id: statusSegmentStep, label: "step", description: "Model-turn progress, e.g. STEP 2/32"},
		{id: statusSegmentTok, label: "tok", description: "Session token totals, e.g. TOK I:1.2K O:340"},
	}
}

func isValidStatuslineID(id string) bool {
	clean := strings.ToLower(strings.TrimSpace(id))
	for _, candidate := range statusPersistOrder {
		if clean == candidate {
			return true
		}
	}
	return false
}

// normalizeStatuslineHidden lowercases, trims, de-duplicates, validates, and
// returns hidden IDs in canonical high-to-low priority order.
func normalizeStatuslineHidden(hidden []string) ([]string, error) {
	seen := make(map[string]bool, len(hidden))
	for _, segment := range hidden {
		clean := strings.ToLower(strings.TrimSpace(segment))
		if clean == "" {
			continue
		}
		if !isValidStatuslineID(clean) {
			return nil, fmt.Errorf("unknown statusline segment %q (want one of remote, mode, model, think, ws, ctx, step, tok)", segment)
		}
		seen[clean] = true
	}
	normalized := make([]string, 0, len(seen))
	for _, candidate := range statusPersistOrder {
		if seen[candidate] {
			normalized = append(normalized, candidate)
		}
	}
	return normalized, nil
}

func statusHiddenSet(hidden []string) map[string]bool {
	set := make(map[string]bool, len(hidden))
	for _, segment := range hidden {
		clean := strings.ToLower(strings.TrimSpace(segment))
		if clean != "" {
			set[clean] = true
		}
	}
	return set
}

// SetStatuslineHidden replaces the user-visible status bar denylist. Unknown
// IDs are ignored so older sessions and hand-edited configs degrade to showing
// every known segment rather than failing to render.
func (u *UI) SetStatuslineHidden(hidden []string) {
	normalized, err := normalizeStatuslineHidden(hidden)
	if err != nil {
		normalized = nil
		for _, segment := range hidden {
			clean := strings.ToLower(strings.TrimSpace(segment))
			if clean != "" && isValidStatuslineID(clean) {
				normalized = append(normalized, clean)
			}
		}
	}
	u.screenMu.Lock()
	defer u.screenMu.Unlock()
	if len(normalized) == 0 {
		u.statuslineHidden = nil
		return
	}
	u.statuslineHidden = append([]string(nil), normalized...)
}

// StatuslineHidden returns the current hidden-segment denylist in canonical
// order. The result is a copy.
func (u *UI) StatuslineHidden() []string {
	u.screenMu.Lock()
	defer u.screenMu.Unlock()
	return append([]string(nil), u.statuslineHidden...)
}

// statuslineHiddenLocked returns the hidden set assuming screenMu is held.
func (u *UI) statuslineHiddenLocked() map[string]bool {
	return statusHiddenSet(u.statuslineHidden)
}

func shortenModeLabel(mode string, short bool) string {
	if short && mode == "INTERACTIVE" {
		return "INT"
	}
	return mode
}

// renderFilteredStatusBar builds the bar from the active segment set. dropped
// contains width-driven removals in addition to hidden. ws is the workspace
// path to display (already shortened by the caller). shortMode selects the
// compact INT label.
func renderFilteredStatusBar(provider, model, ws string, unicodeEnabled, color, remote bool, modelColor, workspaceColor string, labels []string, hidden, dropped map[string]bool, shortMode bool) string {
	ctx := ""
	if len(labels) > 0 {
		ctx = labels[0]
	}
	tok := ""
	if len(labels) > 1 {
		tok = labels[1]
	}
	step := ""
	if len(labels) > 2 {
		step = labels[2]
	}
	mode := ""
	if len(labels) > 3 {
		mode = shortenModeLabel(labels[3], shortMode)
	}
	think := ""
	if len(labels) > 4 {
		think = labels[4]
	}

	active := func(id, value string, always bool) bool {
		if hidden[id] || dropped[id] {
			return false
		}
		if always {
			return true
		}
		return value != ""
	}
	// CTX and TOK always have a value on the UI path ("unknown" fallback), so
	// they are controlled only by hidden/dropped, never by emptiness.
	showRemote := remote && !hidden[statusSegmentRemote] && !dropped[statusSegmentRemote]
	showModel := !hidden[statusSegmentModel] && !dropped[statusSegmentModel]
	showCtx := !hidden[statusSegmentCtx] && !dropped[statusSegmentCtx] && len(labels) > 0
	showWS := !hidden[statusSegmentWS] && !dropped[statusSegmentWS]
	showTok := !hidden[statusSegmentTok] && !dropped[statusSegmentTok] && len(labels) > 1
	showStep := active(statusSegmentStep, step, false)
	showMode := active(statusSegmentMode, mode, false)
	showThink := active(statusSegmentThink, think, false)
	_ = ctx
	_ = tok

	if !color {
		parts := []string{}
		if showRemote {
			parts = append(parts, remoteStatusBadge(false))
		}
		if showMode {
			parts = append(parts, "[MODE "+mode+"]")
		}
		if showModel {
			parts = append(parts, provider, "[MODEL "+model+"]")
		}
		if showThink {
			parts = append(parts, "[THINK "+think+"]")
		}
		if showWS {
			parts = append(parts, "[WS "+ws+"]")
		}
		if showCtx {
			parts = append(parts, "[CTX "+ctx+"]")
		}
		if showStep {
			parts = append(parts, "[STEP "+step+"]")
		}
		if showTok {
			parts = append(parts, "[TOK "+tok+"]")
		}
		return strings.Join(parts, " ")
	}

	segments := []string{}
	if showRemote {
		segments = append(segments, remoteStatusBadge(true))
	}
	if showMode {
		segments = append(segments, statusSegment("MODE", mode, yellow))
	}
	if showModel {
		segments = append(segments, statusValue(provider, cyan), statusSegment("MODEL", model, modelColor))
	}
	if showThink {
		segments = append(segments, statusSegment("THINK", think, magenta))
	}
	if showWS {
		segments = append(segments, statusSegment("WS", ws, workspaceColor))
	}
	if showCtx {
		segments = append(segments, statusSegment("CTX", ctx, green))
	}
	if showStep {
		segments = append(segments, statusSegment("STEP", step, yellow))
	}
	if showTok {
		segments = append(segments, statusSegment("TOK", tok, cyan))
	}
	separator := dim + "  │  " + reset
	if !unicodeEnabled {
		separator = dim + "  |  " + reset
	}
	return strings.Join(segments, separator)
}

// renderStatusBarWithPriority fits enabled segments to width using the
// approved priority: remote > mode > model > think > ws > ctx > step > tok.
// Display order follows the same priority. The
// workspace path is shortened before any segment drops, and whole-bar
// truncation with an ellipsis is the final fallback.
func renderStatusBarWithPriority(provider, model, root string, width int, unicodeEnabled, color, remote bool, hidden map[string]bool, modelColor, workspaceColor string, labels []string) string {
	if hidden == nil {
		hidden = map[string]bool{}
	}
	dropped := map[string]bool{}
	shortMode := false

	render := func(ws string) string {
		return renderFilteredStatusBar(provider, model, ws, unicodeEnabled, color, remote, modelColor, workspaceColor, labels, hidden, dropped, shortMode)
	}

	// Cap long workspace paths even on wide terminals, matching the historic
	// maxWorkspaceStatusWidth behavior.
	shortened := func(available int) string {
		if available > maxWorkspaceStatusWidth {
			available = maxWorkspaceStatusWidth
		}
		return shortenWorkspacePath(root, available, unicodeEnabled)
	}

	fitWorkspace := func() string {
		if width <= 0 || hidden[statusSegmentWS] || dropped[statusSegmentWS] {
			return render(root)
		}
		base := render("")
		available := width - visibleWidth(base)
		if available > maxWorkspaceStatusWidth {
			available = maxWorkspaceStatusWidth
		}
		if available > 0 && visibleWidth(root) > available {
			return render(shortened(available))
		}
		return render(root)
	}

	finished := func(bar string) bool {
		return width <= 0 || visibleWidth(bar) <= width
	}

	bar := fitWorkspace()
	if finished(bar) {
		if color {
			return bar + reset
		}
		return bar
	}

	// Squeeze INTERACTIVE to INT before dropping whole segments.
	hasInteractive := len(labels) > 3 && labels[3] == "INTERACTIVE" && !hidden[statusSegmentMode] && !dropped[statusSegmentMode]
	if hasInteractive {
		shortMode = true
		bar = fitWorkspace()
		if finished(bar) {
			if color {
				return bar + reset
			}
			return bar
		}
	}

	for _, id := range statusDropOrder {
		if hidden[id] || dropped[id] {
			continue
		}
		// Skip empty optional segments; dropping them changes nothing.
		switch id {
		case statusSegmentRemote:
			if !remote {
				continue
			}
		case statusSegmentStep:
			if len(labels) <= 2 || labels[2] == "" {
				continue
			}
		case statusSegmentMode:
			if len(labels) <= 3 || labels[3] == "" {
				continue
			}
		case statusSegmentThink:
			if len(labels) <= 4 || labels[4] == "" {
				continue
			}
		case statusSegmentCtx:
			if len(labels) == 0 {
				continue
			}
		case statusSegmentTok:
			if len(labels) <= 1 {
				continue
			}
		}
		// Keep at least one segment so the bar never renders empty when the
		// width allows a single ellipsis.
		remaining := 0
		for _, candidate := range statusDisplayOrder {
			if hidden[candidate] || dropped[candidate] || candidate == id {
				continue
			}
			switch candidate {
			case statusSegmentRemote:
				if !remote {
					continue
				}
			case statusSegmentStep:
				if len(labels) <= 2 || labels[2] == "" {
					continue
				}
			case statusSegmentMode:
				if len(labels) <= 3 || labels[3] == "" {
					continue
				}
			case statusSegmentThink:
				if len(labels) <= 4 || labels[4] == "" {
					continue
				}
			case statusSegmentCtx:
				if len(labels) == 0 {
					continue
				}
			case statusSegmentTok:
				if len(labels) <= 1 {
					continue
				}
			}
			remaining++
		}
		if remaining == 0 {
			break
		}
		dropped[id] = true
		bar = fitWorkspace()
		if finished(bar) {
			if color {
				return bar + reset
			}
			return bar
		}
	}

	if !color {
		return truncateDiffLine(bar, width, unicodeEnabled)
	}
	return truncateDiffLine(bar, width, unicodeEnabled) + reset
}

func statusBarWithStatuslineHidden(provider, model, root string, width int, unicodeEnabled, color, remote bool, hidden []string, modelColor, workspaceColor string, contextLabel ...string) string {
	provider = sanitizeDiffLine(provider, "<ESC>")
	model = sanitizeDiffLine(model, "<ESC>")
	root = sanitizeDiffLine(root, "<ESC>")
	return renderStatusBarWithPriority(provider, model, root, width, unicodeEnabled, color, remote, statusHiddenSet(hidden), modelColor, workspaceColor, contextLabel)
}

// statuslineSelectionText summarizes hidden segments for confirmations.
func statuslineSelectionText(hidden []string) string {
	if len(hidden) == 0 {
		return "none"
	}
	clean := make([]string, 0, len(hidden))
	for _, segment := range hidden {
		clean = append(clean, sanitizeDiffLine(segment, "<ESC>"))
	}
	return strings.Join(clean, ", ")
}

func (u *UI) handleStatuslineCommand(fields []string) {
	if len(fields) == 1 {
		u.chooseStatusline()
		return
	}
	rest := strings.Join(fields[1:], " ")
	lower := strings.ToLower(strings.TrimSpace(rest))
	switch lower {
	case "show", "list", "status":
		u.printStatuslineSummary()
		return
	case "reset", "all", "default", "clear", "none":
		u.applyStatuslineHidden(nil)
		return
	}
	// Forms: `/statusline <name> on|off`, `/statusline hide a,b`,
	// `/statusline show a,b`, `/statusline tok off`, `/statusline reset`.
	args := strings.Fields(rest)
	if len(args) >= 1 && (strings.EqualFold(args[0], "hide") || strings.EqualFold(args[0], "show")) {
		verb := strings.ToLower(args[0])
		names := strings.Split(strings.Join(args[1:], " "), ",")
		u.applyStatuslineVerb(verb, names)
		return
	}
	if len(args) == 2 && (strings.EqualFold(args[1], "on") || strings.EqualFold(args[1], "off")) {
		if !isValidStatuslineID(args[0]) {
			u.printSystemMessage(yellow + "Unknown statusline segment: " + sanitizeDiffLine(args[0], "<ESC>") + ". Use /statusline to list segments." + reset)
			return
		}
		current := append([]string(nil), u.StatuslineHidden()...)
		set := statusHiddenSet(current)
		id := strings.ToLower(strings.TrimSpace(args[0]))
		if strings.EqualFold(args[1], "off") {
			set[id] = true
		} else {
			delete(set, id)
		}
		next := make([]string, 0, len(set))
		for id := range set {
			next = append(next, id)
		}
		u.applyStatuslineHidden(next)
		return
	}
	if len(args) == 1 && isValidStatuslineID(args[0]) {
		// Toggle a single segment for scripts: `/statusline tok`.
		current := statusHiddenSet(u.StatuslineHidden())
		id := strings.ToLower(strings.TrimSpace(args[0]))
		next := []string{}
		for existing := range current {
			if existing != id {
				next = append(next, existing)
			}
		}
		if !current[id] {
			next = append(next, id)
		}
		u.applyStatuslineHidden(next)
		return
	}
	u.printSystemMessage(yellow + "Usage: /statusline [<name> on|off] [hide|show a,b] [show|reset]" + reset)
}

func (u *UI) applyStatuslineVerb(verb string, names []string) {
	cleaned := []string{}
	for _, raw := range names {
		name := strings.ToLower(strings.TrimSpace(raw))
		if name == "" {
			continue
		}
		if !isValidStatuslineID(name) {
			u.printSystemMessage(yellow + "Unknown statusline segment: " + sanitizeDiffLine(raw, "<ESC>") + ". Use /statusline to list segments." + reset)
			return
		}
		cleaned = append(cleaned, name)
	}
	if len(cleaned) == 0 {
		u.printSystemMessage(yellow + "Usage: /statusline " + verb + " <name[,name...]>" + reset)
		return
	}
	current := statusHiddenSet(u.StatuslineHidden())
	if verb == "hide" {
		for _, id := range cleaned {
			current[id] = true
		}
	} else {
		for _, id := range cleaned {
			delete(current, id)
		}
	}
	next := make([]string, 0, len(current))
	for id := range current {
		next = append(next, id)
	}
	u.applyStatuslineHidden(next)
}

func (u *UI) printStatuslineSummary() {
	hidden := u.StatuslineHidden()
	visible := []string{}
	hiddenSet := statusHiddenSet(hidden)
	for _, option := range statuslineOptions() {
		if !hiddenSet[option.id] {
			visible = append(visible, option.id)
		}
	}
	color := u.out != nil && ColorEnabled(u.out)
	hiddenText := statuslineSelectionText(hidden)
	visibleText := statuslineSelectionText(visible)
	if !color {
		u.printSystemMessage(fmt.Sprintf("Statusline hidden: %s; visible: %s", hiddenText, visibleText))
		return
	}
	u.printSystemMessage(fmt.Sprintf("%sStatusline hidden:%s %s%s%s; %svisible:%s %s%s%s", bold+cyan, reset, dim, hiddenText, reset, bold+cyan, reset, green, visibleText, reset))
}

func (u *UI) applyStatuslineHidden(hidden []string) {
	normalized, err := normalizeStatuslineHidden(hidden)
	if err != nil {
		u.printSystemMessage(yellow + sanitizeDiffLine(err.Error(), "<ESC>") + reset)
		return
	}
	u.screenMu.Lock()
	u.statuslineHidden = append([]string(nil), normalized...)
	u.screenMu.Unlock()
	u.persistStatuslinePreference(normalized)
	u.drawStatusBar()
	if len(normalized) == 0 {
		u.printSystemMessage(green + "Statusline: showing all segments." + reset)
		return
	}
	u.printSystemMessage(fmt.Sprintf("%sStatusline hidden: %s%s", green, statuslineSelectionText(normalized), reset))
}

func (u *UI) persistStatuslinePreference(hidden []string) {
	writer, main := u.runtimePreferenceTarget()
	if writer == nil || !main {
		return
	}
	if err := writer.PersistStatuslineHidden(hidden); err != nil {
		u.printSystemMessage(yellow + "Warning: unable to persist statusline preference: " + sanitizeDiffLine(err.Error(), "<ESC>") + reset)
	}
}

func (u *UI) chooseStatusline() {
	options := statuslineOptions()
	hidden := statusHiddenSet(u.StatuslineHidden())
	initial := make([]bool, len(options))
	for i, option := range options {
		initial[i] = !hidden[option.id]
	}
	if u.input == nil || u.terminal == nil {
		u.printSystemMessage(dim + "Statusline selection is unavailable." + reset)
		return
	}
	u.printSystemMessage(dim + "Type to filter. Use Up/Down or PgUp/PgDn to move, Space to toggle, Enter to apply, Esc to leave, or Ctrl+C to cancel." + reset)
	u.input.setRaw(true)
	u.beginRawSelector()
	defer func() {
		u.input.setRaw(false)
		u.endRawSelector()
	}()
	visible := min(12, max(3, u.height-6))
	enabled, accepted, err := selectStatusline(u.input, u.terminal, options, initial, visible, u.width, ColorEnabled(u.out))
	if err != nil {
		return
	}
	if !accepted {
		u.printSystemMessage(dim + "Statusline selection cancelled." + reset)
		return
	}
	hiddenNext := []string{}
	for i, option := range options {
		if i < len(enabled) && !enabled[i] {
			hiddenNext = append(hiddenNext, option.id)
		}
	}
	u.applyStatuslineHidden(hiddenNext)
}

type statuslineStatus struct {
	id      string
	label   string
	enabled bool
}

func selectStatusline(in io.Reader, out io.Writer, options []statuslineOption, initial []bool, visible, width int, color bool) ([]bool, bool, error) {
	if len(options) == 0 {
		return nil, false, nil
	}
	visible = selectorVisible(len(options), visible)
	statuses := make([]statuslineStatus, len(options))
	for i, option := range options {
		enabled := i < len(initial) && initial[i]
		statuses[i] = statuslineStatus{id: option.id, label: option.id + " — " + option.description, enabled: enabled}
	}
	current := 0
	start := 0
	query := ""
	matches := matchingStatuslineIndices(statuses, query)
	rows := visible + 1
	renderStatuslineSelector(out, statuses, matches, current, start, visible, width, query, color)
	for {
		key, err := readSelectorKey(in)
		if err != nil {
			clearSelector(out, rows)
			return nil, false, err
		}
		switch key {
		case string([]byte{ctrlC}), "\x1b":
			clearSelector(out, rows)
			return nil, false, nil
		case "\r", "\n":
			clearSelector(out, rows)
			enabled := make([]bool, len(statuses))
			for i, status := range statuses {
				enabled[i] = status.enabled
			}
			return enabled, true, nil
		case " ":
			if len(matches) == 0 {
				continue
			}
			index := matches[current]
			statuses[index].enabled = !statuses[index].enabled
			replaceSelectorRow(out, rows, 1+current-start, renderStatuslineLine(statuses[index], true, width, color))
			continue
		case arrowUpSequence, arrowDownSequence, selectorPageUp, selectorPageDown:
			if len(matches) == 0 {
				continue
			}
			oldCurrent, oldStart := current, start
			switch key {
			case arrowUpSequence:
				current = (current - 1 + len(matches)) % len(matches)
				start = selectorStart(current, len(matches), visible, start)
			case arrowDownSequence:
				current = (current + 1) % len(matches)
				start = selectorStart(current, len(matches), visible, start)
			case selectorPageUp:
				current, start = selectorPage(current, start, len(matches), visible, -1)
			case selectorPageDown:
				current, start = selectorPage(current, start, len(matches), visible, 1)
			}
			if start != oldStart {
				clearSelector(out, rows)
				renderStatuslineSelector(out, statuses, matches, current, start, visible, width, query, color)
			} else if current != oldCurrent {
				oldIndex, index := matches[oldCurrent], matches[current]
				replaceSelectorRow(out, rows, 1+oldCurrent-start, renderStatuslineLine(statuses[oldIndex], false, width, color))
				replaceSelectorRow(out, rows, 1+current-start, renderStatuslineLine(statuses[index], true, width, color))
			}
			continue
		case string([]byte{8}), string([]byte{127}):
			if query == "" {
				continue
			}
			query = query[:len(query)-1]
			matches = matchingStatuslineIndices(statuses, query)
			current, start = 0, 0
		case string([]byte{ctrlU}):
			query = ""
			matches = matchingStatuslineIndices(statuses, query)
			current, start = 0, 0
		default:
			if len(key) != 1 || key[0] < 32 || key[0] > 126 {
				continue
			}
			query += key
			matches = matchingStatuslineIndices(statuses, query)
			current, start = 0, 0
		}
		clearSelector(out, rows)
		renderStatuslineSelector(out, statuses, matches, current, start, visible, width, query, color)
	}
}

func matchingStatuslineIndices(statuses []statuslineStatus, query string) []int {
	query = strings.ToLower(query)
	matches := make([]int, 0, len(statuses))
	for i, status := range statuses {
		if strings.Contains(strings.ToLower(status.id+" "+status.label), query) {
			matches = append(matches, i)
		}
	}
	return matches
}

func renderStatuslineSelector(out io.Writer, statuses []statuslineStatus, matches []int, current, start, visible, width int, query string, color bool) {
	header := selectorHeader(fmt.Sprintf("Select statusline segments (%d/%d) | Filter: %s", len(matches), len(statuses), query), width)
	fmt.Fprintln(out, header)
	for row := 0; row < visible; row++ {
		matchIndex := start + row
		if matchIndex >= len(matches) {
			if row == 0 && len(matches) == 0 {
				fmt.Fprintln(out, "  No matching segments")
			} else {
				fmt.Fprintln(out)
			}
			continue
		}
		i := matches[matchIndex]
		fmt.Fprintln(out, renderStatuslineLine(statuses[i], matchIndex == current, width, color))
	}
}

func renderStatuslineLine(status statuslineStatus, current bool, width int, color bool) string {
	box := "[x]"
	if !status.enabled {
		box = "[ ]"
	}
	prefix := "  "
	if current {
		prefix = "> "
	}
	line := fmt.Sprintf("%s%s %s", prefix, box, sanitizeDiffLine(status.label, "<ESC>"))
	if width > 0 {
		line = truncateDiffLine(line, width, false)
	}
	if color && current {
		line = cyan + line + reset
	}
	return line
}
