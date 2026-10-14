package tools

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestRestoreToolSettingsAndValidatedGrants(t *testing.T) {
	root, external := t.TempDir(), t.TempDir()
	original, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	original.DisableTool("web_fetch")
	original.addGrant(external)
	data := original.SaveTools()
	restored, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := restored.RestoreTools(data); err != nil {
		t.Fatal(err)
	}
	if restored.IsToolEnabled("web_fetch") || !restored.IsToolEnabled("web_search") || !restored.isGranted(external) {
		t.Fatal("settings not restored")
	}
	var saved savedTools
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	saved.Grants = append(saved.Grants, filepath.Join(external, "missing"))
	data, _ = json.Marshal(saved)
	if err := restored.RestoreTools(data); err != nil {
		t.Fatal(err)
	}
	if len(restored.RestoreWarnings()) != 1 || !restored.isGranted(external) {
		t.Fatal("invalid grant did not produce an isolated warning")
	}
}

func TestRestoreWebSelectionsControlsSandboxNetworking(t *testing.T) {
	for _, selection := range []struct {
		name          string
		fetch, search bool
	}{
		{"both disabled", false, false},
		{"fetch enabled", true, false},
		{"search enabled", false, true},
		{"both enabled", true, true},
	} {
		t.Run(selection.name, func(t *testing.T) {
			root := t.TempDir()
			original, err := NewWithOptions(root, Options{Sandbox: true, BubblewrapPath: "/unused/bwrap"})
			if err != nil {
				t.Fatal(err)
			}
			if selection.fetch {
				original.EnableTool("web_fetch")
			}
			if selection.search {
				original.EnableTool("web_search")
			}
			data := original.SaveTools()
			for _, selectionsOnly := range []bool{false, true} {
				restored, err := NewWithOptions(root, Options{Sandbox: true, BubblewrapPath: "/unused/bwrap"})
				if err != nil {
					t.Fatal(err)
				}
				restore := restored.RestoreTools
				if selectionsOnly {
					restore = restored.RestoreToolSelections
				}
				if err := restore(data); err != nil {
					t.Fatal(err)
				}
				if restored.IsToolEnabled("web_fetch") != selection.fetch || restored.IsToolEnabled("web_search") != selection.search {
					t.Fatalf("saved selections changed after restore (selections only = %v)", selectionsOnly)
				}
				wantNetwork := selection.fetch || selection.search
				args := restored.sandbox.arguments(root, restored.grantPaths(), "/bin/true")
				if restored.sandbox.allowNetwork != wantNetwork || strings.Contains(strings.Join(args, " "), "--unshare-net") == wantNetwork {
					t.Fatalf("restored sandbox networking does not match saved selections: %q", args)
				}
			}
		})
	}
}
