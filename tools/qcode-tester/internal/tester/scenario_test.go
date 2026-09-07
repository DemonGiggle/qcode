package tester

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadScenariosRejectsInvalidWorkspacePath(t *testing.T) {
	directory := t.TempDir()
	scenarioDirectory := filepath.Join(directory, "escape")
	if err := os.MkdirAll(scenarioDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	content := `{"version":1,"name":"escape","prompt":"test","timeout":"1s","seed_files":{"../escape":"bad"}}`
	if err := os.WriteFile(filepath.Join(scenarioDirectory, "scenario.json"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := loadScenarios(directory, nil)
	if err == nil || !strings.Contains(err.Error(), "outside workspace") {
		t.Fatalf("load error = %v", err)
	}
}

func TestWriteManifest(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := writeManifest(path, root); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var entries []manifestEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Path != "note.txt" || entries[0].Size != 5 || entries[0].SHA256 != "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824" {
		t.Fatalf("manifest = %#v", entries)
	}
}
