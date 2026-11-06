package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeKeyFile(t *testing.T, content string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.key")
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	// os.WriteFile respects umask; enforce exact mode.
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAPIKeyPathResolvesIntoConnection(t *testing.T) {
	keyPath := writeKeyFile(t, "file-secret\n", 0600)
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	text := "[providers.openai]\napi_key_path = '" + strings.ReplaceAll(keyPath, "'", "") + "'\n"
	if err := os.WriteFile(cfgPath, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, _, diagnostics := load([]string{cfgPath})
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %v", diagnostics)
	}
	if _, key := cfg.ProviderConnection("openai"); key != "file-secret" {
		t.Fatalf("key = %q, want file-secret", key)
	}
	found := false
	for _, secret := range cfg.Secrets() {
		if secret == "file-secret" {
			found = true
		}
	}
	if !found {
		t.Fatalf("secrets missing file key: %v", cfg.Secrets())
	}
	if cfg.Providers["openai"].APIKeyPath == nil {
		t.Fatal("APIKeyPath lost after load")
	}
}

func TestAPIKeyPathBothSetIsFatal(t *testing.T) {
	path := writeConfig(t, t.TempDir(), "config.toml", "[providers.openai]\napi_key = 'x'\napi_key_path = '/tmp/x'\n")
	_, _, diagnostics := load([]string{path})
	if len(diagnostics) != 1 || !strings.Contains(diagnostics[0].Error(), "both api_key and api_key_path") {
		t.Fatalf("diagnostics = %v", diagnostics)
	}
	if !errors.Is(diagnostics[0], errFatalProviderConfig) {
		t.Fatal("expected fatal provider config error")
	}
}

func TestAPIKeyPathBadPermissionsWarnsAndClears(t *testing.T) {
	keyPath := writeKeyFile(t, "secret", 0644)
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(cfgPath, []byte("[providers.openai]\napi_key_path = '"+keyPath+"'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, _, diagnostics := load([]string{cfgPath})
	if len(diagnostics) != 1 || !strings.Contains(diagnostics[0].Error(), "api_key_path") {
		t.Fatalf("diagnostics = %v", diagnostics)
	}
	if _, key := cfg.ProviderConnection("openai"); key != "" {
		t.Fatalf("key = %q, want empty after bad perm", key)
	}
	issues := CheckPaths([]string{cfgPath})
	found := false
	for _, issue := range issues {
		if issue.Severity == SeverityError && issue.Key == "providers.openai.api_key_path" {
			found = true
			if strings.Contains(issue.Message, "secret") {
				t.Fatalf("message leaks secret: %q", issue.Message)
			}
		}
	}
	if !found {
		t.Fatalf("check issues = %v", issues)
	}
}

func TestAPIKeyPathMissingFile(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	missing := filepath.Join(t.TempDir(), "missing.key")
	if err := os.WriteFile(cfgPath, []byte("[providers.openai]\napi_key_path = '"+missing+"'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, _, diagnostics := load([]string{cfgPath})
	if len(diagnostics) != 1 || !strings.Contains(diagnostics[0].Error(), "not found") {
		t.Fatalf("diagnostics = %v", diagnostics)
	}
	issues := CheckPaths([]string{cfgPath})
	if !HasErrors(issues) {
		t.Fatalf("expected errors, got %v", issues)
	}
}

func TestChatGPTAPIKeyPathRejected(t *testing.T) {
	path := writeConfig(t, t.TempDir(), "config.toml", "[providers.chatgpt]\napi_key_path = '/tmp/x'\n")
	_, _, diagnostics := load([]string{path})
	if len(diagnostics) != 1 || !strings.Contains(diagnostics[0].Error(), "providers.chatgpt.api_key_path") {
		t.Fatalf("diagnostics = %v", diagnostics)
	}
}

func TestProfileModelPrecedence(t *testing.T) {
	cfg := Config{
		Provider: "openai",
		Model:    "root-model",
		Thinking: "low",
		ProviderModels: map[string]string{
			"openai": "remembered-model",
		},
		ProviderThinking: map[string]string{
			"openai": "medium",
		},
		Providers: map[string]ProviderProfile{
			"openai": {Model: stringPointer("profile-model"), Thinking: stringPointer("high")},
		},
	}
	if m, th, ok := cfg.ProviderModel("openai"); !ok || m != "profile-model" || th != "high" {
		t.Fatalf("profile = %q %q %v", m, th, ok)
	}
	if m, th := cfg.EffectiveModel("openai"); m != "profile-model" || th != "high" {
		t.Fatalf("effective = %q %q", m, th)
	}
	// Profile thinking alone applies without a profile model.
	cfg2 := Config{Providers: map[string]ProviderProfile{"openai": {Thinking: stringPointer("max")}}}
	if th, ok := cfg2.ProfileThinking("openai"); !ok || th != "max" {
		t.Fatalf("thinking = %q %v", th, ok)
	}
}
