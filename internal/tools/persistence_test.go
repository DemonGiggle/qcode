package tools

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestRestoreToolSettingsAndValidatedGrants(t *testing.T) {
	root, external := t.TempDir(), t.TempDir()
	original, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	original.EnableTool("web_search")
	original.addGrant(external)
	data := original.SaveTools()
	restored, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := restored.RestoreTools(data); err != nil {
		t.Fatal(err)
	}
	if !restored.IsToolEnabled("web_search") || !restored.isGranted(external) {
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
