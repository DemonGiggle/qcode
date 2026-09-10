package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestToolEnableDisable(t *testing.T) {
	registry, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	// Web tools require opt-in; other tools remain enabled by default.
	names := registry.ToolNames()
	if len(names) == 0 {
		t.Fatal("expected at least one tool")
	}
	for _, name := range names {
		want := name != "web_fetch" && name != "web_search"
		if registry.IsToolEnabled(name) != want {
			t.Fatalf("unexpected default for %q", name)
		}
	}

	// Disable a tool.
	first := "read"
	registry.DisableTool(first)
	if registry.IsToolEnabled(first) {
		t.Fatalf("tool %q should be disabled after DisableTool", first)
	}

	// EnabledSchemas should not include the disabled tool.
	schemas := registry.EnabledSchemas()
	for _, s := range schemas {
		if s.Name == first {
			t.Fatalf("disabled tool %q should not appear in EnabledSchemas", first)
		}
	}

	// Re-enable the tool.
	registry.EnableTool(first)
	if !registry.IsToolEnabled(first) {
		t.Fatalf("tool %q should be enabled after EnableTool", first)
	}
}

func TestToolDisableNonExistent(t *testing.T) {
	registry, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	// Disabling a non-existent tool should not panic or error.
	registry.DisableTool("nonexistent-tool")
	if registry.IsToolEnabled("nonexistent-tool") {
		t.Fatal("non-existent tool should remain disabled")
	}
}

func TestToolResetSessionRestoresDefaults(t *testing.T) {
	registry, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	names := registry.ToolNames()
	if len(names) == 0 {
		t.Fatal("expected at least one tool")
	}

	// Disable all tools.
	for _, name := range names {
		registry.DisableTool(name)
	}

	// Verify all are disabled.
	for _, name := range names {
		if registry.IsToolEnabled(name) {
			t.Fatalf("tool %q should be disabled before reset", name)
		}
	}

	// Reset session.
	registry.ResetSession()

	// Restore the original defaults, including disabled web tools.
	for _, name := range names {
		want := name != "web_fetch" && name != "web_search"
		if registry.IsToolEnabled(name) != want {
			t.Fatalf("unexpected reset default for %q", name)
		}
	}
}

func TestDisabledToolRefusesExecution(t *testing.T) {
	root := t.TempDir()
	registry, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sample.txt"), []byte("content\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A handler-dispatched tool must refuse to run while disabled.
	registry.DisableTool("read")
	if _, err := call(t, registry, "read", map[string]any{"path": "sample.txt"}); err == nil {
		t.Fatal("disabled read tool should refuse to execute")
	} else if !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("expected disabled-tool error, got %q", err.Error())
	}

	// A switch-dispatched tool must refuse to run while disabled.
	registry.DisableTool("write")
	if _, err := call(t, registry, "write", map[string]any{"path": "blocked.txt", "content": "nope"}); err == nil {
		t.Fatal("disabled write tool should refuse to execute")
	} else if !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("expected disabled-tool error, got %q", err.Error())
	}

	// Nothing should have been written.
	if _, statErr := os.Stat(filepath.Join(root, "blocked.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("disabled write tool must not create files: %v", statErr)
	}

	// Re-enabling restores execution for both dispatch paths.
	registry.EnableTool("read")
	if _, err := call(t, registry, "read", map[string]any{"path": "sample.txt"}); err != nil {
		t.Fatalf("re-enabled read tool should execute: %v", err)
	}
	registry.EnableTool("write")
	if _, err := call(t, registry, "write", map[string]any{"path": "restored.txt", "content": "written"}); err != nil {
		t.Fatalf("re-enabled write tool should execute: %v", err)
	}
}
