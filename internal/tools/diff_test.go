package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"qcode/internal/llm"
)

func detailedCall(t *testing.T, registry *Registry, name string, args any) ExecutionResult {
	t.Helper()
	data, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	result, err := registry.ExecuteDetailed(context.Background(), llm.ToolCall{Name: name, Arguments: data})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestWriteDetailedShowsNewFileAndOverwriteDiffs(t *testing.T) {
	registry, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	created := detailedCall(t, registry, "write", map[string]any{"path": "src/a.txt", "content": "alpha\nbeta\n"})
	for _, expected := range []string{"--- /dev/null", "+++ b/src/a.txt", "+alpha", "+beta"} {
		if !strings.Contains(created.Diff, expected) {
			t.Errorf("new-file diff missing %q:\n%s", expected, created.Diff)
		}
	}
	overwritten := detailedCall(t, registry, "write", map[string]any{"path": "src/a.txt", "content": "alpha\ngamma\n"})
	for _, expected := range []string{"--- a/src/a.txt", "-beta", "+gamma"} {
		if !strings.Contains(overwritten.Diff, expected) {
			t.Errorf("overwrite diff missing %q:\n%s", expected, overwritten.Diff)
		}
	}
}

func TestEditDetailedShowsContextAndUnchangedWriteHasNoDiff(t *testing.T) {
	registry, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	content := "one\ntwo\nthree\n"
	detailedCall(t, registry, "write", map[string]any{"path": "a.txt", "content": content})
	edited := detailedCall(t, registry, "edit", map[string]any{"path": "a.txt", "old_text": "two", "new_text": "second"})
	for _, expected := range []string{" one", "-two", "+second", " three"} {
		if !strings.Contains(edited.Diff, expected) {
			t.Errorf("edit diff missing %q:\n%s", expected, edited.Diff)
		}
	}
	unchanged := detailedCall(t, registry, "write", map[string]any{"path": "a.txt", "content": "one\nsecond\nthree\n"})
	if unchanged.Diff != "" {
		t.Fatalf("unchanged write diff:\n%s", unchanged.Diff)
	}
}

func TestUnifiedDiffIsBounded(t *testing.T) {
	before := make([]string, 300)
	after := make([]string, 300)
	for index := range before {
		before[index] = fmt.Sprintf("old %03d", index)
		after[index] = fmt.Sprintf("new %03d", index)
	}
	diff := unifiedDiff("large.txt", []byte(strings.Join(before, "\n")), []byte(strings.Join(after, "\n")), true)
	if !strings.Contains(diff, "diff truncated") {
		t.Fatalf("large diff was not truncated")
	}
	if lines := strings.Count(diff, "\n") + 1; lines > maxDiffLines+1 {
		t.Fatalf("diff lines = %d", lines)
	}
}

func TestUnifiedDiffDetectsFinalNewlineChange(t *testing.T) {
	diff := unifiedDiff("a.txt", []byte("same\n"), []byte("same"), true)
	for _, expected := range []string{"-same", "+same", "\\ No newline at end of file"} {
		if !strings.Contains(diff, expected) {
			t.Fatalf("final newline diff missing %q:\n%s", expected, diff)
		}
	}
}
