package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"qcode/internal/session"
)

type workHistoryReader interface {
	WorkRecords() []session.WorkRecord
}

type historyBrowser struct {
	records  []session.WorkRecord
	matches  []int
	query    string
	pending  []byte
	selected int
	start    int
}

func newHistoryBrowser(records []session.WorkRecord) *historyBrowser {
	b := &historyBrowser{records: records}
	b.filter()
	return b
}

func completedAgentHistory(records []session.WorkRecord, agentID string) []session.WorkRecord {
	completed := make([]session.WorkRecord, 0, len(records))
	for i := len(records) - 1; i >= 0; i-- {
		record := records[i]
		if record.AgentID == agentID && record.Status == "completed" && !record.Consultation {
			completed = append(completed, record)
		}
	}
	return completed
}

func (b *historyBrowser) filter() {
	b.matches = b.matches[:0]
	query := strings.ToLower(b.query)
	for i, record := range b.records {
		if query == "" || strings.Contains(strings.ToLower(record.Prompt), query) {
			b.matches = append(b.matches, i)
		}
	}
	b.selected = 0
	b.start = 0
}

func (b *historyBrowser) current() (session.WorkRecord, bool) {
	if len(b.matches) == 0 || b.selected < 0 || b.selected >= len(b.matches) {
		return session.WorkRecord{}, false
	}
	return b.records[b.matches[b.selected]], true
}

func (b *historyBrowser) move(delta, visible int) {
	if len(b.matches) == 0 {
		return
	}
	b.selected = (b.selected + delta + len(b.matches)) % len(b.matches)
	b.start = selectorStart(b.selected, len(b.matches), max(1, visible), b.start)
}

func (b *historyBrowser) page(direction, visible int) {
	if len(b.matches) == 0 {
		return
	}
	b.selected, b.start = selectorPage(b.selected, b.start, len(b.matches), max(1, visible), direction)
}

type historyDetailResult int

const (
	historyDetailBack historyDetailResult = iota
	historyDetailClose
)

func showHistoryBrowser(in io.Reader, out io.Writer, records []session.WorkRecord, agentName string, width, height int, color, unicodeEnabled bool) error {
	if len(records) == 0 {
		return nil
	}
	width, height = max(1, width), max(2, height)
	browser := newHistoryBrowser(records)
	for {
		renderHistoryList(out, browser, agentName, width, height, color, unicodeEnabled)
		key, err := readSelectorKey(in)
		if err != nil {
			return err
		}
		visible := max(1, height-1)
		switch key {
		case string([]byte{ctrlC}), "\x1b":
			return nil
		case "\r", "\n":
			record, ok := browser.current()
			if !ok {
				continue
			}
			result, err := showHistoryDetail(in, out, record, browser.selected+1, len(browser.matches), width, height, color, unicodeEnabled)
			if err != nil {
				return err
			}
			if result == historyDetailClose {
				return nil
			}
		case arrowUpSequence:
			browser.move(-1, visible)
		case arrowDownSequence:
			browser.move(1, visible)
		case selectorPageUp:
			browser.page(-1, visible)
		case selectorPageDown:
			browser.page(1, visible)
		case string([]byte{8}), string([]byte{127}):
			if len(browser.pending) > 0 {
				browser.pending = nil
			} else if browser.query != "" {
				_, size := utf8.DecodeLastRuneInString(browser.query)
				browser.query = browser.query[:len(browser.query)-size]
				browser.filter()
			}
		case string([]byte{ctrlU}):
			browser.query = ""
			browser.pending = nil
			browser.filter()
		default:
			browser.addSearchKey(key)
		}
	}
}

func (b *historyBrowser) addSearchKey(key string) {
	if len(key) != 1 || key[0] < 32 || key[0] == 127 {
		return
	}
	if key[0] < utf8.RuneSelf {
		b.query += key
		b.filter()
		return
	}
	b.pending = append(b.pending, key[0])
	if !utf8.FullRune(b.pending) {
		return
	}
	r, size := utf8.DecodeRune(b.pending)
	if r != utf8.RuneError || size > 1 {
		b.query += string(b.pending[:size])
		b.filter()
	}
	b.pending = nil
}

func renderHistoryList(out io.Writer, browser *historyBrowser, agentName string, width, height int, color, unicodeEnabled bool) {
	header := fmt.Sprintf("History: %s | %d/%d | Search: %s | Up/Down PgUp/PgDn Enter Esc",
		sanitizeDiffLine(agentName, "<ESC>"), len(browser.matches), len(browser.records), sanitizeDiffLine(browser.query, "<ESC>"))
	lines := make([]string, height)
	lines[0] = header
	visible := max(1, height-1)
	if len(browser.matches) == 0 {
		lines[1] = "  No matching prompts"
	} else {
		if browser.start > max(0, len(browser.matches)-visible) {
			browser.start = max(0, len(browser.matches)-visible)
		}
		for row := 0; row < visible && browser.start+row < len(browser.matches); row++ {
			match := browser.start + row
			record := browser.records[browser.matches[match]]
			marker := "  "
			if match == browser.selected {
				marker = "> "
			}
			stamp := record.Finished.Local().Format("2006-01-02 15:04")
			line := marker + stamp + "  " + historyPromptPreview(record.Prompt)
			if color && match == browser.selected {
				line = cyan + bold + line + reset
			}
			lines[row+1] = line
		}
	}
	renderHistoryRows(out, lines, width, unicodeEnabled)
}

func historyPromptPreview(prompt string) string {
	prompt = sanitizeDiffLine(prompt, "<ESC>")
	prompt = strings.Join(strings.Fields(prompt), " ")
	if prompt == "" {
		return "(empty prompt)"
	}
	return prompt
}

func showHistoryDetail(in io.Reader, out io.Writer, record session.WorkRecord, position, total, width, height int, color, unicodeEnabled bool) (historyDetailResult, error) {
	pager := planPager{lines: historyDetailLines(record, width, color, unicodeEnabled)}
	for {
		renderHistoryDetail(out, pager, position, total, width, height, color, unicodeEnabled)
		key, err := readSelectorKey(in)
		if err != nil {
			return historyDetailBack, err
		}
		visible := historyDetailVisible(height)
		switch key {
		case string([]byte{ctrlC}), "q", "Q":
			return historyDetailClose, nil
		case "\x1b", string([]byte{8}), string([]byte{127}):
			return historyDetailBack, nil
		case selectorPageUp:
			pager.page(-1, visible)
		case selectorPageDown:
			pager.page(1, visible)
		case arrowUpSequence:
			pager.move(-1, visible)
		case arrowDownSequence:
			pager.move(1, visible)
		case "\x1b[H", "g":
			pager.home()
		case "\x1b[F", "G":
			pager.end(visible)
		}
	}
}

func historyDetailVisible(height int) int {
	// One row separates the modal from the tab bar and one row contains the
	// navigation header. Everything else belongs to the scrollable item.
	return max(1, height-2)
}

func historyDetailLines(record session.WorkRecord, width int, color, unicodeEnabled bool) []string {
	sectionHeading := func(text, accent string) string {
		if color {
			return bold + accent + text + reset
		}
		return text
	}
	lines := []string{sectionHeading("Prompt", yellow)}
	prompt := strings.ReplaceAll(strings.ReplaceAll(record.Prompt, "\r\n", "\n"), "\r", "\n")
	prompt = sanitizeDiffLine(prompt, "<ESC>")
	for _, line := range strings.Split(prompt, "\n") {
		lines = append(lines, strings.Split(wrapANSI(line, width, ""), "\n")...)
	}
	lines = append(lines, "", sectionHeading("Response", green))
	if record.Response == "" {
		message := "No response text."
		if color {
			message = dim + message + reset
		}
		return append(lines, message)
	}
	var rendered bytes.Buffer
	writer := NewMarkdownWriter(&rendered, color, width)
	writer.SetUnicode(unicodeEnabled)
	writer.BeginResponse()
	_, _ = writer.Write([]byte(record.Response))
	writer.EndResponse()
	response := strings.TrimSuffix(rendered.String(), reset)
	return append(lines, strings.Split(response, "\n")...)
}

func renderHistoryDetail(out io.Writer, pager planPager, position, total, width, height int, color, unicodeEnabled bool) {
	header := fmt.Sprintf("History item %d/%d | Up/Down PgUp/PgDn Home/End | Esc: list | q: close", position, total)
	visible := historyDetailVisible(height)
	if pager.maxTop(visible) > 0 {
		header += fmt.Sprintf(" | Lines %d-%d/%d", pager.top+1, min(len(pager.lines), pager.top+visible), len(pager.lines))
	}
	if color {
		header = dim + header + reset
	}
	lines := make([]string, height)
	// Row zero intentionally stays blank to separate this modal from the tabs.
	if height > 1 {
		lines[1] = header
	}
	for row := 2; row < height; row++ {
		index := pager.top + row - 2
		if index < len(pager.lines) {
			lines[row] = pager.lines[index]
		}
	}
	renderHistoryRows(out, lines, width, unicodeEnabled)
}

func renderHistoryRows(out io.Writer, lines []string, width int, unicodeEnabled bool) {
	var output strings.Builder
	for row, line := range lines {
		fmt.Fprintf(&output, "\x1b[%d;1H\x1b[2K%s\x1b[0m", row+2, truncateDiffLine(line, width, unicodeEnabled))
	}
	_, _ = io.WriteString(out, output.String())
}

type historyViewWriter struct{ ui *UI }

func (w historyViewWriter) Write(data []byte) (int, error) {
	w.ui.screenMu.Lock()
	defer w.ui.screenMu.Unlock()
	return w.ui.out.Write(data)
}

func (u *UI) showHistory(ctx context.Context) {
	reader, ok := u.manager.(workHistoryReader)
	if u.manager == nil || !ok {
		u.printSystemMessage(yellow + "History is unavailable." + reset)
		return
	}
	if u.input == nil || u.out == nil || !u.fixedInput {
		u.printSystemMessage(yellow + "History requires an interactive terminal." + reset)
		return
	}

	u.screenMu.Lock()
	agentID := u.activeAgent
	width, height := u.width, u.height
	u.screenMu.Unlock()
	records := completedAgentHistory(reader.WorkRecords(), agentID)
	if len(records) == 0 {
		u.printSystemMessage(dim + "No completed prompts for the active agent." + reset)
		return
	}
	agentName := agentID
	if summary, err := u.manager.Summary(agentID); err == nil && summary.Name != "" {
		agentName = summary.Name
	}

	u.input.setRaw(true)
	u.beginRawSelector()
	defer func() {
		u.input.setRaw(false)
		u.endRawSelector()
	}()

	err := showHistoryBrowser(u.input, historyViewWriter{ui: u}, records, agentName, width, max(2, height-4), ColorEnabled(u.out), u.unicode)
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(ctx.Err(), context.Canceled) {
		u.printSystemMessage(yellow + "Unable to show history: " + sanitizeDiffLine(err.Error(), "<ESC>") + reset)
	}
}
