package config

import "testing"

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
