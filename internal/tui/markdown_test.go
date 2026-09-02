package tui

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"testing"
)

var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func TestMarkdownWriterRendersStreamedSyntax(t *testing.T) {
	var output bytes.Buffer
	writer := NewMarkdownWriter(&output, true)
	writer.BeginResponse()
	chunks := []string{
		"# Head", "ing\n\n- **bo", "ld** and `code`\n",
		"> quoted\n```go\nfmt.Println(\"hi\")\n```\n",
		"[site](https://example.com) and ~~old~~\n",
	}
	for _, chunk := range chunks {
		if _, err := writer.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	writer.EndResponse()

	plain := ansiPattern.ReplaceAllString(output.String(), "")
	want := strings.Join([]string{
		"Heading",
		"",
		"• bold and code",
		"│ quoted",
		"╭─ go",
		`│ fmt.Println("hi")`,
		"╰─",
		"• site (https://example.com) and old",
		"",
	}, "\n")
	if plain != want {
		t.Fatalf("plain output:\n%q\nwant:\n%q", plain, want)
	}
	for name, sequence := range map[string]string{"bold": bold, "heading": cyan, "code": yellow, "link": underline, "strike": strikethrough} {
		if !strings.Contains(output.String(), sequence) {
			t.Errorf("output has no %s style", name)
		}
	}
}

func TestMarkdownWriterReportsWhenStreamChunkBecomesVisible(t *testing.T) {
	writer := NewMarkdownWriter(&bytes.Buffer{}, true, 80)
	if writer.StreamChunkCompletesLine("partial") {
		t.Fatal("partial line reported as visible")
	}
	if !writer.StreamChunkCompletesLine("completed\nnext") {
		t.Fatal("completed line reported as buffered")
	}
}

func TestMarkdownWriterRendersAlignedTable(t *testing.T) {
	var output bytes.Buffer
	writer := NewMarkdownWriter(&output, true, 80)
	writer.BeginResponse()
	chunks := []string{
		"| Name | Sco", "re | Note |\n| :--- | ---: | :---: |\n",
		"| **Ada** | 7 | ready |\n| Bob | 42 | waiting |",
	}
	for _, chunk := range chunks {
		_, _ = writer.Write([]byte(chunk))
	}
	writer.EndResponse()

	plain := ansiPattern.ReplaceAllString(output.String(), "")
	want := strings.Join([]string{
		"┌──────┬───────┬─────────┐",
		"│ Name │ Score │  Note   │",
		"├──────┼───────┼─────────┤",
		"│ Ada  │     7 │  ready  │",
		"│ Bob  │    42 │ waiting │",
		"└──────┴───────┴─────────┘",
	}, "\n")
	if plain != want {
		t.Fatalf("table output:\n%s\nwant:\n%s", plain, want)
	}
	if !strings.Contains(output.String(), bold+"Name"+reset) {
		t.Fatalf("table header is not bold: %q", output.String())
	}
}

func TestMarkdownWriterBoundsWideTableToTerminal(t *testing.T) {
	var output bytes.Buffer
	writer := NewMarkdownWriter(&output, false, 24)
	writer.BeginResponse()
	_, _ = writer.Write([]byte("Name | Description\n--- | ---\nalpha | a very long description that cannot fit\n"))
	writer.EndResponse()

	for _, line := range strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n") {
		if width := visibleWidth(line); width > 24 {
			t.Errorf("table line width = %d: %q", width, line)
		}
	}
	if !strings.Contains(output.String(), "…") {
		t.Fatalf("wide cell was not truncated: %q", output.String())
	}
}

func TestMarkdownWriterLeavesInvalidTableAsMarkdown(t *testing.T) {
	var output bytes.Buffer
	writer := NewMarkdownWriter(&output, true, 80)
	writer.BeginResponse()
	_, _ = writer.Write([]byte("| not | a table |\nordinary text\n"))
	writer.EndResponse()
	plain := ansiPattern.ReplaceAllString(output.String(), "")
	if plain != "| not | a table |\n• ordinary text\n" {
		t.Fatalf("output = %q", plain)
	}
}

func TestMarkdownWriterUsesASCIITableBorders(t *testing.T) {
	var output bytes.Buffer
	writer := NewMarkdownWriter(&output, false, 80)
	writer.SetUnicode(false)
	writer.BeginResponse()
	_, _ = writer.Write([]byte("| A | B |\n| --- | --- |\n| x | y |\n"))
	writer.EndResponse()
	if got := output.String(); got != "+---+---+\n| A | B |\n+---+---+\n| x | y |\n+---+---+\n" {
		t.Fatalf("ASCII table = %q", got)
	}
}

func TestMarkdownWriterNeutralizesTableEscapeSequences(t *testing.T) {
	var output bytes.Buffer
	writer := NewMarkdownWriter(&output, true, 80)
	writer.BeginResponse()
	_, _ = writer.Write([]byte("| Value |\n| --- |\n| safe \x1b[2J |\n"))
	writer.EndResponse()
	plain := ansiPattern.ReplaceAllString(output.String(), "")
	if !strings.Contains(plain, "safe ␛[2J") {
		t.Fatalf("table escape was not neutralized: %q", plain)
	}
}

func TestMarkdownWriterKeepsEscapedAndCodePipesInsideCells(t *testing.T) {
	var output bytes.Buffer
	writer := NewMarkdownWriter(&output, true, 80)
	writer.BeginResponse()
	_, _ = writer.Write([]byte("| Plain | Code |\n| --- | --- |\n| one \\| two | `x|y` |\n"))
	writer.EndResponse()
	plain := ansiPattern.ReplaceAllString(output.String(), "")
	if !strings.Contains(plain, "│ one | two │ x|y  │") {
		t.Fatalf("cell pipes split columns: %q", plain)
	}
}

func TestMarkdownWriterPassesThroughWhenDisabled(t *testing.T) {
	var output bytes.Buffer
	writer := NewMarkdownWriter(&output, false)
	writer.BeginResponse()
	input := "# heading\n**bold** and `code`"
	if _, err := writer.Write([]byte(input)); err != nil {
		t.Fatal(err)
	}
	writer.EndResponse()
	if output.String() != input {
		t.Fatalf("output = %q", output.String())
	}
}

func TestMarkdownWriterNeutralizesModelEscapeSequences(t *testing.T) {
	var output bytes.Buffer
	writer := NewMarkdownWriter(&output, true)
	writer.BeginResponse()
	_, _ = writer.Write([]byte("safe \x1b[2J text"))
	writer.EndResponse()
	plain := ansiPattern.ReplaceAllString(output.String(), "")
	if plain != "• safe ␛[2J text" {
		t.Fatalf("plain output = %q", plain)
	}
}

func TestMarkdownWriterWrapsAtWordBoundaries(t *testing.T) {
	var output bytes.Buffer
	writer := NewMarkdownWriter(&output, true, 12)
	writer.BeginResponse()
	_, _ = writer.Write([]byte("one two three four"))
	writer.EndResponse()
	plain := ansiPattern.ReplaceAllString(output.String(), "")
	if plain != "• one two\n  three four" {
		t.Fatalf("wrapped output = %q", plain)
	}
}

func TestMarkdownWriterWrapsWithoutColor(t *testing.T) {
	var output bytes.Buffer
	writer := NewMarkdownWriter(&output, false, 7)
	writer.BeginResponse()
	_, _ = writer.Write([]byte("one two three"))
	writer.EndResponse()
	if got := output.String(); got != "• one\n  two\n  three" {
		t.Fatalf("wrapped output = %q", got)
	}
}

func TestMarkdownWriterKeepsCodeGuideOnWrappedRows(t *testing.T) {
	var output bytes.Buffer
	writer := NewMarkdownWriter(&output, true, 10)
	writer.BeginResponse()
	_, _ = writer.Write([]byte("```txt\none two three\n```\n"))
	writer.EndResponse()
	plain := ansiPattern.ReplaceAllString(output.String(), "")
	if plain != "╭─ txt\n│ one two\n│ three\n╰─\n" {
		t.Fatalf("wrapped code block = %q", plain)
	}
}

func TestMarkdownWriterUsesASCIIGlyphs(t *testing.T) {
	var output bytes.Buffer
	writer := NewMarkdownWriter(&output, true)
	writer.SetUnicode(false)
	writer.BeginResponse()
	_, _ = writer.Write([]byte("- item\n> quote\n```text\nvalue\n```\nsafe \x1b[2J"))
	writer.EndResponse()
	plain := ansiPattern.ReplaceAllString(output.String(), "")
	want := "- item\n| quote\n+- text\n| value\n+-\n* safe <ESC>[2J"
	if plain != want {
		t.Fatalf("ASCII output = %q, want %q", plain, want)
	}
}

func TestMarkdownWriterStylesThinkingGray(t *testing.T) {
	var output bytes.Buffer
	writer := NewMarkdownWriter(&output, true, 80)
	writer.BeginResponse()
	writer.BeginThinking()
	_, _ = writer.Write([]byte("considering options"))
	writer.EndThinking()
	_, _ = writer.Write([]byte("**final answer**"))
	writer.EndResponse()

	if !strings.Contains(output.String(), gray+"considering options"+reset) {
		t.Fatalf("thinking was not gray: %q", output.String())
	}
	if !strings.Contains(output.String(), bold+"final answer"+reset) {
		t.Fatalf("final answer lost normal Markdown styling: %q", output.String())
	}
	plain := ansiPattern.ReplaceAllString(output.String(), "")
	if plain != "• considering options\n• final answer" {
		t.Fatalf("plain output = %q", plain)
	}
}

func TestMarkdownWriterRendersColoredDiff(t *testing.T) {
	var output bytes.Buffer
	writer := NewMarkdownWriter(&output, true, 80)
	writer.EnableDiffs()
	writer.WriteDiff("--- a/file.go\n+++ b/file.go\n@@ -1,1 +1,1 @@\n-old\n+new")
	got := output.String()
	for name, sequence := range map[string]string{
		"summary marker": cyan + "• ",
		"file name":      bold + "Edited file.go",
		"hunk":           cyan + "@@",
		"removal":        red + "-old",
		"addition":       green + "+new",
		"gutter":         dim + "│ ",
	} {
		if !strings.Contains(got, sequence) {
			t.Errorf("diff has no %s style: %q", name, got)
		}
	}
}

func TestMarkdownWriterSummarizesAddedFile(t *testing.T) {
	var output bytes.Buffer
	writer := NewMarkdownWriter(&output, false, 80)
	writer.EnableDiffs()
	writer.WriteDiff("--- /dev/null\n+++ b/new.go\n@@ -0,0 +1,1 @@\n+package main")
	if got := output.String(); !strings.HasPrefix(got, "• Added new.go (+1 -0) · diff 1\n") {
		t.Fatalf("added-file summary = %q", got)
	}
}

func TestMarkdownWriterDoesNotRenderDiffUnlessEnabled(t *testing.T) {
	var output bytes.Buffer
	writer := NewMarkdownWriter(&output, true, 80)
	writer.WriteDiff("+not shown")
	if output.Len() != 0 || writer.DiffEnabled() {
		t.Fatalf("disabled diff output = %q", output.String())
	}
}

func TestMarkdownWriterNeutralizesDiffEscapeSequences(t *testing.T) {
	var output bytes.Buffer
	writer := NewMarkdownWriter(&output, true, 80)
	writer.EnableDiffs()
	writer.WriteDiff("+safe \x1b[2J\r\a")
	plain := ansiPattern.ReplaceAllString(output.String(), "")
	if plain != "• Edited file (+1 -0) · diff 1\n│ +safe ␛[2J<0x0D><0x07>\n╰─\n" {
		t.Fatalf("diff output = %q", plain)
	}
}

func TestMarkdownWriterLimitsAndBalancesDiffPreview(t *testing.T) {
	var lines []string
	lines = append(lines, "--- a/file.go", "+++ b/file.go", "@@ -1,12 +1,12 @@")
	for index := range 12 {
		lines = append(lines, fmt.Sprintf("-old %d", index))
	}
	for index := range 12 {
		lines = append(lines, fmt.Sprintf("+new %d", index))
	}
	var output bytes.Buffer
	writer := NewMarkdownWriter(&output, false, 80)
	writer.EnableDiffs()
	writer.WriteDiff(strings.Join(lines, "\n"))

	preview := strings.TrimSuffix(output.String(), "\n")
	if rows := strings.Count(preview, "\n") + 1; rows != maxDiffPreviewRows {
		t.Fatalf("preview rows = %d:\n%s", rows, preview)
	}
	for _, expected := range []string{"• Edited file.go (+12 -12) · diff 1", "│ -old 0", "│ -old 3", "│ +new 0", "│ +new 2", "╰─ /diff 1 to expand · 17 hidden"} {
		if !strings.Contains(preview, expected) {
			t.Errorf("preview missing %q:\n%s", expected, preview)
		}
	}
}

func TestDiffPreviewIgnoresTrailingNewline(t *testing.T) {
	var output bytes.Buffer
	writer := NewMarkdownWriter(&output, false, 80)
	writer.EnableDiffs()
	writer.WriteDiff("--- a/file.go\n+++ b/file.go\n@@ -1 +1 @@\n-old\n+new\n")

	got := output.String()
	if strings.Contains(got, "│ \n") {
		t.Fatalf("preview includes a synthetic empty line: %q", got)
	}
	if rows := strings.Count(strings.TrimSuffix(got, "\n"), "\n") + 1; rows != 5 {
		t.Fatalf("preview rows = %d: %q", rows, got)
	}
}

func TestMarkdownWriterExpandsNumberedDiff(t *testing.T) {
	var output bytes.Buffer
	writer := NewMarkdownWriter(&output, false, 80)
	writer.EnableDiffs()
	writer.WriteDiff("--- a/one\n+++ b/one\n-old\n+new")
	writer.WriteDiff("--- a/two\n+++ b/two\n-before\n+after")
	output.Reset()

	number, total, ok := writer.WriteStoredDiff(1)
	if !ok || number != 1 || total != 2 {
		t.Fatalf("expanded diff = number %d, total %d, ok %v", number, total, ok)
	}
	if got := output.String(); !strings.Contains(got, "• Edited one (+1 -1) · diff 1 · expanded\n") || !strings.Contains(got, "│ +new\n") || strings.Contains(got, "+after\n") {
		t.Fatalf("expanded output = %q", got)
	}
	output.Reset()
	if number, total, ok = writer.WriteStoredDiff(0); !ok || number != 2 || total != 2 || !strings.Contains(output.String(), "│ +after\n") {
		t.Fatalf("latest diff = number %d, total %d, ok %v, output %q", number, total, ok, output.String())
	}

	writer.ResetDiffs()
	if _, total, ok := writer.WriteStoredDiff(0); ok || total != 0 {
		t.Fatalf("diffs remained after reset: total %d, ok %v", total, ok)
	}
}

func TestExpandedDiffWrapsWithGuide(t *testing.T) {
	var output bytes.Buffer
	writer := NewMarkdownWriter(&output, false, 12)
	writer.EnableDiffs()
	writer.WriteDiff("--- a/file\n+++ b/file\n+abcdefghijklmnop")
	output.Reset()

	writer.WriteStoredDiff(1)
	got := output.String()
	if !strings.Contains(got, "│ +abcdefghi\n│ jklmnop\n") {
		t.Fatalf("wrapped expanded diff lost its guide: %q", got)
	}
}

func TestDiffPreviewStaysWithinRowsOnNarrowTerminal(t *testing.T) {
	var output bytes.Buffer
	writer := NewMarkdownWriter(&output, false, 1)
	writer.SetUnicode(false)
	writer.EnableDiffs()
	writer.WriteDiff(strings.Repeat("+very-long-line\n", 12))
	if rows := strings.Count(strings.TrimSuffix(output.String(), "\n"), "\n") + 1; rows != maxDiffPreviewRows {
		t.Fatalf("preview rows = %d: %q", rows, output.String())
	}
}
