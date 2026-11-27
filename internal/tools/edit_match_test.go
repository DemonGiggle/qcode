package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"qcode/internal/llm"
)

func editRegistry(t *testing.T) (*Registry, string) {
	t.Helper()
	root := t.TempDir()
	registry, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	return registry, root
}

func writeFile(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, root, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func editDetailed(t *testing.T, registry *Registry, args map[string]any) (llm.ToolResult, error) {
	t.Helper()
	data, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return registry.ExecuteDetailed(context.Background(), llm.ToolCall{Name: "edit", Arguments: data})
}

// TestEditExactMatchStaysByteExact guards the original behavior: an exact
// match replaces once and reports no tolerated difference.
func TestEditExactMatchStaysByteExact(t *testing.T) {
	registry, root := editRegistry(t)
	writeFile(t, root, "a.txt", "alpha\r\nbeta\r\ngamma\r\n")

	result, err := editDetailed(t, registry, map[string]any{"path": "a.txt", "old_text": "beta\r\n", "new_text": "BETA\r\n"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "edited a.txt" {
		t.Fatalf("output = %q, want no tolerated-difference note", result.Output)
	}
	if got := readFile(t, root, "a.txt"); got != "alpha\r\nBETA\r\ngamma\r\n" {
		t.Fatalf("file = %q", got)
	}
}

// TestEditToleratesReadGutter covers the copy-paste trap created by read's
// "     1\t" line decoration.
func TestEditToleratesReadGutter(t *testing.T) {
	registry, root := editRegistry(t)
	writeFile(t, root, "a.txt", "one\ntwo\nthree\n")

	// Read the file the way a model does, then edit with the gutter intact.
	readOut, err := call(t, registry, "read", map[string]any{"path": "a.txt"})
	if err != nil {
		t.Fatal(err)
	}
	snippet := strings.TrimSuffix(readOut, "\n") // "     1\tone\n     2\ttwo\n     3\tthree"

	result, err := editDetailed(t, registry, map[string]any{"path": "a.txt", "old_text": snippet, "new_text": "     1\tone\n     2\tTWO\n     3\tthree"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Output, "gutter") {
		t.Fatalf("output = %q, want a gutter note", result.Output)
	}
	if got := readFile(t, root, "a.txt"); got != "one\nTWO\nthree\n" {
		t.Fatalf("file = %q", got)
	}
}

// TestEditToleratesCRLF covers the hidden-EOL trap: read strips "\r", so a
// multi-line snippet arrives with LF only.
func TestEditToleratesCRLF(t *testing.T) {
	registry, root := editRegistry(t)
	writeFile(t, root, "a.txt", "one\r\ntwo\r\nthree\r\n")

	result, err := editDetailed(t, registry, map[string]any{"path": "a.txt", "old_text": "one\ntwo", "new_text": "one\n2"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Output, "line-ending") {
		t.Fatalf("output = %q, want a line-ending note", result.Output)
	}
	// Untouched lines keep CRLF and the replacement adopts the file's ending.
	if got := readFile(t, root, "a.txt"); got != "one\r\n2\r\nthree\r\n" {
		t.Fatalf("file = %q", got)
	}
	if result.Diff == "" {
		t.Fatal("expected a diff for a successful edit")
	}
}

// TestEditCRLFKeepsUntouchedMixedEOL ensures the tolerant path never rewrites
// line endings outside the replaced span.
func TestEditCRLFKeepsUntouchedMixedEOL(t *testing.T) {
	registry, root := editRegistry(t)
	writeFile(t, root, "a.txt", "a\r\nb\nc\r\n")

	if _, err := editDetailed(t, registry, map[string]any{"path": "a.txt", "old_text": "b\nc", "new_text": "b\nC"}); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, root, "a.txt"); got != "a\r\nb\nC\r\n" {
		t.Fatalf("file = %q", got)
	}
}

// TestEditToleratesGutterAndCRLF covers the most common real copy: a read
// result from a CRLF file carries both the gutter and LF-only endings, so
// neither single-trap fallback alone can match it.
func TestEditToleratesGutterAndCRLF(t *testing.T) {
	registry, root := editRegistry(t)
	writeFile(t, root, "a.txt", "one\r\ntwo\r\nthree\r\n")

	readOut, err := call(t, registry, "read", map[string]any{"path": "a.txt"})
	if err != nil {
		t.Fatal(err)
	}
	snippet := strings.TrimSuffix(readOut, "\n")
	if strings.Contains(snippet, "\r") {
		t.Fatalf("read output = %q, want no CRLF", snippet)
	}

	result, err := editDetailed(t, registry, map[string]any{"path": "a.txt", "old_text": snippet, "new_text": "     1\tone\n     2\tTWO\n     3\tthree"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"gutter", "line-ending"} {
		if !strings.Contains(result.Output, want) {
			t.Fatalf("output = %q, want %q", result.Output, want)
		}
	}
	// Gutter gone, CRLF preserved.
	if got := readFile(t, root, "a.txt"); got != "one\r\nTWO\r\nthree\r\n" {
		t.Fatalf("file = %q", got)
	}
}

// TestEditGutterReplaceAllUsesPerMatchLineEnding keeps a gutter-tolerated
// replace_all from stamping the first span's terminator onto later spans in a
// mixed-EOL file: each span follows the line ending that its own match sees.
func TestEditGutterReplaceAllUsesPerMatchLineEnding(t *testing.T) {
	registry, root := editRegistry(t)
	writeFile(t, root, "a.txt", "X\r\nY\nX\n")

	if _, err := editDetailed(t, registry, map[string]any{"path": "a.txt", "old_text": "     1\tX", "new_text": "a\nb", "replace_all": true}); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, root, "a.txt"); got != "a\r\nb\r\nY\na\nb\n" {
		t.Fatalf("file = %q", got)
	}
}

// TestSpliceMatchesUsesPerMatchReplacement pins the splice contract directly.
func TestSpliceMatchesUsesPerMatchReplacement(t *testing.T) {
	got, err := spliceMatches([]byte("aXbXc"), []editMatch{{start: 1, end: 2}, {start: 3, end: 4}}, [][]byte{[]byte("1"), []byte("2")})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "a1b2c" {
		t.Fatalf("spliced = %q", got)
	}
}

// TestSpliceMatchesRejectsReplacementCountMismatch keeps a caller bug loud
// instead of silently deleting matched bytes.
func TestSpliceMatchesRejectsReplacementCountMismatch(t *testing.T) {
	if _, err := spliceMatches([]byte("aXb"), []editMatch{{start: 1, end: 2}}, nil); err == nil {
		t.Fatal("expected an internal error for a missing replacement")
	}
}

// TestEditMixedEOLFirstLevelWins pins the deliberate first-level-wins rule: an
// exact LF occurrence is edited alone, and the CRLF twin is left for the model
// to re-read rather than silently rewritten.
func TestEditMixedEOLFirstLevelWins(t *testing.T) {
	t.Run("replace_all edits only the exact subset", func(t *testing.T) {
		registry, root := editRegistry(t)
		writeFile(t, root, "a.txt", "a\nbX\na\r\nb\n")
		result, err := editDetailed(t, registry, map[string]any{"path": "a.txt", "old_text": "a\nb", "new_text": "y\nz", "replace_all": true})
		if err != nil {
			t.Fatal(err)
		}
		if result.Output != "edited a.txt" {
			t.Fatalf("output = %q, want no tolerated-difference note", result.Output)
		}
		if got := readFile(t, root, "a.txt"); got != "y\nzX\na\r\nb\n" {
			t.Fatalf("file = %q", got)
		}
	})
	t.Run("exactly-once ignores the CRLF twin", func(t *testing.T) {
		registry, root := editRegistry(t)
		writeFile(t, root, "a.txt", "a\nbX\na\r\nb\n")
		if _, err := editDetailed(t, registry, map[string]any{"path": "a.txt", "old_text": "a\nb", "new_text": "y\nz"}); err != nil {
			t.Fatalf("exact subset should be unambiguous, got %v", err)
		}
		if got := readFile(t, root, "a.txt"); got != "y\nzX\na\r\nb\n" {
			t.Fatalf("file = %q", got)
		}
	})
}

// TestEditReplaceAll covers the explicit multi-occurrence path.
func TestEditReplaceAll(t *testing.T) {
	registry, root := editRegistry(t)
	writeFile(t, root, "a.txt", "x\ny\nx\n")

	if _, err := editDetailed(t, registry, map[string]any{"path": "a.txt", "old_text": "x", "new_text": "1", "replace_all": true}); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, root, "a.txt"); got != "1\ny\n1\n" {
		t.Fatalf("file = %q", got)
	}
}

// TestEditAmbiguousOccurrences verifies the default stays exactly-once and the
// error names the recovery options.
func TestEditAmbiguousOccurrences(t *testing.T) {
	registry, root := editRegistry(t)
	writeFile(t, root, "a.txt", "x\nx\n")

	_, err := editDetailed(t, registry, map[string]any{"path": "a.txt", "old_text": "x", "new_text": "1"})
	if err == nil {
		t.Fatal("expected an ambiguity error")
	}
	for _, want := range []string{"found 2 occurrences", "replace_all"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want %q", err, want)
		}
	}
	if got := readFile(t, root, "a.txt"); got != "x\nx\n" {
		t.Fatalf("file = %q, want unchanged", got)
	}
}

// TestEditMissReportsWhitespaceHint covers the near-miss hint for a tab/space
// mismatch, which the tool must not silently rewrite.
func TestEditMissReportsWhitespaceHint(t *testing.T) {
	registry, root := editRegistry(t)
	writeFile(t, root, "a.go", "package a\n\nfunc main() {\n\treturn\n}\n")

	_, err := editDetailed(t, registry, map[string]any{"path": "a.go", "old_text": "    return", "new_text": "\treturn nil"})
	if err == nil {
		t.Fatal("expected a mismatch error")
	}
	for _, want := range []string{"found 0 occurrences", "line 4", "surrounding whitespace"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want %q", err, want)
		}
	}
	if got := readFile(t, root, "a.go"); got != "package a\n\nfunc main() {\n\treturn\n}\n" {
		t.Fatalf("file = %q, want unchanged", got)
	}
}

// TestEditMissMentionsRawBytes makes the gutter/line-ending contract visible in
// every zero-match error.
func TestEditMissMentionsRawBytes(t *testing.T) {
	registry, root := editRegistry(t)
	writeFile(t, root, "a.txt", "alpha\n")

	_, err := editDetailed(t, registry, map[string]any{"path": "a.txt", "old_text": "not-there", "new_text": "x"})
	if err == nil {
		t.Fatal("expected a mismatch error")
	}
	for _, want := range []string{"raw file text", "line-number gutter", "line endings"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want %q", err, want)
		}
	}
}

// TestEditMissReportsMultiLineHint distinguishes a stale multi-line snippet
// from a completely absent one.
func TestEditMissReportsMultiLineHint(t *testing.T) {
	registry, root := editRegistry(t)
	writeFile(t, root, "a.txt", "first\nsecond\nthird\n")

	_, err := editDetailed(t, registry, map[string]any{"path": "a.txt", "old_text": "first\nstale", "new_text": "x"})
	if err == nil {
		t.Fatal("expected a mismatch error")
	}
	if !strings.Contains(err.Error(), "line 1 matches the first old_text line") {
		t.Fatalf("error = %q", err)
	}
}

// TestEditSchemaExposesReplaceAll keeps the schema, decoder, and prompt in sync.
func TestEditSchemaExposesReplaceAll(t *testing.T) {
	registry, _ := editRegistry(t)
	var found bool
	for _, schema := range registry.Schemas() {
		if schema.Name != "edit" {
			continue
		}
		found = true
		properties, ok := schema.Parameters["properties"].(map[string]any)
		if !ok {
			t.Fatalf("edit schema properties = %T", schema.Parameters["properties"])
		}
		property, ok := properties["replace_all"].(map[string]any)
		if !ok {
			t.Fatalf("replace_all property = %T", properties["replace_all"])
		}
		if property["type"] != "boolean" {
			t.Fatalf("replace_all type = %v", property["type"])
		}
		if required, ok := schema.Parameters["required"].([]string); ok {
			for _, name := range required {
				if name == "replace_all" {
					t.Fatal("replace_all must stay optional")
				}
			}
		}
		if !strings.Contains(schema.Description, "gutter") {
			t.Fatalf("description = %q, want the copy-paste contract", schema.Description)
		}
	}
	if !found {
		t.Fatal("edit schema not found")
	}
}
