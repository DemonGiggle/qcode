package tools

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
)

const (
	diffContextLines = 3
	maxDiffLines     = 200
)

func unifiedDiff(path string, before, after []byte, existed bool) string {
	if bytes.Equal(before, after) {
		return ""
	}
	path = strings.ReplaceAll(filepath.ToSlash(path), "\n", "?")
	path = strings.ReplaceAll(path, "\r", "?")
	if bytes.IndexByte(before, 0) >= 0 || bytes.IndexByte(after, 0) >= 0 {
		return "Binary file changed: " + path
	}
	oldLines := diffLines(string(before))
	newLines := diffLines(string(after))
	oldHasFinalNewline := len(before) > 0 && before[len(before)-1] == '\n'
	newHasFinalNewline := len(after) > 0 && after[len(after)-1] == '\n'
	prefix := commonPrefix(oldLines, newLines)
	newlineOnlyChange := prefix == len(oldLines) && prefix == len(newLines) && oldHasFinalNewline != newHasFinalNewline
	suffix := 0
	if newlineOnlyChange && prefix > 0 {
		prefix--
	} else {
		suffix = commonSuffix(oldLines[prefix:], newLines[prefix:])
	}
	oldChangedEnd := len(oldLines) - suffix
	newChangedEnd := len(newLines) - suffix
	hunkStart := max(0, prefix-diffContextLines)
	oldEnd := min(len(oldLines), oldChangedEnd+diffContextLines)
	newEnd := min(len(newLines), newChangedEnd+diffContextLines)

	oldPath := "a/" + path
	if !existed {
		oldPath = "/dev/null"
	}
	lines := []string{"--- " + oldPath, "+++ b/" + path}
	lines = append(lines, fmt.Sprintf("@@ -%d,%d +%d,%d @@", hunkLine(hunkStart, len(oldLines)), oldEnd-hunkStart, hunkLine(hunkStart, len(newLines)), newEnd-hunkStart))
	for _, line := range oldLines[hunkStart:prefix] {
		lines = append(lines, " "+line)
	}
	for index, line := range oldLines[prefix:oldChangedEnd] {
		lines = append(lines, "-"+line)
		if prefix+index == len(oldLines)-1 && !oldHasFinalNewline {
			lines = append(lines, "\\ No newline at end of file")
		}
	}
	for index, line := range newLines[prefix:newChangedEnd] {
		lines = append(lines, "+"+line)
		if prefix+index == len(newLines)-1 && !newHasFinalNewline {
			lines = append(lines, "\\ No newline at end of file")
		}
	}
	for _, line := range newLines[newChangedEnd:newEnd] {
		lines = append(lines, " "+line)
	}
	if len(lines) > maxDiffLines {
		omitted := len(lines) - maxDiffLines
		lines = append(lines[:maxDiffLines], fmt.Sprintf("... diff truncated: %d lines omitted ...", omitted))
	}
	return strings.Join(lines, "\n")
}

func diffLines(text string) []string {
	if text == "" {
		return nil
	}
	lines := strings.Split(text, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func commonPrefix(left, right []string) int {
	limit := min(len(left), len(right))
	index := 0
	for index < limit && left[index] == right[index] {
		index++
	}
	return index
}

func commonSuffix(left, right []string) int {
	limit := min(len(left), len(right))
	count := 0
	for count < limit && left[len(left)-1-count] == right[len(right)-1-count] {
		count++
	}
	return count
}

func hunkLine(start, total int) int {
	if total == 0 {
		return 0
	}
	return start + 1
}
