package tui

import (
	"bytes"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"qcode/internal/redaction"
)

// outputGroup shares the display's screen lock with history and terminal writes.
// Leading whitespace and styling wait for visible content, so an empty stream
// cannot leave a separator behind. Later whitespace remains verbatim.
type outputGroup struct {
	display *agentDisplay
	pending []byte
	visible bool
}

func (d *agentDisplay) NewOutputGroup() io.Writer {
	return &outputGroup{display: d}
}

func (g *outputGroup) Write(data []byte) (int, error) {
	g.display.ui.screenMu.Lock()
	defer g.display.ui.screenMu.Unlock()
	return g.writeLocked(data)
}

func (g *outputGroup) writeLocked(data []byte) (int, error) {
	n := len(data)
	transient := bytes.ContainsRune(data, '\r') && !bytes.ContainsRune(data, '\n')
	data = g.prepareLocked(data)
	if len(data) == 0 {
		return n, nil
	}
	if transient {
		g.display.transient = visibleGroupText(data)
	}
	_, err := g.display.writeFilteredLocked(data)
	return n, err
}

func (g *outputGroup) prepareLocked(data []byte) []byte {
	d := g.display
	data = []byte(d.ui.redaction.Text(redaction.Terminal, string(data)))
	if !g.visible {
		g.pending = append(g.pending, data...)
		if !visibleGroupText(g.pending) {
			return nil
		}
		data = g.pending
		g.pending = nil
		g.visible = true
	}
	if d.transient && visibleGroupText(data) {
		// Spinner rows stay transient until completion. A notice may arrive
		// while one is active; erase it before committing
		// any rows so it cannot become archived transcript content.
		_, _ = d.writeFilteredLocked([]byte("\r\x1b[2K"))
		d.transient = false
	}
	if d.group != g && visibleGroupText(data) {
		// Derive the boundary from retained history, including restored or
		// cleared tabs. Incoming blank rows can supply the separator too.
		padding := d.history.groupPadding()
		plain := plainHistoryText(string(data))
		leading := plain[:len(plain)-len(strings.TrimLeftFunc(plain, unicode.IsSpace))]
		blankRows := strings.Count(leading, "\n")
		if blankRows > padding {
			data = skipGroupBlankRows(data, blankRows-padding)
		}
		padding = max(0, padding-blankRows)
		data = append([]byte(strings.Repeat("\n", padding)), data...)
		d.group = g
	}
	return data
}

// Remove redundant opening blank rows without losing the ANSI styles that
// precede content or its indentation after the final opening newline.
func skipGroupBlankRows(data []byte, rows int) []byte {
	var styles []byte
	for rows > 0 && len(data) > 0 {
		if data[0] == '\x1b' {
			n, complete := ansiSequenceLength(data)
			if !complete {
				break
			}
			styles = append(styles, data[:n]...)
			data = data[n:]
			continue
		}
		r, size := utf8.DecodeRune(data)
		if r == '\n' {
			rows--
		}
		data = data[size:]
	}
	return append(styles, data...)
}

func visibleGroupText(data []byte) bool {
	for len(data) > 0 {
		if data[0] == '\x1b' {
			n, complete := ansiSequenceLength(data)
			if !complete {
				return false
			}
			data = data[n:]
			continue
		}
		if !utf8.FullRune(data) {
			return false
		}
		r, size := utf8.DecodeRune(data)
		if r >= ' ' && !unicode.IsSpace(r) {
			return true
		}
		data = data[size:]
	}
	return false
}

// groupPadding returns the newlines needed before the next visible group.
// It uses only the repaint window: /clear starts a fresh visible transcript.
func (w *historyWriter) groupPadding() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	if strings.TrimSpace(plainHistoryText(renderHistoryCells(w.current))) != "" {
		return 2
	}
	if len(w.lines) > 0 && strings.TrimSpace(plainHistoryText(w.lines[len(w.lines)-1])) != "" {
		return 1
	}
	return 0
}

// writeNoticeLocked emits one standalone notice while screenMu is held.
func (d *agentDisplay) writeNoticeLocked(message string) {
	g := &outputGroup{display: d}
	_, _ = g.writeLocked([]byte(message + "\n"))
}
