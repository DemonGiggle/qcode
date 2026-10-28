package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"qcode/internal/llm"
	"qcode/internal/redaction"
)

// compactContextReader is implemented by agents that can expose their current
// conversation messages for the /compact show debug view.
type compactContextReader interface {
	ContextSnapshot() []llm.Message
}

func (u *UI) compactedMessages() ([]llm.Message, bool) {
	if u.manager != nil {
		u.screenMu.Lock()
		id := u.activeAgent
		mgr := u.manager
		u.screenMu.Unlock()
		if mgr == nil {
			return nil, false
		}
		value, ok := mgr.Runner(id)
		if !ok {
			return nil, false
		}
		reader, ok := value.(compactContextReader)
		if !ok || reader == nil {
			return nil, false
		}
		return reader.ContextSnapshot(), true
	}
	reader, ok := u.runner.(compactContextReader)
	if !ok || reader == nil {
		return nil, false
	}
	return reader.ContextSnapshot(), true
}

// showCompactedContext opens the post-compaction context (system + summary +
// recent tail) in a history-detail-style pager. Key handling matches the
// history detail view so compaction quality can be inspected with familiar
// navigation.
func (u *UI) showCompactedContext(ctx context.Context) {
	msgs, ok := u.compactedMessages()
	if !ok {
		u.printSystemMessage(yellow + "Compacted context is unavailable." + reset)
		return
	}
	if len(msgs) == 0 {
		u.printSystemMessage(dim + "No compacted context to show." + reset)
		return
	}
	if u.input == nil || u.out == nil || !u.fixedInput {
		u.printSystemMessage(yellow + "Compacted context view requires an interactive terminal." + reset)
		return
	}

	redact := func(s string) string {
		return u.redaction.Text(redaction.Terminal, s)
	}
	u.screenMu.Lock()
	width := u.width
	color := ColorEnabled(u.out)
	unicodeEnabled := u.unicode
	u.screenMu.Unlock()
	lines := compactContextLines(msgs, max(1, width), color, redact)

	u.input.setRaw(true)
	u.beginRawSelector()
	defer func() {
		u.input.setRaw(false)
		u.endRawSelector()
	}()

	u.screenMu.Lock()
	width, start, visible := u.width, u.latestPromptStartLocked(), u.commandViewHeightLocked()
	u.screenMu.Unlock()
	err := showCompactDetail(u.input, historyViewWriter{ui: u, start: start}, lines, width, max(2, visible), color, unicodeEnabled)
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(ctx.Err(), context.Canceled) {
		u.printSystemMessage(yellow + "Unable to show compacted context: " + sanitizeDiffLine(err.Error(), "<ESC>") + reset)
	}
}

func compactContextLines(msgs []llm.Message, width int, color bool, redact func(string) string) []string {
	sectionHeading := func(text, accent string) string {
		if color {
			return bold + accent + text + reset
		}
		return text
	}
	summaryCount := 0
	for _, msg := range msgs {
		if msg.Origin == "conversation_summary" {
			summaryCount++
		}
	}
	recentCount := max(0, len(msgs)-1-summaryCount)
	lines := []string{
		sectionHeading("Compacted context", yellow),
		fmt.Sprintf("%d messages: 1 system + %d summary + %d recent", len(msgs), summaryCount, recentCount),
		"",
	}
	for i, msg := range msgs {
		heading := fmt.Sprintf("[%d] %s", i, msg.Role)
		accent := cyan
		if msg.Role == "system" {
			accent = yellow
		} else if msg.Origin == "conversation_summary" {
			accent = green
		}
		if msg.Name != "" {
			heading += " name=" + msg.Name
		}
		if msg.ToolCallID != "" {
			heading += " tool_call_id=" + msg.ToolCallID
		}
		if msg.Origin != "" {
			heading += " origin=" + msg.Origin
		}
		if msg.Untrusted {
			heading += " (untrusted reference data)"
		}
		lines = append(lines, sectionHeading(heading, accent))
		if len(msg.ToolCalls) > 0 {
			for _, call := range msg.ToolCalls {
				args := strings.TrimSpace(string(call.Arguments))
				if args == "" {
					args = "{}"
				}
				callLine := fmt.Sprintf("tool_call %s %s %s", call.ID, call.Name, redact(args))
				callLine = sanitizeDiffLine(callLine, "<ESC>")
				lines = append(lines, strings.Split(wrapANSI(callLine, width, ""), "\n")...)
			}
		}
		content := msg.Content
		if content == "" && msg.Thinking != "" {
			content = "(empty content; thinking shown below)"
		}
		if content != "" {
			content = redact(content)
			content = strings.ReplaceAll(strings.ReplaceAll(content, "\r\n", "\n"), "\r", "\n")
			content = sanitizeDiffLine(content, "<ESC>")
			for _, line := range strings.Split(content, "\n") {
				lines = append(lines, strings.Split(wrapANSI(line, width, ""), "\n")...)
			}
		}
		if msg.Thinking != "" {
			thinking := sanitizeDiffLine(redact(msg.Thinking), "<ESC>")
			thinking = strings.ReplaceAll(strings.ReplaceAll(thinking, "\r\n", "\n"), "\r", "\n")
			lines = append(lines, dim+"thinking:" +reset)
			for _, line := range strings.Split(thinking, "\n") {
				lines = append(lines, strings.Split(wrapANSI(line, width, ""), "\n")...)
			}
		}
		if len(msg.Images) > 0 {
			lines = append(lines, fmt.Sprintf("[%d image(s) retained in live context]", len(msg.Images)))
		}
		if len(msg.ReasoningDetails) > 0 {
			lines = append(lines, fmt.Sprintf("[reasoning details preserved: %d bytes]", len(msg.ReasoningDetails)))
		}
		lines = append(lines, "")
	}
	return lines
}

func showCompactDetail(in io.Reader, out io.Writer, lines []string, width, height int, color, unicodeEnabled bool) error {
	pager := planPager{lines: lines}
	for {
		renderCompactDetail(out, pager, width, height, color, unicodeEnabled)
		key, err := readSelectorKey(in)
		if err != nil {
			return err
		}
		visible := historyDetailVisible(height)
		switch key {
		case string([]byte{ctrlC}), "q", "Q", "\x1b", string([]byte{8}), string([]byte{127}):
			return nil
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

func renderCompactDetail(out io.Writer, pager planPager, width, height int, color, unicodeEnabled bool) {
	header := "Compacted context | Up/Down PgUp/PgDn Home/End | Esc/q: close"
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
