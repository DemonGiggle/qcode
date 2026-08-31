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
	out     io.Writer
	enabled bool
	active  bool
	inFence bool
	buffer  bytes.Buffer
}

func NewMarkdownWriter(out io.Writer, enabled bool) *MarkdownWriter {
	return &MarkdownWriter{out: out, enabled: enabled}
}

func (w *MarkdownWriter) BeginResponse() {
	w.active = true
	w.inFence = false
	w.buffer.Reset()
}

func (w *MarkdownWriter) EndResponse() {
	if !w.active {
		return
	}
	if w.enabled && w.buffer.Len() > 0 {
		w.renderLine(strings.TrimSuffix(w.buffer.String(), "\r"))
	}
	w.buffer.Reset()
	w.inFence = false
	w.active = false
	if w.enabled {
		fmt.Fprint(w.out, reset)
	}
}

func (w *MarkdownWriter) Write(data []byte) (int, error) {
	if !w.active || !w.enabled {
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
	line = strings.ReplaceAll(line, "\x1b", "␛")
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
		if w.inFence {
			fmt.Fprint(w.out, magenta+"╰─"+reset)
			w.inFence = false
			return
		}
		language := strings.TrimSpace(trimmed[3:])
		label := "╭─"
		if language != "" {
			label += " " + language
		}
		fmt.Fprint(w.out, magenta+label+reset)
		w.inFence = true
		return
	}
	if w.inFence {
		fmt.Fprint(w.out, yellow+line+reset)
		return
	}
	if trimmed == "---" || trimmed == "***" || trimmed == "___" {
		fmt.Fprint(w.out, gray+strings.Repeat("─", 40)+reset)
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
		fmt.Fprint(w.out, leading+headingStyle+renderInline(heading, headingStyle)+reset)
		return
	}
	if strings.HasPrefix(content, "> ") || content == ">" {
		quote := strings.TrimSpace(strings.TrimPrefix(content, ">"))
		fmt.Fprint(w.out, leading+cyan+"│ "+reset+italic+renderInline(quote, italic)+reset)
		return
	}
	if marker, rest, ok := listItem(content); ok {
		fmt.Fprint(w.out, leading+cyan+marker+reset+renderInline(rest))
		return
	}
	fmt.Fprint(w.out, renderInline(line))
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

func listItem(line string) (string, string, bool) {
	if len(line) >= 2 && (line[0] == '-' || line[0] == '*' || line[0] == '+') && line[1] == ' ' {
		rest := line[2:]
		if strings.HasPrefix(strings.ToLower(rest), "[x] ") {
			return "☑ ", rest[4:], true
		}
		if strings.HasPrefix(rest, "[ ] ") {
			return "☐ ", rest[4:], true
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
