package main

import (
	"testing"

	"qcode/internal/config"
)

func TestApplyConfig(t *testing.T) {
	t.Setenv("QCODE_PROVIDER", "")
	t.Setenv("QCODE_MODEL", "")
	t.Setenv("QCODE_BASE_URL", "")
	opts := options{provider: "ollama", model: "default-model"}
	maxSteps := 48

	applyConfig(&opts, config.Config{
		Provider: "openai-like",
		Model:    "configured-model",
		BaseURL:  "https://example.test/v1",
		MaxSteps: &maxSteps,
	}, nil)

	if opts.provider != "openai-like" || opts.model != "configured-model" || opts.baseURL != "https://example.test/v1" || opts.maxSteps != 48 {
		t.Fatalf("options = %+v, want config values", opts)
	}
}

func TestApplyConfigDoesNotOverrideFlagsOrEnvironment(t *testing.T) {
	t.Setenv("QCODE_PROVIDER", "env-provider")
	t.Setenv("QCODE_MODEL", "")
	t.Setenv("QCODE_BASE_URL", "")
	opts := options{
		provider: "env-provider",
		model:    "flag-model",
		baseURL:  "flag-url",
		maxSteps: 64,
	}
	configuredMaxSteps := 48

	applyConfig(&opts, config.Config{
		Provider: "config-provider",
		Model:    "config-model",
		BaseURL:  "config-url",
		MaxSteps: &configuredMaxSteps,
	}, map[string]bool{"model": true, "base-url": true, "max-steps": true})

	if opts.provider != "env-provider" || opts.model != "flag-model" || opts.baseURL != "flag-url" || opts.maxSteps != 64 {
		t.Fatalf("options = %+v, higher-precedence values were overwritten", opts)
	}
}
