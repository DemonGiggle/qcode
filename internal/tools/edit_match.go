package tools

import (
	"bytes"
	"fmt"
	"strings"
)

// editMatch is one accepted old_text match in the raw file bytes.
type editMatch struct {
	start int
	end   int
}

// editTolerance records which copy-paste differences a match had to tolerate.
// The zero value means old_text matched raw bytes exactly.
type editTolerance uint8

const (
	editTolerateGutter editTolerance = 1 << iota
	editTolerateLineEndings
)

// note describes a tolerated difference for the tool output.
func (t editTolerance) note() string {
	switch t {
	case 0:
		return ""
	case editTolerateGutter:
		return "matched after removing the line-number gutter that read adds"
	case editTolerateLineEndings:
		return "matched ignoring CRLF/LF line-ending differences"
	default:
		return "matched after removing the line-number gutter that read adds and ignoring CRLF/LF line-ending differences"
	}
}

// locateEditMatches finds every span of data that old_text should replace.
// Exact matches always win. Only when there are none, two copy-paste traps are
// tolerated, because each makes byte-exact matching impossible for a model that
// has only seen read output:
//
//   - read prefixes every line with a line-number gutter (see read), so a
//     snippet copied verbatim from a read result never matches raw bytes;
//   - read strips CRLF terminators (bufio.ScanLines), so a model cannot see the
//     "\r" that a CRLF file contains and cannot reproduce it.
//
// The traps are tried separately and then together, because a snippet copied
// from a read result of a CRLF file carries both at once.
func locateEditMatches(data []byte, oldText string) ([]editMatch, editTolerance) {
	if spans := allIndexes(data, []byte(oldText)); len(spans) > 0 {
		return spans, 0
	}
	stripped, hasGutter := stripReadGutter(oldText)
	if hasGutter {
		if spans := allIndexes(data, []byte(stripped)); len(spans) > 0 {
			return spans, editTolerateGutter
		}
	}
	if spans, ok := crlfMatches(data, oldText); ok {
		return spans, editTolerateLineEndings
	}
	if hasGutter {
		if spans, ok := crlfMatches(data, stripped); ok {
			return spans, editTolerateGutter | editTolerateLineEndings
		}
	}
	return nil, 0
}

// allIndexes returns non-overlapping occurrences of sub in data. Overlaps are
// not reported so the occurrence count matches bytes.Count.
func allIndexes(data, sub []byte) []editMatch {
	if len(sub) == 0 {
		return nil
	}
	var spans []editMatch
	for offset := 0; offset+len(sub) <= len(data); {
		index := bytes.Index(data[offset:], sub)
		if index < 0 {
			break
		}
		start := offset + index
		spans = append(spans, editMatch{start: start, end: start + len(sub)})
		offset = start + len(sub)
	}
	return spans
}

// stripReadGutter removes the "     1\t" prefix that read adds to every line.
// It reports false unless at least one line actually carried a gutter, so
// ordinary text is never rewritten by accident.
func stripReadGutter(text string) (string, bool) {
	if text == "" {
		return "", false
	}
	lines := splitLinesKeepEOL(text)
	changed := false
	for index, line := range lines {
		body, eol := splitLineEOL(line)
		stripped, ok := stripGutterPrefix(body)
		if !ok {
			return "", false
		}
		if stripped != body {
			changed = true
		}
		lines[index] = stripped + eol
	}
	if !changed {
		return "", false
	}
	return strings.Join(lines, ""), true
}

// stripGutterPrefix matches read's "%6d\t%s" line format. Blank lines are
// accepted unchanged so a snippet that drops a gutter-only line still works.
func stripGutterPrefix(line string) (string, bool) {
	if line == "" {
		return "", true
	}
	trimmed := strings.TrimLeft(line, " ")
	if len(trimmed) == len(line) {
		return "", false
	}
	digits := 0
	for digits < len(trimmed) && trimmed[digits] >= '0' && trimmed[digits] <= '9' {
		digits++
	}
	if digits == 0 || digits > 9 || digits >= len(trimmed) || trimmed[digits] != '\t' {
		return "", false
	}
	return trimmed[digits+1:], true
}

// stripGutters removes read's line-number gutter from every line that carries
// one and leaves other lines untouched. It is only used for new_text after the
// same gutter was tolerated in old_text, where the model is clearly editing a
// copied read result.
func stripGutters(text string) string {
	lines := splitLinesKeepEOL(text)
	changed := false
	for index, line := range lines {
		body, eol := splitLineEOL(line)
		stripped, ok := stripGutterPrefix(body)
		if !ok || stripped == body {
			continue
		}
		lines[index] = stripped + eol
		changed = true
	}
	if !changed {
		return text
	}
	return strings.Join(lines, "")
}

// crlfMatches retries matching with CRLF collapsed to LF, mapping the accepted
// spans back to raw byte offsets so untouched lines keep their original
// terminators.
func crlfMatches(data []byte, oldText string) ([]editMatch, bool) {
	if !bytes.Contains(data, []byte("\r\n")) || strings.Contains(oldText, "\r") {
		return nil, false
	}
	needle := []byte(oldText)
	if len(needle) == 0 {
		return nil, false
	}
	normalized, offsets := normalizeCRLF(data)
	spans := allIndexes(normalized, needle)
	if len(spans) == 0 {
		return nil, false
	}
	converted := make([]editMatch, 0, len(spans))
	for _, span := range spans {
		converted = append(converted, editMatch{start: offsets[span.start], end: offsets[span.end]})
	}
	return converted, true
}

// normalizeCRLF returns data with every "\r\n" replaced by "\n" plus a map
// from normalized byte offsets to raw byte offsets (len(result)+1 entries).
func normalizeCRLF(data []byte) ([]byte, []int) {
	normalized := make([]byte, 0, len(data))
	offsets := make([]int, 0, len(data)+1)
	for index := 0; index < len(data); index++ {
		offsets = append(offsets, index)
		if data[index] == '\r' && index+1 < len(data) && data[index+1] == '\n' {
			normalized = append(normalized, '\n')
			index++
			continue
		}
		normalized = append(normalized, data[index])
	}
	offsets = append(offsets, len(data))
	return normalized, offsets
}

// spliceMatches replaces each match with its own replacement bytes, so a
// replace_all over a mixed-EOL file can align each span with its own line
// terminator.
func spliceMatches(data []byte, matches []editMatch, replacements [][]byte) []byte {
	var out bytes.Buffer
	out.Grow(len(data))
	last := 0
	for index, match := range matches {
		out.Write(data[last:match.start])
		if index < len(replacements) {
			out.Write(replacements[index])
		}
		last = match.end
	}
	out.Write(data[last:])
	return out.Bytes()
}

// lineEndingAt reports the line terminator in effect at raw offset pos: the
// next terminator if there is one, otherwise the nearest preceding terminator.
func lineEndingAt(data []byte, pos int) string {
	if pos < 0 {
		pos = 0
	}
	if pos > len(data) {
		pos = len(data)
	}
	if index := bytes.IndexByte(data[pos:], '\n'); index >= 0 {
		if pos+index > 0 && data[pos+index-1] == '\r' {
			return "\r\n"
		}
		return "\n"
	}
	for index := pos - 1; index >= 0; index-- {
		if data[index] == '\n' {
			if index > 0 && data[index-1] == '\r' {
				return "\r\n"
			}
			return "\n"
		}
	}
	return "\n"
}

// withLineEnding rewrites text's LF terminators to eol. CRLF input is
// normalized first so no "\r\r\n" can be produced.
func withLineEnding(text, eol string) []byte {
	if eol != "\r\n" {
		return []byte(text)
	}
	normalized := strings.ReplaceAll(text, "\r\n", "\n")
	return []byte(strings.ReplaceAll(normalized, "\n", eol))
}

// splitLinesKeepEOL splits text after each "\n", keeping terminators.
func splitLinesKeepEOL(text string) []string {
	var lines []string
	for {
		index := strings.IndexByte(text, '\n')
		if index < 0 {
			if text != "" {
				lines = append(lines, text)
			}
			return lines
		}
		lines = append(lines, text[:index+1])
		text = text[index+1:]
	}
}

func splitLineEOL(line string) (string, string) {
	if strings.HasSuffix(line, "\r\n") {
		return line[:len(line)-2], "\r\n"
	}
	if strings.HasSuffix(line, "\n") {
		return line[:len(line)-1], "\n"
	}
	return line, ""
}

// missingOldTextError explains a zero-match edit in terms of the copy-paste
// traps that actually cause it, plus the closest line when one is recognizable.
func missingOldTextError(data []byte, oldText string) error {
	message := "old_text must occur exactly once; found 0 occurrences"
	if hint := nearMissHint(data, oldText); hint != "" {
		message += "; " + hint
	}
	message += ". Copy raw file text: drop the line-number gutter that read adds (\"     1\\t\") and keep the file's own line endings"
	return fmt.Errorf("%s", message)
}

// nearMissHint points at the line of data that most resembles old_text's first
// line, so a failing edit says what to re-read instead of only the count.
func nearMissHint(data []byte, oldText string) string {
	lines := splitLinesKeepEOL(oldText)
	first := ""
	for _, line := range lines {
		body, _ := splitLineEOL(line)
		if trimmed := strings.TrimSpace(body); trimmed != "" {
			first = trimmed
			break
		}
	}
	if first == "" {
		return ""
	}
	dataLines := strings.Split(string(data), "\n")
	for index, raw := range dataLines {
		line := strings.TrimSuffix(raw, "\r")
		if strings.TrimSpace(line) != first {
			continue
		}
		if len(lines) > 1 {
			return fmt.Sprintf("line %d matches the first old_text line but the following lines differ", index+1)
		}
		if line != first {
			return fmt.Sprintf("line %d matches except for surrounding whitespace", index+1)
		}
		return ""
	}
	lower := strings.ToLower(first)
	for index, raw := range dataLines {
		line := strings.TrimSuffix(raw, "\r")
		if strings.ToLower(strings.TrimSpace(line)) == lower {
			return fmt.Sprintf("line %d matches except for letter case", index+1)
		}
	}
	return ""
}
