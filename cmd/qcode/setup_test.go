package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"qcode/internal/config"
	"qcode/internal/llm"
	"qcode/internal/redaction"
)

type fakeSetupStep struct {
	kind  string // "option", "model", "thinking", "theme", "prompt"
	reply string
	ok    bool
}

type fakeSetupUI struct {
	steps           []fakeSetupStep
	idx             int
	seenOptions     [][]string
	seenOptionNouns []string
}

func (f *fakeSetupUI) next(kind string) (string, bool, error) {
	if f.idx >= len(f.steps) {
		return "", false, errors.New("unexpected UI prompt: " + kind)
	}
	step := f.steps[f.idx]
	f.idx++
	if step.kind != kind {
		return "", false, errors.New("unexpected prompt kind: got " + kind + ", script has " + step.kind)
	}
	return step.reply, step.ok, nil
}

func (f *fakeSetupUI) SelectOption(noun string, options []string, disabled map[string]bool, current string, searchable bool) (string, bool, error) {
	f.seenOptionNouns = append(f.seenOptionNouns, noun)
	f.seenOptions = append(f.seenOptions, append([]string(nil), options...))
	return f.next("option")
}

func (f *fakeSetupUI) SelectModel(models []string, current string) (string, bool, error) {
	return f.next("model")
}

func (f *fakeSetupUI) SelectThinking(levels []string, current string) (string, bool, error) {
	return f.next("thinking")
}

func (f *fakeSetupUI) SelectTheme(current string) (string, bool, error) {
	return f.next("theme")
}

func (f *fakeSetupUI) PromptLine(label, defaultVal string, secret bool) (string, bool, error) {
	return f.next("prompt")
}

func TestSetupRequiresInteractiveTerminal(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := runSetupWithDeps(nil, strings.NewReader(""), &stdout, &stderr, false, defaultSetupDeps())
	if err == nil || !strings.Contains(err.Error(), "interactive terminal") {
		t.Fatalf("expected interactive terminal error, got %v", err)
	}
}

func TestSetupFreshOpenAIFlow(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	keyPath := filepath.Join(dir, "openai.key")

	ui := &fakeSetupUI{
		steps: []fakeSetupStep{
			{kind: "option", reply: "openai", ok: true},
			{kind: "prompt", reply: "sk-fresh-secret-999", ok: true},
			{kind: "prompt", reply: keyPath, ok: true},
			{kind: "option", reply: "Use default endpoint", ok: true},
			{kind: "model", reply: "gpt-5", ok: true},
			{kind: "thinking", reply: "high", ok: true},
			{kind: "option", reply: "Finish setup and save", ok: true},
		},
	}

	var stdout, stderr bytes.Buffer
	deps := setupDeps{
		newUI:       func(io.Reader, io.Writer) setupUI { return ui },
		loadConfig:  func() (config.Config, []string, []error, error) { return config.Config{}, nil, nil, nil },
		userCfgPath: func() (string, error) { return cfgPath, nil },
		newWriter: func() (*config.RuntimePreferenceWriter, error) {
			return config.NewRuntimePreferenceWriterForPath(cfgPath), nil
		},
		writeKeyFile: config.WriteAPIKeyFile,
		probeModels: func(ctx context.Context, cfg config.Config, provider, overrideBaseURL, overrideAPIKey string, policy *redaction.Policy) (setupProbeResult, error) {
			if provider != "openai" || overrideAPIKey != "sk-fresh-secret-999" {
				t.Fatalf("unexpected probe args: provider=%q key=%q", provider, overrideAPIKey)
			}
			return setupProbeResult{
				models: []string{"gpt-5", "gpt-5-mini"},
				thinkingCap: func(m string) llm.ThinkingCapability {
					return llm.ThinkingCapability{Supported: true, Adjustable: true, Levels: []string{"low", "medium", "high"}, Default: "medium"}
				},
			}, nil
		},
		checkConfig: func() ([]config.Issue, []string, error) {
			return nil, []string{cfgPath}, nil
		},
		goos: "linux",
	}

	if err := runSetupWithDeps(nil, strings.NewReader(""), &stdout, &stderr, true, deps); err != nil {
		t.Fatalf("runSetupWithDeps() error = %v", err)
	}
	if strings.Contains(stdout.String(), "sk-fresh-secret-999") {
		t.Fatalf("raw API key leaked in stdout:\n%s", stdout.String())
	}

	info, err := os.Lstat(keyPath)
	if err != nil {
		t.Fatalf("Lstat(%q) error = %v", keyPath, err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("key file perm = %04o, want 0600", perm)
	}

	cfgBytes, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", cfgPath, err)
	}
	cfgText := string(cfgBytes)
	if !strings.Contains(cfgText, `provider = "openai"`) || !strings.Contains(cfgText, `model = "gpt-5"`) || !strings.Contains(cfgText, `thinking = "high"`) {
		t.Fatalf("unexpected saved config:\n%s", cfgText)
	}
}

func TestSetupRerunConfiguredAndKeepOriginal(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	keyPath := filepath.Join(dir, "openai.key")
	if _, err := config.WriteAPIKeyFile(keyPath, "sk-existing"); err != nil {
		t.Fatalf("WriteAPIKeyFile() error = %v", err)
	}
	initial := "provider = \"openai\"\n\n[providers.openai]\napi_key_path = \"" + keyPath + "\"\nmodel = \"gpt-5\"\nthinking = \"medium\"\n"
	if err := os.WriteFile(cfgPath, []byte(initial), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	existingCfg := config.Config{
		Provider: "openai",
		Providers: map[string]config.ProviderProfile{
			"openai": {
				APIKeyPath: strPtr(keyPath),
				Model:      strPtr("gpt-5"),
				Thinking:   strPtr("medium"),
			},
		},
	}

	ui := &fakeSetupUI{
		steps: []fakeSetupStep{
			{kind: "option", reply: "Keep original config (openai)", ok: true},
			{kind: "option", reply: "Keep original config (API key configured)", ok: true},
			{kind: "option", reply: "Use default endpoint", ok: true},
			{kind: "option", reply: "Keep original config (gpt-5)", ok: true},
			{kind: "thinking", reply: "Keep original config (medium)", ok: true},
			{kind: "option", reply: "Continue to Advanced Setup (theme, sandbox, autoreview, interactive)", ok: true},
			{kind: "option", reply: "Choose a theme (live preview)", ok: true},
			{kind: "theme", reply: "dracula", ok: true},
			{kind: "option", reply: "Enable sandbox", ok: true},
			{kind: "option", reply: "Skip / leave empty", ok: true},
			{kind: "option", reply: "Enable autoreview", ok: true},
			{kind: "option", reply: "Keep original config (default: disabled)", ok: true},
		},
	}

	var stdout, stderr bytes.Buffer
	deps := setupDeps{
		newUI:       func(io.Reader, io.Writer) setupUI { return ui },
		loadConfig:  func() (config.Config, []string, []error, error) { return existingCfg, []string{cfgPath}, nil, nil },
		userCfgPath: func() (string, error) { return cfgPath, nil },
		newWriter: func() (*config.RuntimePreferenceWriter, error) {
			return config.NewRuntimePreferenceWriterForPath(cfgPath), nil
		},
		writeKeyFile: config.WriteAPIKeyFile,
		probeModels: func(ctx context.Context, cfg config.Config, provider, overrideBaseURL, overrideAPIKey string, policy *redaction.Policy) (setupProbeResult, error) {
			return setupProbeResult{
				models: []string{"gpt-5"},
				thinkingCap: func(m string) llm.ThinkingCapability {
					return llm.ThinkingCapability{Supported: true, Adjustable: true, Levels: []string{"low", "medium", "high"}, Default: "medium"}
				},
			}, nil
		},
		checkConfig: func() ([]config.Issue, []string, error) {
			return nil, []string{cfgPath}, nil
		},
		goos: "linux",
	}

	if err := runSetupWithDeps(nil, strings.NewReader(""), &stdout, &stderr, true, deps); err != nil {
		t.Fatalf("runSetupWithDeps() error = %v", err)
	}

	// Verify `(configured)` appeared in the provider picker.
	foundConfiguredBadge := false
	for _, opt := range ui.seenOptions[0] {
		if opt == "openai (configured)" {
			foundConfiguredBadge = true
			break
		}
	}
	if !foundConfiguredBadge {
		t.Fatalf("expected 'openai (configured)' in provider options, got %v", ui.seenOptions[0])
	}

	cfgBytes, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	cfgText := string(cfgBytes)
	if !strings.Contains(cfgText, `theme = "dracula"`) || !strings.Contains(cfgText, `sandbox = true`) || !strings.Contains(cfgText, `autoreview = true`) {
		t.Fatalf("expected advanced settings in config, got:\n%s", cfgText)
	}
	if !strings.Contains(cfgText, `model = "gpt-5"`) || !strings.Contains(cfgText, `thinking = "medium"`) {
		t.Fatalf("expected original model/thinking preserved, got:\n%s", cfgText)
	}
}

func TestSetupConnectionFailureRetryAndManualModel(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")

	ui := &fakeSetupUI{
		steps: []fakeSetupStep{
			{kind: "option", reply: "ollama", ok: true},
			{kind: "prompt", reply: "http://127.0.0.1:11434", ok: true},
			{kind: "option", reply: "Retry connection", ok: true},
			{kind: "option", reply: "Enter model name manually (skip live check)", ok: true},
			{kind: "prompt", reply: "qwen2.5-coder:7b", ok: true},
			{kind: "option", reply: "Finish setup and save", ok: true},
		},
	}

	attempts := 0
	var stdout, stderr bytes.Buffer
	deps := setupDeps{
		newUI:       func(io.Reader, io.Writer) setupUI { return ui },
		loadConfig:  func() (config.Config, []string, []error, error) { return config.Config{}, nil, nil, nil },
		userCfgPath: func() (string, error) { return cfgPath, nil },
		newWriter: func() (*config.RuntimePreferenceWriter, error) {
			return config.NewRuntimePreferenceWriterForPath(cfgPath), nil
		},
		writeKeyFile: config.WriteAPIKeyFile,
		probeModels: func(ctx context.Context, cfg config.Config, provider, overrideBaseURL, overrideAPIKey string, policy *redaction.Policy) (setupProbeResult, error) {
			attempts++
			return setupProbeResult{}, errors.New("connection refused")
		},
		checkConfig: func() ([]config.Issue, []string, error) {
			return nil, []string{cfgPath}, nil
		},
		goos: "linux",
	}

	if err := runSetupWithDeps(nil, strings.NewReader(""), &stdout, &stderr, true, deps); err != nil {
		t.Fatalf("runSetupWithDeps() error = %v", err)
	}
	if attempts != 2 {
		t.Fatalf("probe attempts = %d, want 2", attempts)
	}
	cfgBytes, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !strings.Contains(string(cfgBytes), `model = "qwen2.5-coder:7b"`) {
		t.Fatalf("expected manual model saved, got:\n%s", string(cfgBytes))
	}
}

func TestSetupChatGPTInlineOAuth(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")

	ui := &fakeSetupUI{
		steps: []fakeSetupStep{
			{kind: "option", reply: "chatgpt", ok: true},
			{kind: "model", reply: "gpt-5.6-codex", ok: true},
			{kind: "option", reply: "Finish setup and save", ok: true},
		},
	}

	oauthCalled := false
	var stdout, stderr bytes.Buffer
	deps := setupDeps{
		newUI: func(io.Reader, io.Writer) setupUI { return ui },
		loadConfig: func() (config.Config, []string, []error, error) {
			return config.Config{ChatGPT: config.ChatGPT{OAuthPath: filepath.Join(dir, "chatgpt-oauth.json")}}, nil, nil, nil
		},
		userCfgPath: func() (string, error) { return cfgPath, nil },
		newWriter: func() (*config.RuntimePreferenceWriter, error) {
			return config.NewRuntimePreferenceWriterForPath(cfgPath), nil
		},
		writeKeyFile: config.WriteAPIKeyFile,
		chatgptLogin: func(oauthPath string, stdin io.Reader, stdout io.Writer) error {
			oauthCalled = true
			return nil
		},
		probeModels: func(ctx context.Context, cfg config.Config, provider, overrideBaseURL, overrideAPIKey string, policy *redaction.Policy) (setupProbeResult, error) {
			return setupProbeResult{models: []string{"gpt-5.6-codex"}}, nil
		},
		checkConfig: func() ([]config.Issue, []string, error) {
			return nil, []string{cfgPath}, nil
		},
		goos: "linux",
	}

	if err := runSetupWithDeps(nil, strings.NewReader(""), &stdout, &stderr, true, deps); err != nil {
		t.Fatalf("runSetupWithDeps() error = %v", err)
	}
	if !oauthCalled {
		t.Fatal("expected inline ChatGPT OAuth flow to be called")
	}
}

func TestSetupRedactsNewAPIKeyAndResolvesRelativeKeyPath(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, "cfg")
	cfgPath := filepath.Join(cfgDir, "config.toml")
	relKeyPath := filepath.Join("keys", "openai.key")
	wantKeyPath := filepath.Join(cfgDir, "keys", "openai.key")
	newSecret := "sk-brand-new-secret-xyz"

	ui := &fakeSetupUI{
		steps: []fakeSetupStep{
			{kind: "option", reply: "openai", ok: true},
			{kind: "prompt", reply: newSecret, ok: true},
			{kind: "prompt", reply: relKeyPath, ok: true},
			{kind: "option", reply: "Use default endpoint", ok: true},
			// Probe fails once with the raw secret in the error, then we enter manual model.
			{kind: "option", reply: "Enter model name manually (skip live check)", ok: true},
			{kind: "prompt", reply: "gpt-5", ok: true},
			{kind: "option", reply: "Finish setup and save", ok: true},
		},
	}

	var stdout, stderr bytes.Buffer
	deps := setupDeps{
		newUI:       func(io.Reader, io.Writer) setupUI { return ui },
		loadConfig:  func() (config.Config, []string, []error, error) { return config.Config{}, nil, nil, nil },
		userCfgPath: func() (string, error) { return cfgPath, nil },
		newWriter: func() (*config.RuntimePreferenceWriter, error) {
			return config.NewRuntimePreferenceWriterForPath(cfgPath), nil
		},
		writeKeyFile: config.WriteAPIKeyFile,
		probeModels: func(ctx context.Context, cfg config.Config, provider, overrideBaseURL, overrideAPIKey string, policy *redaction.Policy) (setupProbeResult, error) {
			return setupProbeResult{}, errors.New("401 unauthorized for key " + overrideAPIKey)
		},
		checkConfig: func() ([]config.Issue, []string, error) {
			return nil, []string{cfgPath}, nil
		},
		goos: "linux",
	}

	if err := runSetupWithDeps(nil, strings.NewReader(""), &stdout, &stderr, true, deps); err != nil {
		t.Fatalf("runSetupWithDeps() error = %v", err)
	}
	if strings.Contains(stdout.String(), newSecret) {
		t.Fatalf("new API key leaked in stdout:\n%s", stdout.String())
	}
	if _, err := os.Lstat(wantKeyPath); err != nil {
		t.Fatalf("expected key file at %q: %v", wantKeyPath, err)
	}
	cfgBytes, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", cfgPath, err)
	}
	if !strings.Contains(string(cfgBytes), wantKeyPath) {
		t.Fatalf("expected canonicalized api_key_path %q in config:\n%s", wantKeyPath, string(cfgBytes))
	}
}

func TestSetupPreservesThinkingWhenThinkingStepSkipped(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	initial := "provider = \"ollama\"\n\n[providers.ollama]\nmodel = \"qwen2.5-coder:7b\"\nthinking = \"high\"\n"
	if err := os.WriteFile(cfgPath, []byte(initial), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	existingCfg := config.Config{
		Provider: "ollama",
		Providers: map[string]config.ProviderProfile{
			"ollama": {
				BaseURL:  strPtr("http://127.0.0.1:11434"),
				Model:    strPtr("qwen2.5-coder:7b"),
				Thinking: strPtr("high"),
			},
		},
	}

	ui := &fakeSetupUI{
		steps: []fakeSetupStep{
			{kind: "option", reply: "Keep original config (ollama)", ok: true},
			{kind: "option", reply: "Keep original config (http://127.0.0.1:11434)", ok: true},
			{kind: "option", reply: "Select a different model", ok: true},
			{kind: "prompt", reply: "qwen2.5-coder:32b", ok: true},
			{kind: "option", reply: "Finish setup and save", ok: true},
		},
	}

	var stdout, stderr bytes.Buffer
	deps := setupDeps{
		newUI:       func(io.Reader, io.Writer) setupUI { return ui },
		loadConfig:  func() (config.Config, []string, []error, error) { return existingCfg, []string{cfgPath}, nil, nil },
		userCfgPath: func() (string, error) { return cfgPath, nil },
		newWriter: func() (*config.RuntimePreferenceWriter, error) {
			return config.NewRuntimePreferenceWriterForPath(cfgPath), nil
		},
		writeKeyFile: config.WriteAPIKeyFile,
		probeModels: func(ctx context.Context, cfg config.Config, provider, overrideBaseURL, overrideAPIKey string, policy *redaction.Policy) (setupProbeResult, error) {
			// Return empty models & nil thinkingCap so thinking step is skipped.
			return setupProbeResult{}, nil
		},
		checkConfig: func() ([]config.Issue, []string, error) {
			return nil, []string{cfgPath}, nil
		},
		goos: "linux",
	}

	if err := runSetupWithDeps(nil, strings.NewReader(""), &stdout, &stderr, true, deps); err != nil {
		t.Fatalf("runSetupWithDeps() error = %v", err)
	}
	cfgBytes, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", cfgPath, err)
	}
	cfgText := string(cfgBytes)
	if !strings.Contains(cfgText, `model = "qwen2.5-coder:32b"`) {
		t.Fatalf("expected updated model in config:\n%s", cfgText)
	}
	if !strings.Contains(cfgText, `thinking = "high"`) {
		t.Fatalf("expected existing thinking = \"high\" to be preserved, got:\n%s", cfgText)
	}
}

func strPtr(s string) *string {
	return &s
}
