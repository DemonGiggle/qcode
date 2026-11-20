package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProviderKind(t *testing.T) {
	for _, tc := range []struct {
		name, kind, want string
		setType          bool
	}{
		{"ollama", "", "ollama", false},
		{"gemini", "", "gemini", false},
		{"lab", "ollama", "ollama", true},
		{"second", "openai", "openai", true},
		{"lab", "", "", false},
		{"lab", "", "", true},
		{"lab", "other-instance", "", true},
		{"ollama", "openai", "", true},
		{"custom", "ollama", "", true},
		{"bad.name", "ollama", "", true},
		{"Bad", "ollama", "", true},
	} {
		t.Run(tc.name+"/"+tc.kind, func(t *testing.T) {
			p := ProviderProfile{}
			if tc.setType {
				p.Type = stringPointer(tc.kind)
			}
			cfg := Config{Providers: map[string]ProviderProfile{tc.name: p}}
			got, err := cfg.ProviderKind(tc.name)
			if (err != nil) != (tc.want == "") || got != tc.want {
				t.Fatalf("ProviderKind = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestInstanceTypeLayering(t *testing.T) {
	low, err := decodeConfig([]byte("[providers.lab]\ntype = 'ollama'\nbase_url = 'http://lab'\n"), true)
	if err != nil {
		t.Fatal(err)
	}
	high, err := decodeConfig([]byte("[providers.lab]\nmodel = 'coder'\n"), true)
	if err != nil {
		t.Fatal(err)
	}
	merge(&low, high)
	if kind, err := low.ProviderKind("lab"); err != nil || kind != "ollama" {
		t.Fatalf("merged kind = %q, %v", kind, err)
	}
	if *low.Providers["lab"].BaseURL != "http://lab" || *low.Providers["lab"].Model != "coder" {
		t.Fatal("lost profile fields")
	}
}

func TestInstanceInheritanceAcrossFiles(t *testing.T) {
	dir := t.TempDir()
	low := writeConfig(t, dir, "low.toml", "[providers.ollama]\nbase_url = 'http://local'\nmodel = 'base-model'\nthinking = 'high'\napi_key = 'parent-secret'\n[providers.lab]\ntype = 'ollama'\n")
	high := writeConfig(t, dir, "high.toml", "[providers.lab]\nbase_url = 'http://lab'\nthinking = ''\napi_key = ''\n")
	cfg, _, diagnostics := load([]string{high, low})
	if len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	base, key := cfg.ProviderConnection("lab")
	if base != "http://lab" || key != "" {
		t.Fatalf("connection = %q, %q", base, key)
	}
	model, thinking, ok := cfg.ProviderModel("lab")
	if !ok || model != "base-model" || thinking != "" {
		t.Fatalf("model = %q, thinking = %q, ok = %v", model, thinking, ok)
	}
	for _, issue := range CheckPaths([]string{high, low}) {
		if issue.Severity == SeverityError {
			t.Fatal(issue)
		}
	}
}

func TestInstanceRejectsChatGPTCredentials(t *testing.T) {
	dir := t.TempDir()
	low := writeConfig(t, dir, "low.toml", "[providers.work]\ntype = 'chatgpt'\n")
	high := writeConfig(t, dir, "high.toml", "[providers.work]\napi_key = 'forbidden'\n")
	_, _, diagnostics := load([]string{high, low})
	if len(diagnostics) == 0 {
		t.Fatal("accepted API key on ChatGPT instance")
	}
	found := false
	for _, issue := range CheckPaths([]string{high, low}) {
		if issue.Severity == SeverityError {
			found = true
		}
	}
	if !found {
		t.Fatal("config check accepted API key on ChatGPT instance")
	}
}

func TestInstancePreferencesAndRosterRoundTrip(t *testing.T) {
	dir := t.TempDir()
	low := writeConfig(t, dir, "base.toml", "[providers.ollama]\nmodel = 'parent'\nthinking = 'high'\n[providers.lab]\ntype = 'ollama'\n")
	high := writeConfig(t, dir, "user.toml", "")
	cfg, _, diagnostics := load([]string{high, low})
	if len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	writer := newRuntimePreferenceWriter(high)
	writer.SetLegacyConfig(cfg)
	if err := writer.PersistProvider("lab", "lab-model", ""); err != nil {
		t.Fatal(err)
	}
	if err := writer.PersistAllowedAgentModels([]AllowedAgentModel{{Provider: "lab", Model: "lab-model", Role: AgentRoleSmart}}); err != nil {
		t.Fatal(err)
	}
	restored, _, diagnostics := load([]string{high, low})
	if len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	if restored.Provider != "lab" {
		t.Fatal(restored.Provider)
	}
	model, thinking, ok := restored.ProviderModel("lab")
	if !ok || model != "lab-model" || thinking != "" {
		t.Fatalf("restored = %q %q %v", model, thinking, ok)
	}
	parent, _, _ := restored.ProviderModel("ollama")
	if parent != "parent" {
		t.Fatal("instance save changed parent")
	}
	roster := restored.NormalizedAllowedAgentModels()
	if len(roster) != 1 || roster[0].Provider != "lab" {
		t.Fatal(roster)
	}
	if err := writer.PersistProvider("unknown", "model", ""); err == nil {
		t.Fatal("accepted unknown instance")
	}
}

func TestInheritedInstanceKeyPathUsesDeclaringDirectory(t *testing.T) {
	parentDir, instanceDir := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(parentDir, "provider.key"), []byte("parent-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(instanceDir, "provider.key"), []byte("wrong-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	low := writeConfig(t, parentDir, "config.toml", "[providers.openai]\napi_key_path = 'provider.key'\n")
	high := writeConfig(t, instanceDir, "config.toml", "[providers.work]\ntype = 'openai'\n")
	cfg, _, diagnostics := load([]string{high, low})
	if len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	_, key := cfg.ProviderConnection("work")
	if key != "parent-secret" {
		t.Fatal("inherited key resolved relative to instance instead of declaring file")
	}
	if cfg.Providers["work"].Type == nil || *cfg.Providers["work"].Type != "openai" {
		t.Fatal("key resolution lost type")
	}
	for _, issue := range CheckPaths([]string{high, low}) {
		if issue.Severity == SeverityError {
			t.Fatal(issue)
		}
	}
}

func TestOllamaCloudProfileInheritance(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "cloud.key"), []byte("fixture-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := writeConfig(t, dir, "config.toml", `provider = "company-cloud"
[providers.ollama-cloud]
api_key_path = "cloud.key"
model = "Gemma:31b"
[providers.company-cloud]
type = "ollama-cloud"
base_url = "https://proxy.test/ollama/api"
`)
	cfg, _, problems := load([]string{path})
	if len(problems) != 0 {
		t.Fatal(problems)
	}
	if kind, err := cfg.ProviderKind(cfg.Provider); err != nil || kind != "ollama-cloud" {
		t.Fatalf("kind = %q, %v", kind, err)
	}
	if base, key := cfg.ProviderConnection(cfg.Provider); base != "https://proxy.test/ollama/api" || key != "fixture-key" {
		t.Fatal("wrong inherited profile connection")
	}
	if model, _ := cfg.EffectiveModel(cfg.Provider); model != "Gemma:31b" {
		t.Fatalf("inherited model = %q", model)
	}
	for _, issue := range CheckPaths([]string{path}) {
		if issue.Severity == SeverityError {
			t.Fatal(issue)
		}
	}
}
