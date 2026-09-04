package main

import (
	"testing"

	"qcode/internal/config"
)

func TestApplyConfig(t *testing.T) {
	t.Setenv("QCODE_PROVIDER", "")
	t.Setenv("QCODE_MODEL", "")
	t.Setenv("QCODE_BASE_URL", "")
	t.Setenv("QCODE_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	opts := options{provider: "ollama", model: "default-model"}
	maxSteps := 48

	applyConfig(&opts, config.Config{
		Provider: "openai",
		Model:    "configured-model",
		BaseURL:  "https://example.test/v1",
		APIKey:   "configured-key",
		MaxSteps: &maxSteps,
	}, nil)

	if opts.provider != "openai" || opts.model != "configured-model" || opts.baseURL != "https://example.test/v1" || opts.apiKey != "configured-key" || opts.maxSteps != 48 {
		t.Fatalf("options = %+v, want config values", opts)
	}
}

func TestApplyConfigDoesNotOverrideFlagsOrEnvironment(t *testing.T) {
	t.Setenv("QCODE_PROVIDER", "env-provider")
	t.Setenv("QCODE_MODEL", "")
	t.Setenv("QCODE_BASE_URL", "")
	t.Setenv("QCODE_API_KEY", "env-key")
	t.Setenv("OPENAI_API_KEY", "")
	opts := options{
		provider: "env-provider",
		model:    "flag-model",
		baseURL:  "flag-url",
		apiKey:   "env-key",
		maxSteps: 64,
	}
	configuredMaxSteps := 48

	applyConfig(&opts, config.Config{
		Provider: "config-provider",
		Model:    "config-model",
		BaseURL:  "config-url",
		APIKey:   "config-key",
		MaxSteps: &configuredMaxSteps,
	}, map[string]bool{"model": true, "base-url": true, "max-steps": true})

	if opts.provider != "env-provider" || opts.model != "flag-model" || opts.baseURL != "flag-url" || opts.apiKey != "env-key" || opts.maxSteps != 64 {
		t.Fatalf("options = %+v, higher-precedence values were overwritten", opts)
	}
}

func TestApplyConfigDoesNotOverrideAPIKeyFlag(t *testing.T) {
	t.Setenv("QCODE_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	opts := options{apiKey: "flag-key"}

	applyConfig(&opts, config.Config{APIKey: "config-key"}, map[string]bool{"api-key": true})

	if opts.apiKey != "flag-key" {
		t.Fatalf("api key = %q, want flag value", opts.apiKey)
	}
}

func TestApplyConfigDoesNotMixSettingsFromAnotherProvider(t *testing.T) {
	t.Setenv("QCODE_PROVIDER", "")
	t.Setenv("QCODE_MODEL", "")
	t.Setenv("QCODE_BASE_URL", "")
	t.Setenv("QCODE_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	opts := options{
		provider: "opencode-go",
		model:    "default-model",
		maxSteps: 32,
	}
	configuredMaxSteps := 48

	applyConfig(&opts, config.Config{
		Provider: "ollama",
		Model:    "configured-model",
		BaseURL:  "http://ollama.test",
		APIKey:   "configured-key",
		MaxSteps: &configuredMaxSteps,
	}, map[string]bool{"provider": true})

	if opts.provider != "opencode-go" || opts.model != "default-model" || opts.baseURL != "" || opts.apiKey != "" {
		t.Fatalf("options = %+v, mixed provider-specific config", opts)
	}
	if opts.maxSteps != 48 {
		t.Fatalf("max steps = %d, want provider-independent config value", opts.maxSteps)
	}
}

func TestApplyConfigSandboxPrecedence(t *testing.T) {
	enabled := true
	opts := options{}
	applyConfig(&opts, config.Config{Sandbox: &enabled}, nil)
	if !opts.sandbox {
		t.Fatal("sandbox config was not applied")
	}

	opts.sandbox = false
	applyConfig(&opts, config.Config{Sandbox: &enabled}, map[string]bool{"sandbox": true})
	if opts.sandbox {
		t.Fatal("sandbox config overrode explicit --sandbox=false")
	}
}
