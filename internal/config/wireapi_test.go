package config

import (
	"strings"
	"testing"
)

func TestWireAPIAcceptedForOpenAI(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, "openai.toml", "[providers.openai]\nwire_api = 'responses'\n")
	cfg, _, diagnostics := load([]string{path})
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %v", diagnostics)
	}
	if got := cfg.ProviderWireAPI("openai"); got != WireAPIResponses {
		t.Errorf("ProviderWireAPI = %q", got)
	}
}

func TestWireAPIRejectsUnknownValue(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, "bad.toml", "[providers.openai]\nwire_api = 'stream'\n")
	_, _, diagnostics := load([]string{path})
	if len(diagnostics) == 0 {
		t.Fatal("expected diagnostics")
	}
	if !strings.Contains(diagnostics[0].Error(), "wire_api") {
		t.Fatalf("diagnostic = %v", diagnostics[0])
	}
}

func TestWireAPIRejectsNonOpenAIKind(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, "gemini.toml", "[providers.gemini]\nwire_api = 'responses'\n")
	_, _, diagnostics := load([]string{path})
	if len(diagnostics) == 0 {
		t.Fatal("expected diagnostics")
	}
	if !strings.Contains(diagnostics[0].Error(), "only supported by the openai provider kind") {
		t.Fatalf("diagnostic = %v", diagnostics[0])
	}
}

func TestWireAPIAllowsEmptyAndIsCaseInsensitive(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, "case.toml", "[providers.openai]\nwire_api = 'RESPONSES'\n")
	cfg, _, diagnostics := load([]string{path})
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %v", diagnostics)
	}
	if got := cfg.ProviderWireAPI("openai"); got != WireAPIResponses {
		t.Fatalf("ProviderWireAPI = %q", got)
	}

	empty := writeConfig(t, dir, "empty.toml", "[providers.openai]\nwire_api = ''\n")
	cfg, _, diagnostics = load([]string{empty})
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %v", diagnostics)
	}
	if got := cfg.ProviderWireAPI("openai"); got != "" {
		t.Fatalf("ProviderWireAPI = %q", got)
	}
}

func TestWireAPIInstanceInheritanceAndOverride(t *testing.T) {
	dir := t.TempDir()
	low := writeConfig(t, dir, "low.toml", "[providers.openai]\nwire_api = 'responses'\n[providers.lab]\ntype = 'openai'\n")
	high := writeConfig(t, dir, "high.toml", "")
	cfg, _, diagnostics := load([]string{high, low})
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %v", diagnostics)
	}
	if got := cfg.ProviderWireAPI("lab"); got != WireAPIResponses {
		t.Fatalf("inherited wire_api = %q", got)
	}
	// An explicit instance value wins over the inherited one.
	high = writeConfig(t, dir, "high.toml", "[providers.lab]\nwire_api = 'chat_completions'\n")
	cfg, _, diagnostics = load([]string{high, low})
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %v", diagnostics)
	}
	if got := cfg.ProviderWireAPI("lab"); got != WireAPIChatCompletions {
		t.Fatalf("overridden wire_api = %q", got)
	}
}

func TestWireAPIAllowedOnCustomOpenAIInstance(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, "lab.toml", "[providers.lab]\ntype = 'openai'\nwire_api = 'responses'\nbase_url = 'http://localhost:8080/v1'\n")
	cfg, _, diagnostics := load([]string{path})
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %v", diagnostics)
	}
	if got := cfg.ProviderWireAPI("lab"); got != WireAPIResponses {
		t.Fatalf("ProviderWireAPI = %q", got)
	}
}

func TestCheckReportsWireAPIOnNonOpenAIKind(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, "gemini.toml", "[providers.gemini]\nwire_api = 'responses'\n")
	found := false
	for _, issue := range CheckPaths([]string{path}) {
		if strings.Contains(issue.Message, "wire_api") {
			found = true
		}
	}
	if !found {
		t.Fatal("config check did not report wire_api on non-openai provider")
	}
}
