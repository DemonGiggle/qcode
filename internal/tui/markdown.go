package tui

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"unicode"
)

const (
	italic        = "\x1b[3m"
	underline     = "\x1b[4m"
	strikethrough = "\x1b[9m"
	blue          = "\x1b[34m"
	magenta       = "\x1b[35m"
	gray          = "\x1b[90m"
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
	buffer   bytes.Buffer
}

func NewMarkdownWriter(out io.Writer, enabled bool, width ...int) *MarkdownWriter {
	writer := &MarkdownWriter{out: out, enabled: enabled, unicode: true}
	if len(width) > 0 {
		writer.width = width[0]
	}
	return writer
}

func (w *MarkdownWriter) SetUnicode(enabled bool) { w.unicode = enabled }

func (w *MarkdownWriter) EnableDiffs() { w.diffs = true }

func (w *MarkdownWriter) DiffEnabled() bool { return w.diffs }

func (w *MarkdownWriter) WriteDiff(diff string) {
	if !w.diffs || diff == "" {
		return
	}
	escapeLabel := "␛"
	if !w.unicode {
		escapeLabel = "<ESC>"
	}
	for _, line := range strings.Split(diff, "\n") {
		line = sanitizeDiffLine(line, escapeLabel)
		style := ""
		if w.enabled {
			switch {
			case strings.HasPrefix(line, "--- "), strings.HasPrefix(line, "+++ "):
				style = bold + cyan
			case strings.HasPrefix(line, "@@"):
				style = magenta
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
		fmt.Fprintln(w.out, wrapANSI(rendered, w.width, "  "))
	}
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
	w.active = true
	w.inFence = false
	w.buffer.Reset()
}

func (w *MarkdownWriter) BeginThinking() { w.thinking = true }

func (w *MarkdownWriter) EndThinking() {
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
	if !w.active {
		return
	}
	if w.buffer.Len() > 0 && (w.enabled || w.width > 0) {
		w.renderLine(strings.TrimSuffix(w.buffer.String(), "\r"))
	}
	w.buffer.Reset()
	w.inFence = false
	w.thinking = false
	w.active = false
	if w.enabled {
		fmt.Fprint(w.out, reset)
	}
}

func (w *MarkdownWriter) Write(data []byte) (int, error) {
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
		w.renderLine(strings.TrimSuffix(w.buffer.String(), "\r"))
		fmt.Fprint(w.out, "\n")
		w.buffer.Reset()
		data = data[newline+1:]
	}
	return written, nil
}

func (w *MarkdownWriter) renderLine(line string) {
	escapeLabel := "␛"
	if !w.unicode {
		escapeLabel = "<ESC>"
	}
	line = strings.ReplaceAll(line, "\x1b", escapeLabel)
	if w.thinking {
		rendered := line
		if w.enabled {
			rendered = gray + line + reset
		}
		fmt.Fprint(w.out, wrapANSI(rendered, w.width, leadingWhitespace(line)))
		return
	}
	if !w.enabled {
		fmt.Fprint(w.out, wrapANSI(line, w.width, leadingWhitespace(line)))
		return
	}
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
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
		w.writeRendered(yellow+line+reset, leadingWhitespace(line))
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
	w.writeRendered(renderInline(line), leading)
}

func (w *MarkdownWriter) writeRendered(rendered, continuation string) {
	fmt.Fprint(w.out, wrapANSI(rendered, w.width, continuation))
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
