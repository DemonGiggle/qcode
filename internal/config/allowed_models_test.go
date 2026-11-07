package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAllowedAgentModelsRoundTrip(t *testing.T) {
	content := "[[agents.allow_models]]\nprovider = \"openai\"\nmodel = \"gpt-5\"\nrole = \"smart\"\n\n[[agents.allow_models]]\nprovider = \"openai\"\nmodel = \"gpt-5-mini\"\nrole = \"balanced\"\n"
	cfg, err := decodeConfig([]byte(content), true)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(cfg.Agents.AllowModels) != 2 {
		t.Fatalf("decoded %d entries: %+v", len(cfg.Agents.AllowModels), cfg.Agents.AllowModels)
	}
	norm := cfg.NormalizedAllowedAgentModels()
	if len(norm) != 2 || norm[0].Model != "gpt-5" || norm[1].Model != "gpt-5-mini" {
		t.Fatalf("normalized = %+v", norm)
	}
}

func TestAllowedAgentModelsStaleThinkingIgnored(t *testing.T) {
	content := "[[agents.allow_models]]\nprovider = \"openai\"\nmodel = \"gpt-5\"\nrole = \"smart\"\nthinking_level = \"high\"\n"
	cfg, err := decodeConfig([]byte(content), true)
	if err != nil {
		t.Fatalf("decode with stale thinking_level must not fail: %v", err)
	}
	norm := cfg.NormalizedAllowedAgentModels()
	if len(norm) != 1 || norm[0].ThinkingLevel != "" {
		t.Fatalf("thinking_level must be ignored, got %+v", norm)
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	issues := CheckPaths([]string{path})
	foundThinkingWarn := false
	for _, is := range issues {
		if strings.Contains(is.Message, "thinking_level is ignored") {
			foundThinkingWarn = true
		}
		if is.Severity == SeverityError {
			t.Fatalf("stale entry must not error startup: %+v", is)
		}
	}
	if !foundThinkingWarn {
		t.Fatalf("expected thinking_level ignored warning, got %+v", issues)
	}
}

func TestAllowedAgentModelsValidationWarns(t *testing.T) {
	content := "[[agents.allow_models]]\nprovider = \"openai\"\nmodel = \"\"\nrole = \"smart\"\n\n[[agents.allow_models]]\nprovider = \"openai\"\nmodel = \"gpt-5\"\nrole = \"genius\"\n\n[[agents.allow_models]]\nprovider = \"nope\"\nmodel = \"x\"\nrole = \"cheap\"\n"
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	issues := CheckPaths([]string{path})
	if len(issues) == 0 {
		t.Fatal("expected warnings for bad roster")
	}
	for _, is := range issues {
		if is.Severity != SeverityWarning {
			t.Fatalf("roster issues must warn, not error: %+v", is)
		}
	}
	// Load must still succeed and drop invalid rows.
	cfg, err := decodeConfig([]byte(content), true)
	if err != nil {
		t.Fatalf("decode must not fail: %v", err)
	}
	if len(cfg.NormalizedAllowedAgentModels()) != 0 {
		t.Fatalf("invalid rows must be dropped, got %+v", cfg.NormalizedAllowedAgentModels())
	}
}

func TestAllowedAgentModelsMergeOverride(t *testing.T) {
	var dst Config
	low := Config{}
	low.Agents.AllowModels = []AllowedAgentModel{{Provider: "openai", Model: "old", Role: "cheap"}}
	merge(&dst, low)
	if len(dst.Agents.AllowModels) != 1 {
		t.Fatalf("low layer merge = %+v", dst.Agents.AllowModels)
	}
	high := Config{}
	high.Agents.AllowModels = []AllowedAgentModel{}
	merge(&dst, high)
	if dst.Agents.AllowModels == nil || len(dst.Agents.AllowModels) != 0 {
		t.Fatalf("explicit empty must clear, got %+v", dst.Agents.AllowModels)
	}
	// Missing key inherits.
	var dst2 Config
	merge(&dst2, low)
	merge(&dst2, Config{})
	if len(dst2.Agents.AllowModels) != 1 {
		t.Fatalf("missing key must inherit, got %+v", dst2.Agents.AllowModels)
	}
}

func TestPersistAllowedAgentModelsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	w := newRuntimePreferenceWriter(path)
	entries := []AllowedAgentModel{
		{Provider: "openai", Model: "gpt-5", Role: "smart"},
		{Provider: "openai", Model: "gpt-5-mini", Role: "balanced"},
	}
	if err := w.PersistAllowedAgentModels(entries); err != nil {
		t.Fatalf("persist: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "[[agents.allow_models]]") {
		t.Fatalf("persist must write array tables: %q", text)
	}
	cfg, err := decodeConfig(data, true)
	if err != nil {
		t.Fatalf("decode persisted: %v", err)
	}
	norm := cfg.NormalizedAllowedAgentModels()
	if len(norm) != 2 {
		t.Fatalf("round-trip got %+v", norm)
	}
	// Clearing removes blocks.
	if err := w.PersistAllowedAgentModels(nil); err != nil {
		t.Fatalf("clear: %v", err)
	}
	data, _ = os.ReadFile(path)
	if strings.Contains(string(data), "allow_models") {
		t.Fatalf("clear must remove roster: %q", string(data))
	}
	// Thinking must be rejected.
	if err := w.PersistAllowedAgentModels([]AllowedAgentModel{{Provider: "openai", Model: "m", Role: "smart", ThinkingLevel: "high"}}); err == nil {
		t.Fatal("persist with thinking_level must fail")
	}
	// Invalid role must fail, not silently persist subset.
	if err := w.PersistAllowedAgentModels([]AllowedAgentModel{{Provider: "openai", Model: "m", Role: "genius"}}); err == nil {
		t.Fatal("persist with bad role must fail")
	}
}
