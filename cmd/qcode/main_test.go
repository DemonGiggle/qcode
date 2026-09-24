package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"qcode/internal/agent"
	"qcode/internal/config"
	"qcode/internal/llm"
	"qcode/internal/prompt"
	"qcode/internal/skills"
	"qcode/internal/tools"
)

type autoloadProvider struct{ request llm.Request }

func (*autoloadProvider) Name() string { return "autoload-test" }
func (p *autoloadProvider) Complete(_ context.Context, request llm.Request, _ llm.StreamCallback) (llm.Response, error) {
	p.request = request
	return llm.Response{Message: llm.Message{Role: "assistant", Content: "done"}}, nil
}

func TestAutoloadSkillDataAndOneShotPrompt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	for path, content := range map[string]string{
		"manual/manual/SKILL.md":    "# Manual only\nNever autoload this.",
		"manual/optional/SKILL.md":  "# Optional\nSelect this manually.",
		"automatic/review/SKILL.md": "---\ndescription: Review code\n---\nSecret body instructions",
		"automatic/manual/SKILL.md": "# Automatic override\nBody instructions",
	} {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	paths := []string{"manual"}
	auto := []string{"automatic", "missing"}
	summaries, err := autoloadSkillData(root, paths, auto)
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 2 || summaries[0].Name != "manual" || summaries[0].Description != "Automatic override" || summaries[1].Name != "review" {
		t.Fatalf("autoload summaries = %#v", summaries)
	}
	selection := skills.NewLazySelection(root, append(paths, auto...)...)
	selection.Set(skillNames(summaries))
	if _, err := selection.Load("optional"); err == nil {
		t.Fatal("non-autoloaded skill was enabled")
	}
	if body, err := selection.Load("review"); err != nil || !strings.Contains(body, "Secret body instructions") {
		t.Fatalf("autoloaded skill = %q, %v", body, err)
	}
	registry, err := tools.NewWithOptions(root, tools.Options{Skills: selection})
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	defer stdout.Close()
	stderr, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	defer stderr.Close()
	provider := &autoloadProvider{}
	if err := runOneShot(context.Background(), "hello", provider, "test-model", "", registry, stderr, stdout, false, 3, prompt.System, nil, 0, summaries); err != nil {
		t.Fatal(err)
	}
	system := provider.request.Messages[0].Content
	if !strings.Contains(system, "Review code") || !strings.Contains(system, "Automatic override") || strings.Contains(system, "Secret body instructions") {
		t.Fatalf("unexpected initial skill context: %s", system)
	}
}

func TestAgentTimeoutConfigPrecedence(t *testing.T) {
	opts := options{agentTimeout: agent.DefaultConsultationTimeout}
	applyConfig(&opts, config.Config{}, nil)
	if opts.agentTimeout != 5*time.Minute {
		t.Fatal("missing config changed the default")
	}
	configured := "90s"
	applyConfig(&opts, config.Config{AgentTimeout: &configured}, nil)
	if opts.agentTimeout != 90*time.Second {
		t.Fatal(opts.agentTimeout)
	}
	opts.agentTimeout = time.Minute
	applyConfig(&opts, config.Config{AgentTimeout: &configured}, map[string]bool{"agent-timeout": true})
	if opts.agentTimeout != time.Minute {
		t.Fatal("config overrode explicit flag")
	}
}

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

func TestContextWindowConfigPrecedence(t *testing.T) {
	t.Setenv("QCODE_PROVIDER", "")
	t.Setenv("QCODE_MODEL", "")
	capacity := 8192
	cfg := config.Config{Provider: "ollama", Model: "test", ContextWindow: &capacity}
	for _, tc := range []struct {
		name  string
		opts  options
		flags map[string]bool
		want  int
	}{
		{"config", options{}, nil, 8192},
		{"flag", options{contextWindow: 4096}, map[string]bool{"context-window": true}, 4096},
		{"automatic flag", options{}, map[string]bool{"context-window": true}, 0},
		{"different model", options{model: "other"}, map[string]bool{"model": true}, 0},
		{"different provider", options{provider: "openai"}, map[string]bool{"provider": true}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			applyConfig(&tc.opts, cfg, tc.flags)
			if tc.opts.contextWindow != tc.want {
				t.Fatalf("got %d want %d", tc.opts.contextWindow, tc.want)
			}
		})
	}
}

func TestAutoCompactConfigPrecedence(t *testing.T) {
	threshold := 70
	disabled := true
	cfg := config.Config{AutoCompactThreshold: &threshold, DisableAutoCompact: &disabled}
	opts := options{autoCompactThreshold: agent.DefaultAutoCompactThreshold}
	applyConfig(&opts, cfg, nil)
	if opts.autoCompactThreshold != 70 || !opts.disableAutoCompact {
		t.Fatalf("config = %+v", opts)
	}
	opts = options{autoCompactThreshold: agent.DefaultAutoCompactThreshold, disableAutoCompact: false}
	applyConfig(&opts, cfg, map[string]bool{"disable-auto-compact": true})
	if opts.disableAutoCompact {
		t.Fatal("explicit flag was overridden by config")
	}
}

func TestSearchBackendConfiguration(t *testing.T) {
	opts := options{provider: "ollama"}
	cfg := config.Config{Provider: "openai", WebSearch: config.WebSearch{Backend: "duckduckgo"}}
	applyConfig(&opts, cfg, map[string]bool{"provider": true})
	if opts.searchBackend != "duckduckgo" {
		t.Fatalf("backend = %q", opts.searchBackend)
	}
}

func TestLearningConfiguration(t *testing.T) {
	opts := options{learningBudget: 1200}
	applyConfig(&opts, config.Config{}, nil)
	if opts.learningBudget != 1200 {
		t.Fatal("missing configuration changed default")
	}
	zero := 0
	applyConfig(&opts, config.Config{Learning: config.Learning{ContextBudget: &zero}}, nil)
	if opts.learningBudget != 0 {
		t.Fatal("zero budget not applied")
	}
}

func TestSandboxCommandPathsConfigPrecedence(t *testing.T) {
	opts := options{}
	applyConfig(&opts, config.Config{SandboxCommandPaths: []string{"/opt/a", "/opt/b"}}, nil)
	if len(opts.sandboxCommandPaths) != 2 {
		t.Fatalf("config paths not applied: %q", opts.sandboxCommandPaths)
	}
	// Explicit flag wins over configuration.
	opts = options{sandboxCommandPaths: []string{"/flag/tools"}}
	applyConfig(&opts, config.Config{SandboxCommandPaths: []string{"/opt/a"}}, map[string]bool{"sandbox-command-path": true})
	if len(opts.sandboxCommandPaths) != 1 || opts.sandboxCommandPaths[0] != "/flag/tools" {
		t.Fatalf("flag did not override config: %q", opts.sandboxCommandPaths)
	}
	// Absent key leaves existing (flag) value alone and remains valid.
	opts = options{}
	applyConfig(&opts, config.Config{}, nil)
	if opts.sandboxCommandPaths != nil {
		t.Fatalf("missing key changed default: %q", opts.sandboxCommandPaths)
	}
}
