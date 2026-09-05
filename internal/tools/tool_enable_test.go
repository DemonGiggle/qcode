package tools

import (
	"testing"
)

func TestToolEnableDisable(t *testing.T) {
	registry, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	// All tools should be enabled by default.
	names := registry.ToolNames()
	if len(names) == 0 {
		t.Fatal("expected at least one tool")
	}
	for _, name := range names {
		if !registry.IsToolEnabled(name) {
			t.Fatalf("tool %q should be enabled by default", name)
		}
	}

	// Disable a tool.
	first := names[0]
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

func TestToolResetSessionClearsDisabled(t *testing.T) {
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

	// All tools should be enabled again.
	for _, name := range names {
		if !registry.IsToolEnabled(name) {
			t.Fatalf("tool %q should be enabled after ResetSession", name)
		}
	}
}
