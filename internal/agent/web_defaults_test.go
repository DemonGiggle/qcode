package agent

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"qcode/internal/llm"
	"qcode/internal/tools"
)

func TestSandboxWebSelectionsSurviveHelperCreationAndResume(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux sandbox shell fixture")
	}
	bwrap := filepath.Join(t.TempDir(), "bwrap")
	if err := os.WriteFile(bwrap, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, enabled := range []string{"none", "web_fetch", "web_search", "both"} {
		t.Run(enabled, func(t *testing.T) {
			root := t.TempDir()
			opts := tools.Options{Sandbox: true, BubblewrapPath: bwrap}
			manager, _ := newDirectoryManager(t, root, opts)
			main, _ := manager.Agent("main")
			fetch := enabled == "web_fetch" || enabled == "both"
			search := enabled == "web_search" || enabled == "both"
			main.ToggleTool("web_fetch", fetch)
			main.ToggleTool("web_search", search)
			childSummary, err := manager.CreateInherited("main", "model")
			if err != nil {
				t.Fatal(err)
			}
			child, _ := manager.Agent(childSummary.ID)
			checkpoint := *main.checkpoint.Load()
			restoredManager, _ := newDirectoryManager(t, root, opts)
			restored, _ := restoredManager.Agent("main")
			if err := restored.RestoreState(checkpoint); err != nil {
				t.Fatal(err)
			}
			for name, runner := range map[string]*Agent{"main": main, "helper": child, "resumed": restored} {
				if runner.ToolEnabled("web_fetch") != fetch || runner.ToolEnabled("web_search") != search {
					t.Fatalf("%s changed saved web selections", name)
				}
				result, err := runner.executeDetailed(context.Background(), llm.ToolCall{Name: "shell", Arguments: []byte(`{"command":"true"}`)})
				if err != nil {
					t.Fatal(err)
				}
				if isolated := strings.Contains(result.Output, "--unshare-net"); isolated == (fetch || search) {
					t.Fatalf("%s sandbox networking does not match selections: %s", name, result.Output)
				}
			}
			child.ToggleTool("web_fetch", !fetch)
			child.ToggleTool("web_search", !search)
			if main.ToolEnabled("web_fetch") != fetch || main.ToolEnabled("web_search") != search {
				t.Fatal("changing helper tools changed main's selections")
			}
		})
	}
}
