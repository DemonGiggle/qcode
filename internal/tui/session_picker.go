package tui

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"qcode/internal/redaction"
	"qcode/internal/session"
)

const maxSessionListRows = 8

type sessionPickerOptions struct {
	visible, width, height int
	color, unicode         bool
	policy                 *redaction.Policy
	size                   func() (int, int)
	refresh                func() ([]session.Entry, error)
	rename                 func(string, string) error
	setPinned              func(string, bool) error
	accept                 func(string) error
}

type sessionPickerMode int

const (
	sessionPickerList sessionPickerMode = iota
	sessionPickerActions
	sessionPickerRename
)

type sessionPicker struct {
	entries        []session.Entry
	matches        []int
	selected       int
	query, message string
	mode           sessionPickerMode
	action         int
	edit           []rune
	cursor         int
}

// sessionPickerWriter paints absolute rows under the same lock as the footer.
// No newline is emitted at the bottom of the scroll region.
type sessionPickerWriter struct{ ui *UI }

func (w sessionPickerWriter) firstOutputRow() int { return 2 }
func (w sessionPickerWriter) Write(data []byte) (int, error) {
	w.ui.screenMu.Lock()
	defer w.ui.screenMu.Unlock()
	return w.ui.out.Write(data)
}
func (w sessionPickerWriter) paintRow(line string) string {
	w.ui.screenMu.Lock()
	defer w.ui.screenMu.Unlock()
	return (themeWriter{palette: w.ui.outputTheme(), color: ColorEnabled(w.ui.out), width: w.ui.width}).paintRow(line)
}

func (u *UI) selectSession(entries []session.Entry) (string, bool, error) {
	return u.pickSession(entries, nil)
}

func (u *UI) pickSession(entries []session.Entry, accept func(string) error) (string, bool, error) {
	size := func() (int, int) {
		u.screenMu.Lock()
		defer u.screenMu.Unlock()
		return u.width, u.commandViewHeightLocked()
	}
	width, height := size()
	if width < 1 || height < 2 {
		return "", false, fmt.Errorf("enlarge the terminal to select a session")
	}
	options := sessionPickerOptions{visible: maxSessionListRows, width: width, height: height,
		color: ColorEnabled(u.out), unicode: u.unicode, policy: u.redaction, size: size, accept: accept}
	if p := u.persistence; p != nil {
		options.rename = p.store.Rename
		options.setPinned = p.store.SetPinned
		options.refresh = func() ([]session.Entry, error) {
			p.mu.Lock()
			defer p.mu.Unlock()
			return p.listSessionsLocked()
		}
	}
	return runSessionPicker(u.input, sessionPickerWriter{u}, entries, options)
}

func runSessionPicker(in io.Reader, out io.Writer, entries []session.Entry, options sessionPickerOptions) (string, bool, error) {
	if len(entries) == 0 {
		return "", false, nil
	}
	entries = terminalSessionEntries(entries, options.policy)
	p := sessionPicker{entries: entries, matches: matchingSessionIndices(entries, "")}
	height, width := options.height, options.width
	defer func() {
		if options.size != nil {
			width, height = options.size()
		}
		paintSessionRows(out, make([]string, max(1, height)), width, options.unicode)
	}()
	for {
		if options.size != nil {
			width, height = options.size()
		}
		height = max(1, height)
		lines, visible := p.render(options, width, height)
		paintSessionRows(out, lines, width, options.unicode)
		key, err := readSelectorKey(in)
		if err != nil {
			return "", false, err
		}
		if key == string([]byte{ctrlC}) {
			return "", false, nil
		}
		if key == "\x1b" {
			if p.mode == sessionPickerList {
				return "", false, nil
			}
			p.mode, p.message = sessionPickerList, ""
			continue
		}
		switch p.mode {
		case sessionPickerRename:
			if key == "\r" || key == "\n" {
				entry := p.entries[p.matches[p.selected]]
				if options.rename == nil {
					p.message = "Session editing is unavailable"
				} else if err := options.rename(entry.ID, strings.TrimSpace(string(p.edit))); err != nil {
					p.message = "Cannot rename: " + err.Error()
				} else {
					p.mode = sessionPickerList
					p.refresh(options, entry.ID)
				}
			} else {
				p.editKey(key)
			}
		case sessionPickerActions:
			switch key {
			case arrowUpSequence, arrowDownSequence:
				p.action = 1 - p.action
			case "\r", "\n":
				entry := p.entries[p.matches[p.selected]]
				p.message = ""
				if p.action == 0 {
					p.mode = sessionPickerRename
					p.edit = []rune(entry.Name)
					p.cursor = len(p.edit)
				} else if options.setPinned == nil {
					p.message = "Session editing is unavailable"
				} else if err := options.setPinned(entry.ID, !entry.Pinned); err != nil {
					p.message = "Cannot change pin: " + err.Error()
				} else {
					p.mode = sessionPickerList
					p.refresh(options, entry.ID)
				}
			}
		case sessionPickerList:
			switch key {
			case "\t":
				if len(p.matches) > 0 {
					p.mode, p.action, p.message = sessionPickerActions, 0, ""
				}
			case "\r", "\n":
				if len(p.matches) > 0 {
					entry := p.entries[p.matches[p.selected]]
					if options.accept != nil {
						if err := options.accept(entry.ID); err != nil {
							p.message = "Cannot resume: " + err.Error()
							continue
						}
					}
					return entry.ID, true, nil
				}
			case arrowUpSequence, arrowDownSequence, selectorPageUp, selectorPageDown:
				if len(p.matches) > 0 {
					switch key {
					case arrowUpSequence:
						p.selected = (p.selected + len(p.matches) - 1) % len(p.matches)
					case arrowDownSequence:
						p.selected = (p.selected + 1) % len(p.matches)
					case selectorPageUp:
						p.selected = max(0, p.selected-visible)
					case selectorPageDown:
						p.selected = min(len(p.matches)-1, p.selected+visible)
					}
					p.message = ""
				}
			default:
				query := p.query
				if key == "\b" || key == "\x7f" {
					_, size := utf8.DecodeLastRuneInString(query)
					query = query[:len(query)-size]
				} else if key == string([]byte{ctrlU}) {
					query = ""
				} else if printableSessionKey(key) {
					query += key
				}
				if query != p.query {
					p.query, p.selected, p.message = query, 0, ""
					p.matches = matchingSessionIndices(p.entries, query)
				}
			}
		}
	}
}

func (p *sessionPicker) refresh(options sessionPickerOptions, selectedID string) {
	p.message = ""
	if options.refresh == nil {
		p.message = "Cannot refresh sessions"
		return
	}
	entries, err := options.refresh()
	if err != nil {
		p.message = "Cannot refresh sessions: " + err.Error()
		return
	}
	entries = terminalSessionEntries(entries, options.policy)
	p.entries, p.selected = entries, 0
	p.matches = matchingSessionIndices(entries, p.query)
	for i, index := range p.matches {
		if entries[index].ID == selectedID {
			p.selected = i
			break
		}
	}
}

func terminalSessionEntries(entries []session.Entry, policy *redaction.Policy) []session.Entry {
	entries = append([]session.Entry(nil), entries...)
	for i := range entries {
		e := &entries[i]
		e.Preview = policy.Text(redaction.Terminal, stripSessionEscapes(e.Preview))
		e.Problem = policy.Text(redaction.Terminal, singleSessionLine(e.Problem))
		e.MetadataProblem = policy.Text(redaction.Terminal, singleSessionLine(e.MetadataProblem))
	}
	return entries
}

func printableSessionKey(key string) bool {
	r, size := utf8.DecodeRuneInString(key)
	return key != "" && size == len(key) && utf8.ValidString(key) && unicode.IsPrint(r)
}

func (p *sessionPicker) editKey(key string) {
	switch key {
	case "\x1b[D":
		p.cursor = max(0, p.cursor-1)
	case "\x1b[C":
		p.cursor = min(len(p.edit), p.cursor+1)
	case "\x01", "\x1b[H", "\x1b[1~", "\x1b[7~":
		p.cursor = 0
	case "\x05", "\x1b[F", "\x1b[4~", "\x1b[8~":
		p.cursor = len(p.edit)
	case "\b", "\x7f":
		if p.cursor > 0 {
			p.edit = append(p.edit[:p.cursor-1], p.edit[p.cursor:]...)
			p.cursor--
		}
	case "\x1b[3~":
		if p.cursor < len(p.edit) {
			p.edit = append(p.edit[:p.cursor], p.edit[p.cursor+1:]...)
		}
	case "\x15":
		p.edit, p.cursor = nil, 0
	case "\x0b":
		p.edit = p.edit[:p.cursor]
	default:
		if printableSessionKey(key) {
			r, _ := utf8.DecodeRuneInString(key)
			p.edit = append(p.edit, 0)
			copy(p.edit[p.cursor+1:], p.edit[p.cursor:])
			p.edit[p.cursor] = r
			p.cursor++
		}
	}
}

func (p *sessionPicker) render(options sessionPickerOptions, width, height int) ([]string, int) {
	lines := make([]string, height)
	legend := interfaceGlyph(options.unicode, "● current | ◆ pinned", "* current | ^ pinned")
	lines[0] = fmt.Sprintf("Resume (%d/%d) | %s | Filter: %s", len(p.matches), len(p.entries), legend, options.policy.Text(redaction.Terminal, p.query))
	footer := "Up/Down PgUp/PgDn | Enter resume | Tab actions | " + selectorLeaveHint
	message := p.message
	var entry session.Entry
	if len(p.matches) > 0 {
		entry = p.entries[p.matches[p.selected]]
		entry.Name = options.policy.Text(redaction.Terminal, stripSessionEscapes(entry.Name))
		if message == "" {
			message = entry.MetadataProblem
			if entry.Problem != "" {
				message = entry.Problem
			}
		}
	}
	if height == 1 {
		lines[0] = footer
		if message != "" {
			lines[0] = "Error: " + singleSessionLine(options.policy.Text(redaction.Terminal, message))
		}
		return lines, 1
	}
	footerRows, errorRows := 0, 0
	if height >= 3 {
		footerRows = 1
	}
	if message != "" && height >= 3 {
		errorRows = 1
		if height == 3 {
			footerRows = 0
		}
		lines[height-1-footerRows] = "Error: " + singleSessionLine(options.policy.Text(redaction.Terminal, message))
	}
	available := height - 1 - footerRows - errorRows
	visible := min(max(1, options.visible), maxSessionListRows, max(1, len(p.matches)), available)
	if p.mode == sessionPickerList {
		start := selectorStart(p.selected, len(p.matches), visible, 0)
		for row := 0; row < visible; row++ {
			index := start + row
			if index < len(p.matches) {
				listed := p.entries[p.matches[index]]
				listed.Name = options.policy.Text(redaction.Terminal, stripSessionEscapes(listed.Name))
				lines[1+row] = renderSessionLineWithUnicode(listed, index == p.selected, width, options.color, options.unicode)
			} else if row == 0 {
				lines[1] = "  No matching sessions"
			}
		}
		// Reserve the heading and its gap, then a bottom rule and a blank row
		// separating the preview from errors and keyboard hints.
		previewRows := max(0, available-visible-4)
		if previewRows > 0 && len(p.matches) > 0 {
			lines[2+visible] = sessionPreviewRule("Preview", width, options.color, options.unicode)
			preview := sessionPreview(entry.Snapshot, options.policy, width, options.unicode, options.color)
			preview = preview[max(0, len(preview)-previewRows):]
			previewEnd := 3 + visible + previewRows
			copy(lines[3+visible:previewEnd], preview)
			lines[previewEnd] = sessionPreviewRule("", width, options.color, options.unicode)
		}
	} else if p.mode == sessionPickerActions {
		lines[0] = "Session actions | " + sessionName(entry)
		pin := "Pin"
		if entry.Pinned {
			pin = "Unpin"
		}
		for i, action := range []string{"Rename", pin} {
			row := 1 + i
			if available == 1 {
				row = 1
				if i != p.action {
					continue
				}
			}
			marker := "  "
			if i == p.action {
				marker = "> "
			}
			lines[row] = marker + action
		}
		footer = "Up/Down | Enter apply | " + selectorLeaveHint
	} else {
		lines[0] = "Rename session | Empty name restores automatic label"
		// Keep the insertion point visible even when editing a long UTF-8 name.
		safe := []rune(strings.ReplaceAll(options.policy.Text(redaction.Terminal, stripSessionEscapes(string(p.edit))), "\n", " "))
		prefix := strings.ReplaceAll(options.policy.Text(redaction.Terminal, stripSessionEscapes(string(p.edit[:p.cursor]))), "\n", " ")
		cursor := min(len(safe), utf8.RuneCountInString(prefix))
		before, after := string(safe[:cursor]), string(safe[cursor:])
		for visibleWidth(before) > max(0, width-8) && before != "" {
			_, size := utf8.DecodeRuneInString(before)
			before = before[size:]
		}
		lines[1] = "Name: " + before + "|" + after
		footer = "Left/Right Home/End | Enter save | " + selectorLeaveHint
	}
	if footerRows > 0 {
		lines[height-1] = footer
	}
	if message != "" && height == 2 {
		lines[0] = "Error: " + singleSessionLine(options.policy.Text(redaction.Terminal, message))
	}
	return lines, max(1, visible)
}

func sessionPreviewRule(label string, width int, color, unicodeEnabled bool) string {
	rule := interfaceGlyph(unicodeEnabled, "─", "-")
	line := ""
	if label != "" {
		line = rule + rule + " " + label + " "
	}
	line += strings.Repeat(rule, max(0, width-visibleWidth(line)))
	line = truncateDiffLine(line, width, unicodeEnabled)
	if color {
		return dim + line + reset
	}
	return line
}

func paintSessionRows(out io.Writer, lines []string, width int, unicodeEnabled bool) {
	var output strings.Builder
	for row, line := range lines {
		line = truncateDiffLine(line, width, unicodeEnabled)
		fmt.Fprintf(&output, "\x1b[%d;1H\x1b[2K%s\x1b[0m", row+selectorFirstOutputRow(out), paintSelectorRow(out, line))
	}
	_, _ = io.WriteString(out, output.String())
}

func matchingSessionIndices(entries []session.Entry, query string) []int {
	query = strings.ToLower(query)
	matches := make([]int, 0, len(entries))
	for i, entry := range entries {
		text := sessionEntryLabel(entry) + " " + singleSessionLine(entry.Preview)
		if strings.Contains(strings.ToLower(text), query) {
			matches = append(matches, i)
		}
	}
	return matches
}

func renderSessionLine(entry session.Entry, selected bool, width int, color bool) string {
	return renderSessionLineWithUnicode(entry, selected, width, color, UnicodeEnabled())
}

func renderSessionLineWithUnicode(entry session.Entry, selected bool, width int, color, unicodeEnabled bool) string {
	marker, current, pin := "  ", "  ", "  "
	if selected {
		marker = "> "
	}
	if entry.Current {
		current = interfaceGlyph(unicodeEnabled, "● ", "* ")
	}
	if entry.Pinned {
		pin = interfaceGlyph(unicodeEnabled, "◆ ", "^ ")
	}
	line := truncateDiffLine(marker+current+pin+sessionEntryLabel(entry), width, unicodeEnabled)
	if color {
		if selected {
			return cyan + bold + line + reset
		}
		if entry.Problem != "" || entry.MetadataProblem != "" {
			return yellow + line + reset
		}
	}
	return line
}

func sessionEntryLabel(entry session.Entry) string {
	when := session.ResumeTime(entry.Snapshot).In(time.Local).Format("Jan 02 15:04")
	agents := "1 agent"
	if len(entry.Agents) != 1 {
		agents = fmt.Sprintf("%d agents", len(entry.Agents))
	}
	return when + " | " + agents + " | " + sessionName(entry)
}

func sessionName(entry session.Entry) string {
	label := singleSessionLine(entry.Name)
	if label == "" {
		label = singleSessionLine(entry.Preview)
	}
	if label == "" {
		label = "Untitled session"
	}
	if entry.Problem != "" {
		label = "Unavailable: " + singleSessionLine(entry.Problem)
	}
	return label
}

func singleSessionLine(text string) string {
	return strings.Join(strings.Fields(stripSessionEscapes(text)), " ")
}

// sessionPreview filters the entire transcript before the caller selects its
// tail to fit the available viewport. Styles cannot hide secrets from matching.
func sessionPreview(snap session.Snapshot, policy *redaction.Policy, width int, unicodeEnabled, color bool) []string {
	var presentation savedPresentation
	var text string
	if json.Unmarshal(snap.Presentation, &presentation) == nil {
		var active, main *savedView
		for i := range presentation.Views {
			v := &presentation.Views[i]
			if v.ID == presentation.Active {
				active = v
			}
			if v.ID == "main" {
				main = v
			}
		}
		if active == nil {
			active = main
		}
		if active != nil {
			h := active.History
			source := h.Archive
			if len(source) == 0 {
				source = h.Lines
			}
			var current []historyCell
			for _, cell := range h.Current {
				current = append(current, historyCell{char: cell.Char, style: cell.Style})
			}
			text = strings.Join(source, "\n") + "\n" + renderHistoryCells(current)
		}
	}
	if strings.TrimSpace(stripSessionEscapes(text)) == "" {
		text = snap.Preview
	}
	// Reconstruct complete rows using only safe styling, including colors that
	// span newlines. Redaction sees plain text so styles cannot split a secret.
	history := newHistoryWriter(io.Discard)
	_, _ = history.Write([]byte(sanitizeSessionText(text, color)))
	styled := append(history.archive, renderHistoryCells(history.current))
	plain := make([]string, len(styled))
	for i, line := range styled {
		plain[i] = stripSessionEscapes(line)
	}
	filtered := strings.Split(policy.Text(redaction.Terminal, strings.Join(plain, "\n")), "\n")
	var lines []string
	for i, line := range filtered {
		if strings.TrimSpace(line) != "" {
			if color {
				line = colorSessionPreviewLine(styled[i], plain[i], line)
			}
			lines = append(lines, truncateDiffLine(line, width, unicodeEnabled))
		}
	}
	return lines
}

// Keep unchanged text's styles around a redacted span, and give replacement
// text the span's original style. Only filtered characters reach the output.
func colorSessionPreviewLine(styled, original, filtered string) string {
	if original == filtered {
		return styled
	}
	history := newHistoryWriter(io.Discard)
	_, _ = history.Write([]byte(styled))
	source := history.current
	safe := []rune(filtered)
	prefix, suffix := 0, 0
	for prefix < min(len(source), len(safe)) && source[prefix].char == safe[prefix] {
		prefix++
	}
	for suffix < min(len(source), len(safe))-prefix && source[len(source)-1-suffix].char == safe[len(safe)-1-suffix] {
		suffix++
	}
	style := ""
	if prefix < len(source) {
		style = source[prefix].style
	}
	cells := append([]historyCell(nil), source[:prefix]...)
	for _, char := range safe[prefix : len(safe)-suffix] {
		cells = append(cells, historyCell{char: char, style: style})
	}
	cells = append(cells, source[len(source)-suffix:]...)
	return renderHistoryCells(cells)
}

// Strip CSI, OSC, DCS and other terminal controls, including C1 forms. Preserve
// newlines and indentation; no saved content can reposition the picker.
func stripSessionEscapes(text string) string {
	return sanitizeSessionText(text, false)
}

// When color is enabled, allow numeric SGR styling only. All other terminal
// controls are stripped before reconstructing history or applying redaction.
func sanitizeSessionText(text string, color bool) string {
	runes := []rune(text)
	var output strings.Builder
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if r == '\x1b' {
			i++
			if i >= len(runes) {
				break
			}
			r = runes[i]
			if r == '[' {
				r = '\u009b'
			} else if strings.ContainsRune("]P^_X", r) {
				r = '\u009d'
			} else {
				for r >= 0x20 && r <= 0x2f && i+1 < len(runes) {
					i++
					r = runes[i]
				}
				continue
			}
		}
		if r == '\u009b' {
			start := i + 1
			for i++; i < len(runes); i++ {
				if runes[i] >= 0x40 && runes[i] <= 0x7e {
					break
				}
			}
			if color && i < len(runes) && runes[i] == 'm' {
				parameters := string(runes[start:i])
				if strings.Trim(parameters, "0123456789;:") == "" {
					output.WriteString("\x1b[" + parameters + "m")
				}
			}
			continue
		}
		if r == '\u009d' || r == '\u0090' || r == '\u009e' || r == '\u009f' || r == '\u0098' {
			for i++; i < len(runes); i++ {
				if runes[i] == '\a' || runes[i] == '\u009c' {
					break
				}
				if runes[i] == '\x1b' && i+1 < len(runes) && runes[i+1] == '\\' {
					i++
					break
				}
			}
			continue
		}
		if r == '\t' {
			output.WriteString("    ")
		} else if r == '\n' || r >= 32 && (r < 127 || r > 159) {
			output.WriteRune(r)
		}
	}
	return output.String()
}
