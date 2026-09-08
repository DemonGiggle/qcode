package tui

import (
	"bytes"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"unicode"
)

const (
	italic             = "\x1b[3m"
	underline          = "\x1b[4m"
	strikethrough      = "\x1b[9m"
	blue               = "\x1b[34m"
	magenta            = "\x1b[35m"
	gray               = "\x1b[90m"
	maxDiffPreviewRows = 10
)

// MarkdownWriter renders complete Markdown lines as ANSI-styled terminal text.
// Buffering by line keeps syntax correct when a provider splits a delimiter
// such as ** or ``` across streamed chunks.
type MarkdownWriter struct {
	out      io.Writer
	enabled  bool
	width    int
	unicode  bool
	active   bool
	inFence  bool
	thinking bool
	diffs    bool
	stateMu  sync.Mutex
	diffMu   sync.Mutex
	diffList []string
	buffer   bytes.Buffer
	table    []markdownTableLine
}

func NewMarkdownWriter(out io.Writer, enabled bool, width ...int) *MarkdownWriter {
	writer := &MarkdownWriter{out: out, enabled: enabled, unicode: true}
	if len(width) > 0 {
		writer.width = width[0]
	}
	return writer
}

func (w *MarkdownWriter) SetUnicode(enabled bool) {
	w.stateMu.Lock()
	w.unicode = enabled
	w.stateMu.Unlock()
}

func (w *MarkdownWriter) SetWidth(width int) {
	w.stateMu.Lock()
	w.width = width
	w.stateMu.Unlock()
}

func (w *MarkdownWriter) EnableDiffs() {
	w.stateMu.Lock()
	w.diffs = true
	w.stateMu.Unlock()
}

func (w *MarkdownWriter) DiffEnabled() bool {
	w.stateMu.Lock()
	defer w.stateMu.Unlock()
	return w.diffs
}

// StreamChunkCompletesLine reports whether writing a provider chunk will
// produce visible output instead of only extending the Markdown line buffer.
func (w *MarkdownWriter) StreamChunkCompletesLine(text string) bool {
	return strings.Contains(text, "\n")
}

func (w *MarkdownWriter) WriteDiff(diff string) {
	w.stateMu.Lock()
	defer w.stateMu.Unlock()
	if !w.diffs || diff == "" {
		return
	}
	w.diffMu.Lock()
	w.diffList = append(w.diffList, diff)
	number := len(w.diffList)
	w.diffMu.Unlock()
	w.writeDiffPreview(diff, number)
}

func (w *MarkdownWriter) ResetDiffs() {
	w.diffMu.Lock()
	w.diffList = nil
	w.diffMu.Unlock()
}

// WriteStoredDiff expands a numbered diff. Number zero selects the latest.
func (w *MarkdownWriter) WriteStoredDiff(number int) (int, int, bool) {
	w.stateMu.Lock()
	defer w.stateMu.Unlock()
	w.diffMu.Lock()
	total := len(w.diffList)
	if number == 0 {
		number = total
	}
	if number < 1 || number > total {
		w.diffMu.Unlock()
		return number, total, false
	}
	diff := w.diffList[number-1]
	w.diffMu.Unlock()

	details := parseDiffDetails(diff)
	w.writeDiffSummary(details, number, true)
	w.writeDiffLines(details.lines, false)
	w.writeDiffFooter("")
	return number, total, true
}

func (w *MarkdownWriter) writeDiffPreview(diff string, number int) {
	details := parseDiffDetails(diff)
	w.writeDiffSummary(details, number, false)
	available := maxDiffPreviewRows - 2
	truncated := len(details.lines) > available
	indices := balancedDiffIndices(details.lines, available)
	preview := make([]string, 0, len(indices))
	for _, index := range indices {
		preview = append(preview, details.lines[index])
	}
	w.writeDiffLines(preview, true)
	if truncated {
		omitted := len(details.lines) - len(preview)
		separator := "·"
		if !w.unicode {
			separator = "-"
		}
		w.writeDiffFooter(fmt.Sprintf("/diff %d to expand %s %d hidden", number, separator, omitted))
	} else {
		w.writeDiffFooter("")
	}
}

type diffDetails struct {
	action    string
	path      string
	additions int
	deletions int
	lines     []string
}

func parseDiffDetails(diff string) diffDetails {
	details := diffDetails{action: "Edited", path: "file"}
	oldPath := ""
	newPath := ""
	lines := strings.Split(strings.TrimSuffix(diff, "\n"), "\n")
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "--- "):
			oldPath = strings.TrimPrefix(line, "--- ")
			continue
		case strings.HasPrefix(line, "+++ "):
			newPath = strings.TrimPrefix(line, "+++ ")
			continue
		case strings.HasPrefix(line, "Binary file changed: "):
			details.path = strings.TrimPrefix(line, "Binary file changed: ")
		}
		if strings.HasPrefix(line, "+") {
			details.additions++
		} else if strings.HasPrefix(line, "-") {
			details.deletions++
		}
		details.lines = append(details.lines, line)
	}
	if newPath != "" && newPath != "/dev/null" {
		details.path = strings.TrimPrefix(newPath, "b/")
	} else if oldPath != "" && oldPath != "/dev/null" {
		details.path = strings.TrimPrefix(oldPath, "a/")
	}
	if oldPath == "/dev/null" {
		details.action = "Added"
	} else if newPath == "/dev/null" {
		details.action = "Deleted"
	}
	return details
}

func balancedDiffIndices(lines []string, limit int) []int {
	if limit >= len(lines) {
		indices := make([]int, len(lines))
		for index := range lines {
			indices[index] = index
		}
		return indices
	}
	selected := make(map[int]bool, limit)
	add := func(index int) {
		if index >= 0 && index < len(lines) && len(selected) < limit {
			selected[index] = true
		}
	}
	metadata := 0
	for index, line := range lines {
		if strings.HasPrefix(line, "@@") {
			add(index)
			metadata++
			if metadata == 2 {
				break
			}
		}
	}
	for index, line := range lines {
		if strings.HasPrefix(line, " ") {
			add(index)
			break
		}
	}
	removals := make([]int, 0, limit)
	additions := make([]int, 0, limit)
	for index, line := range lines {
		if strings.HasPrefix(line, "-") {
			removals = append(removals, index)
		} else if strings.HasPrefix(line, "+") {
			additions = append(additions, index)
		}
	}
	for index := 0; len(selected) < limit && (index < len(removals) || index < len(additions)); index++ {
		if index < len(removals) {
			add(removals[index])
		}
		if index < len(additions) {
			add(additions[index])
		}
	}
	for index := range lines {
		add(index)
	}
	indices := make([]int, 0, len(selected))
	for index := range selected {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	return indices
}

func (w *MarkdownWriter) writeDiffSummary(details diffDetails, number int, expanded bool) {
	marker := "• "
	separator := "·"
	if !w.unicode {
		marker = "* "
		separator = "-"
	}
	escapeLabel := "␛"
	if !w.unicode {
		escapeLabel = "<ESC>"
	}
	details.path = sanitizeDiffLine(details.path, escapeLabel)
	suffix := fmt.Sprintf(" (+%d -%d) %s diff %d", details.additions, details.deletions, separator, number)
	if expanded {
		suffix += " " + separator + " expanded"
	}
	plain := fmt.Sprintf("%s%s %s%s", marker, details.action, details.path, suffix)
	if w.width > 0 && visibleWidth(plain) > w.width {
		available := w.width - visibleWidth(marker)
		if available < 1 {
			marker = ""
			available = w.width
		}
		plain = marker + truncateDiffLine(details.action+" "+details.path, available, w.unicode)
		if w.enabled {
			fmt.Fprintln(w.out, cyan+marker+reset+bold+strings.TrimPrefix(plain, marker)+reset)
			return
		}
		fmt.Fprintln(w.out, plain)
		return
	}
	if !w.enabled {
		fmt.Fprintln(w.out, plain)
		return
	}
	fmt.Fprintf(w.out, "%s%s%s%s%s %s%s (%s+%d%s %s-%d%s) %s%s diff %d%s", cyan, marker, reset, bold, details.action, details.path, reset, green, details.additions, reset, red, details.deletions, reset, dim, separator, number, reset)
	if expanded {
		fmt.Fprintf(w.out, " %s%s expanded%s", dim, separator, reset)
	}
	fmt.Fprintln(w.out)
}

func (w *MarkdownWriter) writeDiffLines(lines []string, truncate bool) {
	for _, line := range lines {
		w.writeStyledDiffLine(line, truncate)
	}
}

func (w *MarkdownWriter) writeStyledDiffLine(line string, truncate bool) {
	escapeLabel := "␛"
	if !w.unicode {
		escapeLabel = "<ESC>"
	}
	line = sanitizeDiffLine(line, escapeLabel)
	marker := "│ "
	if !w.unicode {
		marker = "| "
	}
	if w.width > 0 && w.width < visibleWidth(marker)+1 {
		marker = ""
	}
	contentWidth := 0
	if w.width > 0 {
		contentWidth = w.width - visibleWidth(marker)
		if contentWidth < 1 {
			contentWidth = 1
		}
	}
	if truncate {
		line = truncateDiffLine(line, contentWidth, w.unicode)
	}
	styledMarker := marker
	if w.enabled {
		styledMarker = dim + marker + reset
	}
	style := ""
	if w.enabled {
		switch {
		case strings.HasPrefix(line, "@@"):
			style = cyan
		case strings.HasPrefix(line, "+"):
			style = green
		case strings.HasPrefix(line, "-"):
			style = red
		case strings.HasPrefix(line, "..."):
			style = yellow
		default:
			style = dim
		}
	}
	rendered := line
	if style != "" {
		rendered = style + line + reset
	}
	fmt.Fprintln(w.out, wrapANSI(styledMarker+rendered, w.width, styledMarker))
}

func (w *MarkdownWriter) writeDiffFooter(text string) {
	marker := "╰─"
	if !w.unicode {
		marker = "+-"
	}
	line := marker
	if text != "" {
		line += " " + text
	}
	line = truncateDiffLine(line, w.width, w.unicode)
	if w.enabled {
		fmt.Fprintln(w.out, dim+line+reset)
		return
	}
	fmt.Fprintln(w.out, line)
}

func truncateDiffLine(line string, width int, unicodeEnabled bool) string {
	if width <= 0 || visibleWidth(line) <= width {
		return line
	}
	suffix := "..."
	if unicodeEnabled {
		suffix = "…"
	} else if width < len(suffix) {
		suffix = suffix[:width]
	}
	limit := width - visibleWidth(suffix)
	if limit < 0 {
		limit = 0
	}
	units := displayUnits(line)
	var output strings.Builder
	used := 0
	for _, unit := range units {
		if used+unit.width > limit {
			break
		}
		output.WriteString(unit.raw)
		used += unit.width
	}
	output.WriteString(suffix)
	return output.String()
}

func sanitizeDiffLine(line, escapeLabel string) string {
	var output strings.Builder
	for _, char := range line {
		switch {
		case char == '\x1b':
			output.WriteString(escapeLabel)
		case char == '\t' || char >= 32:
			output.WriteRune(char)
		default:
			fmt.Fprintf(&output, "<0x%02X>", char)
		}
	}
	return output.String()
}

func (w *MarkdownWriter) BeginResponse() {
	w.stateMu.Lock()
	defer w.stateMu.Unlock()
	w.active = true
	w.thinking = false
	w.inFence = false
	w.buffer.Reset()
	w.table = nil
}

func (w *MarkdownWriter) BeginThinking() {
	w.stateMu.Lock()
	w.thinking = true
	w.stateMu.Unlock()
}

func (w *MarkdownWriter) EndThinking() {
	w.stateMu.Lock()
	defer w.stateMu.Unlock()
	if !w.thinking {
		return
	}
	if w.buffer.Len() > 0 {
		w.renderLine(strings.TrimSuffix(w.buffer.String(), "\r"))
		w.buffer.Reset()
		fmt.Fprintln(w.out)
	}
	w.thinking = false
}

func (w *MarkdownWriter) EndResponse() {
	w.stateMu.Lock()
	defer w.stateMu.Unlock()
	if !w.active {
		return
	}
	if w.buffer.Len() > 0 && (w.enabled || w.width > 0) {
		w.consumeLine(strings.TrimSuffix(w.buffer.String(), "\r"), false)
	}
	w.flushTable()
	w.buffer.Reset()
	w.inFence = false
	w.thinking = false
	w.table = nil
	w.active = false
	if w.enabled {
		fmt.Fprint(w.out, reset)
	}
}

func (w *MarkdownWriter) Write(data []byte) (int, error) {
	w.stateMu.Lock()
	defer w.stateMu.Unlock()
	if !w.active || (!w.enabled && w.width <= 0) {
		return w.out.Write(data)
	}
	written := len(data)
	for len(data) > 0 {
		newline := bytes.IndexByte(data, '\n')
		if newline < 0 {
			_, _ = w.buffer.Write(data)
			break
		}
		_, _ = w.buffer.Write(data[:newline])
		w.consumeLine(strings.TrimSuffix(w.buffer.String(), "\r"), true)
		w.buffer.Reset()
		data = data[newline+1:]
	}
	return written, nil
}

func (w *MarkdownWriter) renderCompleteLine(line string, newline bool) {
	w.renderLine(line)
	if newline {
		fmt.Fprint(w.out, "\n")
	}
}

func (w *MarkdownWriter) renderLine(line string) {
	escapeLabel := "␛"
	if !w.unicode {
		escapeLabel = "<ESC>"
	}
	line = strings.ReplaceAll(line, "\x1b", escapeLabel)
	if w.thinking {
		w.writeStatement(line, gray)
		return
	}
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
		if !w.enabled {
			w.inFence = !w.inFence
			fmt.Fprint(w.out, wrapANSI(line, w.width, leadingWhitespace(line)))
			return
		}
		if w.inFence {
			label := "╰─"
			if !w.unicode {
				label = "+-"
			}
			w.writeRendered(magenta+label+reset, "")
			w.inFence = false
			return
		}
		language := strings.TrimSpace(trimmed[3:])
		label := "╭─"
		if !w.unicode {
			label = "+-"
		}
		if language != "" {
			label += " " + language
		}
		w.writeRendered(magenta+label+reset, "")
		w.inFence = true
		return
	}
	if w.inFence {
		if !w.enabled {
			fmt.Fprint(w.out, wrapANSI(line, w.width, leadingWhitespace(line)))
			return
		}
		marker := "│ "
		if !w.unicode {
			marker = "| "
		}
		styledMarker := magenta + marker + reset
		w.writeRendered(styledMarker+yellow+line+reset, styledMarker)
		return
	}
	if !w.enabled {
		if isPlainStatement(line, w.unicode) {
			w.writeStatement(line, "")
		} else {
			fmt.Fprint(w.out, wrapANSI(line, w.width, leadingWhitespace(line)))
		}
		return
	}
	if trimmed == "---" || trimmed == "***" || trimmed == "___" {
		rule := "─"
		if !w.unicode {
			rule = "-"
		}
		w.writeRendered(gray+strings.Repeat(rule, 40)+reset, "")
		return
	}
	leading := line[:len(line)-len(strings.TrimLeftFunc(line, unicode.IsSpace))]
	content := strings.TrimLeftFunc(line, unicode.IsSpace)
	if level, heading := heading(content); level > 0 {
		color := cyan
		if level >= 3 {
			color = blue
		}
		headingStyle := bold + color
		w.writeRendered(leading+headingStyle+renderInline(heading, headingStyle)+reset, leading)
		return
	}
	if strings.HasPrefix(content, "> ") || content == ">" {
		quote := strings.TrimSpace(strings.TrimPrefix(content, ">"))
		continuation := leading + "  "
		marker := "│ "
		if !w.unicode {
			marker = "| "
		}
		w.writeRendered(leading+cyan+marker+reset+italic+renderInline(quote, italic)+reset, continuation)
		return
	}
	if marker, rest, ok := listItem(content, w.unicode); ok {
		continuation := leading + strings.Repeat(" ", visibleWidth(marker))
		w.writeRendered(leading+cyan+marker+reset+renderInline(rest), continuation)
		return
	}
	if content == "" || strings.HasPrefix(line, "    ") || strings.HasPrefix(line, "\t") || strings.HasPrefix(content, "|") {
		w.writeRendered(renderInline(line), leading)
		return
	}
	w.writeStatement(line, "")
}

func (w *MarkdownWriter) writeRendered(rendered, continuation string) {
	fmt.Fprint(w.out, wrapANSI(rendered, w.width, continuation))
}

func (w *MarkdownWriter) writeStatement(line, style string) {
	leading := leadingWhitespace(line)
	content := strings.TrimLeftFunc(line, unicode.IsSpace)
	if content == "" || !isPlainStatement(line, w.unicode) {
		rendered := line
		if w.enabled && style != "" {
			rendered = style + line + reset
		}
		w.writeRendered(rendered, leading)
		return
	}
	marker := "• "
	if !w.unicode {
		marker = "* "
	}
	markerStyle := cyan
	if style != "" {
		markerStyle = style
	}
	renderedContent := renderInline(content)
	if w.enabled && style != "" {
		renderedContent = style + content + reset
	}
	if w.enabled {
		w.writeRendered(leading+markerStyle+marker+reset+renderedContent, leading+"  ")
		return
	}
	w.writeRendered(leading+marker+content, leading+"  ")
}

func isPlainStatement(line string, unicodeEnabled bool) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(line, "    ") || strings.HasPrefix(line, "\t") || strings.HasPrefix(trimmed, "|") {
		return false
	}
	if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") || strings.HasPrefix(trimmed, ">") {
		return false
	}
	if trimmed == "---" || trimmed == "***" || trimmed == "___" {
		return false
	}
	if level, _ := heading(trimmed); level > 0 {
		return false
	}
	_, _, list := listItem(trimmed, unicodeEnabled)
	return !list
}

func leadingWhitespace(line string) string {
	return line[:len(line)-len(strings.TrimLeftFunc(line, unicode.IsSpace))]
}

func heading(line string) (int, string) {
	level := 0
	for level < len(line) && level < 6 && line[level] == '#' {
		level++
	}
	if level == 0 || level >= len(line) || line[level] != ' ' {
		return 0, line
	}
	return level, strings.TrimSpace(line[level:])
}

func listItem(line string, unicodeEnabled bool) (string, string, bool) {
	if len(line) >= 2 && (line[0] == '-' || line[0] == '*' || line[0] == '+') && line[1] == ' ' {
		rest := line[2:]
		if strings.HasPrefix(strings.ToLower(rest), "[x] ") {
			if !unicodeEnabled {
				return "[x] ", rest[4:], true
			}
			return "☑ ", rest[4:], true
		}
		if strings.HasPrefix(rest, "[ ] ") {
			if !unicodeEnabled {
				return "[ ] ", rest[4:], true
			}
			return "☐ ", rest[4:], true
		}
		if !unicodeEnabled {
			return "- ", rest, true
		}
		return "• ", rest, true
	}
	dot := strings.Index(line, ". ")
	if dot > 0 {
		for _, char := range line[:dot] {
			if !unicode.IsDigit(char) {
				return "", line, false
			}
		}
		return line[:dot+2], line[dot+2:], true
	}
	return "", line, false
}

func renderInline(line string, baseStyle ...string) string {
	var out strings.Builder
	restore := ""
	if len(baseStyle) > 0 {
		restore = baseStyle[0]
	}
	for len(line) > 0 {
		switch {
		case strings.HasPrefix(line, "!["):
			if altEnd := strings.Index(line[2:], "]("); altEnd >= 0 {
				altEnd += 2
				if urlEnd := strings.IndexByte(line[altEnd+2:], ')'); urlEnd >= 0 {
					urlEnd += altEnd + 2
					out.WriteString(magenta + "[image: " + line[2:altEnd] + "]" + reset + restore)
					out.WriteString(gray + " (" + line[altEnd+2:urlEnd] + ")" + reset + restore)
					line = line[urlEnd+1:]
					continue
				}
			}
		case line[0] == '[':
			if textEnd := strings.Index(line, "]("); textEnd > 0 {
				if urlEnd := strings.IndexByte(line[textEnd+2:], ')'); urlEnd >= 0 {
					urlEnd += textEnd + 2
					out.WriteString(underline + cyan + line[1:textEnd] + reset + restore)
					out.WriteString(gray + " (" + line[textEnd+2:urlEnd] + ")" + reset + restore)
					line = line[urlEnd+1:]
					continue
				}
			}
		case line[0] == '`':
			if end := strings.IndexByte(line[1:], '`'); end >= 0 {
				end++
				out.WriteString(cyan + line[1:end] + reset + restore)
				line = line[end+1:]
				continue
			}
		case strings.HasPrefix(line, "**") || strings.HasPrefix(line, "__"):
			marker := line[:2]
			if end := strings.Index(line[2:], marker); end >= 0 {
				end += 2
				out.WriteString(bold + line[2:end] + reset + restore)
				line = line[end+2:]
				continue
			}
		case strings.HasPrefix(line, "~~"):
			if end := strings.Index(line[2:], "~~"); end >= 0 {
				end += 2
				out.WriteString(strikethrough + line[2:end] + reset + restore)
				line = line[end+2:]
				continue
			}
		case line[0] == '*' || line[0] == '_':
			marker := line[0]
			if end := strings.IndexByte(line[1:], marker); end >= 0 {
				end++
				out.WriteString(italic + line[1:end] + reset + restore)
				line = line[end+1:]
				continue
			}
		}
		out.WriteByte(line[0])
		line = line[1:]
	}
	return out.String()
}
