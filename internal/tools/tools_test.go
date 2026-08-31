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

func call(t *testing.T, registry *Registry, name string, args any) (string, error) {
	t.Helper()
	data, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return registry.Execute(context.Background(), llm.ToolCall{Name: name, Arguments: data})
}

func TestFileToolWorkflow(t *testing.T) {
	root := t.TempDir()
	registry, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := call(t, registry, "write", map[string]any{"path": "src/a.txt", "content": "alpha\nbeta\n"}); err != nil {
		t.Fatal(err)
	}
	if _, err := call(t, registry, "edit", map[string]any{"path": "src/a.txt", "old_text": "beta", "new_text": "gamma"}); err != nil {
		t.Fatal(err)
	}
	result, err := call(t, registry, "read", map[string]any{"path": "src/a.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "gamma") {
		t.Fatalf("read result = %q", result)
	}
	result, err = call(t, registry, "search", map[string]any{"path": ".", "pattern": "gam+"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, filepath.Join("src", "a.txt")+":2:gamma") {
		t.Fatalf("search result = %q", result)
	}
	data, err := os.ReadFile(filepath.Join(root, "src", "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "alpha\ngamma\n" {
		t.Fatalf("file = %q", data)
	}
}

func TestFileToolsRejectParentEscape(t *testing.T) {
	registry, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, err = call(t, registry, "read", map[string]any{"path": "../secret"})
	if err == nil || !strings.Contains(err.Error(), "outside workspace") {
		t.Fatalf("error = %v", err)
	}
}

func TestShellCapturesOutput(t *testing.T) {
	registry, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	result, err := call(t, registry, "shell", map[string]any{"command": "printf qcode"})
	if err != nil {
		t.Fatal(err)
	}
	if result != "qcode" {
		t.Fatalf("result = %q", result)
	}
}
