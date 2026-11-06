package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"qcode/internal/config"
	"qcode/internal/tui"
)

func runConfig(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: qcode config check [--json]")
	}
	if args[0] != "check" {
		return fmt.Errorf("usage: qcode config check [--json]")
	}
	fs := flag.NewFlagSet("qcode config check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOut := fs.Bool("json", false, "emit issues as JSON")
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: qcode config check [--json]")
	}
	issues, inspected, err := config.CheckDefault()
	if err != nil {
		return err
	}
	if *jsonOut {
		type payload struct {
			Files  []string       `json:"files"`
			Issues []config.Issue `json:"issues"`
		}
		files := inspected
		if files == nil {
			files = []string{}
		}
		if issues == nil {
			issues = []config.Issue{}
		}
		data, merr := json.MarshalIndent(payload{Files: files, Issues: issues}, "", "  ")
		if merr != nil {
			return merr
		}
		fmt.Fprintln(stdout, string(data))
	} else {
		writeHumanReport(stdout, inspected, issues, configUseColor(stdout), configTermWidth(stdout))
	}
	if config.HasErrors(issues) {
		return fmt.Errorf("config check found %d error(s)", countErrors(issues))
	}
	return nil
}

func countErrors(issues []config.Issue) int {
	n := 0
	for _, issue := range issues {
		if issue.Severity == config.SeverityError {
			n++
		}
	}
	return n
}

func countWarnings(issues []config.Issue) int {
	n := 0
	for _, issue := range issues {
		if issue.Severity == config.SeverityWarning {
			n++
		}
	}
	return n
}

// ANSI styles for the human-readable report. They are only emitted when the
// output is a terminal without NO_COLOR (see configUseColor).
const (
	ansiReset      = "\x1b[0m"
	ansiBold       = "\x1b[1m"
	ansiRedBold    = "\x1b[1;31m"
	ansiYellowBold = "\x1b[1;33m"
	ansiGreenBold  = "\x1b[1;32m"
	ansiCyan       = "\x1b[36m"
)

func configUseColor(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return tui.ColorEnabled(f)
}

func configTermWidth(w io.Writer) int {
	f, ok := w.(*os.File)
	if !ok {
		return 0
	}
	return tui.OutputWidth(f)
}

func paint(s, code string, color bool) string {
	if !color || code == "" {
		return s
	}
	return code + s + ansiReset
}

// wrapField word-wraps text under a hanging indent. firstIndent prefixes the
// first line (it must end with a space); continuation lines are padded with
// spaces to the same width. A non-positive width disables wrapping.
func wrapField(firstIndent, text string, width int) string {
	contIndent := strings.Repeat(" ", len(firstIndent))
	if width <= 0 {
		return firstIndent + text
	}
	words := strings.Fields(text)
	if len(words) == 0 {
		return strings.TrimRight(firstIndent, " ")
	}
	var lines []string
	line := firstIndent
	lineLen := len(firstIndent)
	for _, word := range words {
		extra := len(word)
		if lineLen > len(firstIndent) && !strings.HasSuffix(line, " ") {
			extra++ // separating space
		}
		if lineLen+extra > width && lineLen > len(firstIndent) {
			lines = append(lines, strings.TrimRight(line, " "))
			line = contIndent + word
			lineLen = len(contIndent) + len(word)
			continue
		}
		if lineLen > len(firstIndent) && !strings.HasSuffix(line, " ") {
			line += " "
			lineLen++
		}
		line += word
		lineLen += len(word)
	}
	lines = append(lines, line)
	return strings.Join(lines, "\n")
}

// writeHumanReport prints the grouped, indented check report. Issues are
// grouped by file (checked files first, then derived groups such as
// "(merged)"), each finding showing severity, key, problem, and fix on
// indented lines. Only badges, keys, paths, and the summary carry color.
func writeHumanReport(w io.Writer, inspected []string, issues []config.Issue, color bool, width int) {
	if len(inspected) == 0 {
		fmt.Fprintln(w, "No config files found; using built-in defaults.")
	} else {
		fileWord := "file"
		if len(inspected) != 1 {
			fileWord = "files"
		}
		fmt.Fprintf(w, "Checked %d %s:\n", len(inspected), fileWord)
		for _, path := range inspected {
			fmt.Fprintln(w, "  "+paint(path, ansiCyan, color))
		}
	}
	if len(issues) == 0 {
		if len(inspected) > 0 {
			fmt.Fprintln(w, "")
		}
		fmt.Fprintln(w, paint("OK: no errors or warnings.", ansiGreenBold, color))
		return
	}
	// Group issues by file, keeping checked files first in inspected order
	// and derived groups (e.g. "(merged)") last.
	groups := map[string][]config.Issue{}
	for _, issue := range issues {
		groups[issue.File] = append(groups[issue.File], issue)
	}
	var ordered []string
	seen := map[string]bool{}
	for _, path := range inspected {
		if len(groups[path]) > 0 && !seen[path] {
			ordered = append(ordered, path)
			seen[path] = true
		}
	}
	var rest []string
	for file := range groups {
		if !seen[file] {
			rest = append(rest, file)
		}
	}
	// Keep the leftover order deterministic.
	for i := 1; i < len(rest); i++ {
		for j := i; j > 0 && rest[j] < rest[j-1]; j-- {
			rest[j], rest[j-1] = rest[j-1], rest[j]
		}
	}
	ordered = append(ordered, rest...)

	fmt.Fprintln(w, "")
	for gi, file := range ordered {
		fmt.Fprintln(w, paint(file+":", ansiBold, color))
		for _, issue := range groups[file] {
			badge := "WARNING"
			badgeStyle := ansiYellowBold
			if issue.Severity == config.SeverityError {
				badge = "ERROR"
				badgeStyle = ansiRedBold
			}
			head := "  " + paint(fmt.Sprintf("%-7s", badge), badgeStyle, color)
			if issue.Key != "" {
				head += " " + paint(issue.Key, ansiBold, color)
			}
			fmt.Fprintln(w, head)
			fmt.Fprintln(w, wrapField("    problem: ", issue.Message, width))
			fmt.Fprintln(w, wrapField("    fix:     ", issue.Fix, width))
		}
		if gi < len(ordered)-1 {
			fmt.Fprintln(w, "")
		}
	}
	fmt.Fprintln(w, "")
	errors := countErrors(issues)
	warnings := countWarnings(issues)
	var summary string
	var style string
	switch {
	case errors > 0 && warnings > 0:
		summary = fmt.Sprintf("Result: FAIL — %d error%s, %d warning%s.", errors, plural(errors), warnings, plural(warnings))
		style = ansiRedBold
	case errors > 0:
		summary = fmt.Sprintf("Result: FAIL — %d error%s.", errors, plural(errors))
		style = ansiRedBold
	default:
		summary = fmt.Sprintf("Result: OK with %d warning%s.", warnings, plural(warnings))
		style = ansiYellowBold
	}
	fmt.Fprintln(w, paint(summary, style, color))
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
