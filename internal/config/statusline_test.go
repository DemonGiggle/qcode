package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadStatuslineHiddenLayers(t *testing.T) {
	dir := t.TempDir()
	low := writeConfig(t, dir, "low.toml", "statusline_hidden = [\"tok\", \"step\"]\n")
	high := writeConfig(t, dir, "high.toml", "statusline_hidden = [\"ctx\"]\n")
	cfg, _, diagnostics := load([]string{high, low})
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %v", diagnostics)
	}
	if got, want := strings.Join(cfg.StatuslineHidden, ","), "ctx"; got != want {
		t.Fatalf("hidden = %q, want %q (override, not additive)", got, want)
	}
	empty := writeConfig(t, dir, "empty.toml", "statusline_hidden = []\n")
	emptyCfg, _, emptyDiags := load([]string{empty})
	if len(emptyDiags) != 0 || len(emptyCfg.StatuslineHidden) != 0 {
		t.Fatalf("empty hidden = (%+v, %v)", emptyCfg.StatuslineHidden, emptyDiags)
	}
}

func TestLoadRejectsUnknownStatuslineSegment(t *testing.T) {
	path := writeConfig(t, t.TempDir(), "config.toml", "statusline_hidden = [\"bogus\"]\n")
	_, _, diagnostics := load([]string{path})
	if len(diagnostics) != 1 || !strings.Contains(diagnostics[0].Error(), "statusline_hidden") {
		t.Fatalf("diagnostics = %v", diagnostics)
	}
}

func TestPersistStatuslineHiddenRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	original := "# keep this comment\nprovider = \"openai\"\nmodel = \"old-model\"\n\n[learning]\ncontext_budget = 1200\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	writer := newRuntimePreferenceWriter(path)
	if err := writer.PersistStatuslineHidden([]string{"tok", "step", "tok", " CTX "}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"# keep this comment", `provider = "openai"`, "[learning]", "statusline_hidden"} {
		if !strings.Contains(text, want) {
			t.Fatalf("config missing %q: %s", want, text)
		}
	}
	var parsed Config
	if err := loadAndDecodeForTest(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(parsed.StatuslineHidden, ","), "ctx,step,tok"; got != want {
		t.Fatalf("hidden = %q, want %q (canonical priority order)", got, want)
	}
	if err := writer.PersistStatuslineHidden(nil); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "statusline_hidden") {
		t.Fatalf("reset did not remove override: %s", data)
	}
}

func TestPersistStatuslineHiddenRejectsUnknown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := newRuntimePreferenceWriter(path).PersistStatuslineHidden([]string{"bogus"}); err == nil {
		t.Fatal("PersistStatuslineHidden succeeded for unknown segment")
	}
}
