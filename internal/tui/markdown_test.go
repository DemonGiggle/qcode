package tui

import (
	"bytes"
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
		`fmt.Println("hi")`,
		"╰─",
		"site (https://example.com) and old",
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
	if plain != "safe ␛[2J text" {
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
	if plain != "one two\nthree four" {
		t.Fatalf("wrapped output = %q", plain)
	}
}

func TestMarkdownWriterWrapsWithoutColor(t *testing.T) {
	var output bytes.Buffer
	writer := NewMarkdownWriter(&output, false, 7)
	writer.BeginResponse()
	_, _ = writer.Write([]byte("one two three"))
	writer.EndResponse()
	if got := output.String(); got != "one two\nthree" {
		t.Fatalf("wrapped output = %q", got)
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
	want := "- item\n| quote\n+- text\nvalue\n+-\nsafe <ESC>[2J"
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
	if plain != "considering options\nfinal answer" {
		t.Fatalf("plain output = %q", plain)
	}
}
