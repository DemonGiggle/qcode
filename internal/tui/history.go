package tui

import (
	"io"
	"sync"
	"unicode/utf8"
)

const maxHistoryLines = 5000

// historyWriter records a plain-text copy of persistent terminal output while
// forwarding the original, styled output unchanged.
type historyWriter struct {
	out io.Writer

	mu      sync.Mutex
	lines   []string
	current []rune
	cursor  int
	pending []byte
}

func newHistoryWriter(out io.Writer) *historyWriter {
	return &historyWriter{out: out}
}

func (w *historyWriter) Write(data []byte) (int, error) {
	n, err := w.out.Write(data)
	if n > 0 {
		w.mu.Lock()
		w.record(data[:n])
		w.mu.Unlock()
	}
	return n, err
}

func (w *historyWriter) AddLine(line string) {
	w.mu.Lock()
	w.record([]byte(line + "\n"))
	w.mu.Unlock()
}

func (w *historyWriter) Clear() {
	w.mu.Lock()
	w.lines = nil
	w.current = nil
	w.cursor = 0
	w.pending = nil
	w.mu.Unlock()
}

func (w *historyWriter) Lines() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.lines...)
}

func (w *historyWriter) record(data []byte) {
	data = append(w.pending, data...)
	w.pending = nil
	for len(data) > 0 {
		if data[0] == '\x1b' {
			length, complete := ansiSequenceLength(data)
			if !complete {
				w.pending = append(w.pending, data...)
				return
			}
			if length > 0 && data[length-1] == 'K' && string(data[2:length-1]) == "2" {
				w.current = nil
				w.cursor = 0
			}
			data = data[length:]
			continue
		}
		switch data[0] {
		case '\r':
			w.cursor = 0
			data = data[1:]
			continue
		case '\n':
			w.commit(w.current)
			w.current = nil
			w.cursor = 0
			data = data[1:]
			continue
		}
		if !utf8.FullRune(data) {
			w.pending = append(w.pending, data...)
			return
		}
		r, size := utf8.DecodeRune(data)
		data = data[size:]
		if r < ' ' && r != '\t' {
			continue
		}
		if w.cursor < len(w.current) {
			w.current[w.cursor] = r
		} else {
			w.current = append(w.current, r)
		}
		w.cursor++
	}
}

func ansiSequenceLength(data []byte) (int, bool) {
	if len(data) < 2 {
		return 0, false
	}
	if data[1] != '[' {
		return 2, true
	}
	for index := 2; index < len(data); index++ {
		if data[index] >= 0x40 && data[index] <= 0x7e {
			return index + 1, true
		}
	}
	return 0, false
}

func (w *historyWriter) commit(line []rune) {
	w.lines = append(w.lines, string(line))
	if len(w.lines) > maxHistoryLines {
		drop := maxHistoryLines / 5
		w.lines = append([]string(nil), w.lines[drop:]...)
	}
}

func historyPage(lines []string, pageSize, offset, direction int) ([]string, int) {
	if pageSize < 1 {
		pageSize = 1
	}
	maxOffset := len(lines) - pageSize
	if maxOffset < 0 {
		maxOffset = 0
	}
	if direction > 0 {
		offset += pageSize
	} else if direction < 0 {
		offset -= pageSize
	}
	if offset < 0 {
		offset = 0
	}
	if offset > maxOffset {
		offset = maxOffset
	}
	end := len(lines) - offset
	start := end - pageSize
	if start < 0 {
		start = 0
	}
	return append([]string(nil), lines[start:end]...), offset
}
