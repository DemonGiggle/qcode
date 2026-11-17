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
	"qcode/internal/tui"
)

type fakeSetupStep struct {
	kind  string // "option", "model", "thinking", "theme", "prompt"
	reply string
	nav   tui.SetupNavResult
}

type fakeSetupUI struct {
	steps           []fakeSetupStep
	idx             int
	seenOptions     [][]string
	seenOptionNouns []string
}

func (f *fakeSetupUI) next(kind string) (string, tui.SetupNavResult, error) {
	if f.idx >= len(f.steps) {
		return "", tui.SetupCancelled, errors.New("unexpected UI prompt: " + kind)
	}
	step := f.steps[f.idx]
	f.idx++
	if step.kind != kind {
		return "", tui.SetupCancelled, errors.New("unexpected prompt kind: got " + kind + ", script has " + step.kind)
	}
	return step.reply, step.nav, nil
}

func (f *fakeSetupUI) SelectOptionNav(noun string, options []string, disabled map[string]bool, current string, searchable bool) (string, tui.SetupNavResult, error) {
	f.seenOptionNouns = append(f.seenOptionNouns, noun)
	f.seenOptions = append(f.seenOptions, append([]string(nil), options...))
	return f.next("option")
}

func (f *fakeSetupUI) SelectModelNav(models []string, current string) (string, tui.SetupNavResult, error) {
	return f.next("model")
}

func (f *fakeSetupUI) SelectThinkingNav(levels []string, current string) (string, tui.SetupNavResult, error) {
	return f.next("thinking")
}

func (f *fakeSetupUI) SelectThemeNav(current string) (string, tui.SetupNavResult, error) {
	return f.next("theme")
}

func (f *fakeSetupUI) PromptLineNav(label, defaultVal string, secret bool) (string, tui.SetupNavResult, error) {
	return f.next("prompt")
}

func TestSetupRequiresInteractiveTerminal(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := runSetupWithDeps(nil, strings.NewReader(""), &stdout, &stderr, false, defaultSetupDeps())
	if err == nil || !strings.Contains(err.Error(), "interactive terminal") {
		t.Fatalf("expected interactive terminal error, got %v", err)
	}
}

func TestSetupMenuconfigFreshOpenAIAndSaveWithBackup(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	keyPath := filepath.Join(dir, "openai.key")
	// Pre-existing config so Save and Leave creates a .bak and announces it.
	if err := os.WriteFile(cfgPath, []byte("provider = \"ollama\"\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	ui := &fakeSetupUI{
		steps: []fakeSetupStep{
			// Main Menu -> Providers & Models
			{kind: "option", reply: "Providers & Models  --->  (active: ollama / unset)", nav: tui.SetupAccepted},
			// Providers list -> openai
			{kind: "option", reply: "openai", nav: tui.SetupAccepted},
			// Provider submenu -> Set as active default provider
			{kind: "option", reply: "Set as active default provider", nav: tui.SetupAccepted},
			// Provider submenu -> Configure credentials & endpoint
			{kind: "option", reply: "Configure credentials & endpoint", nav: tui.SetupAccepted},
			{kind: "prompt", reply: "sk-test-12345", nav: tui.SetupAccepted},
			{kind: "prompt", reply: keyPath, nav: tui.SetupAccepted},
			{kind: "option", reply: "Use default endpoint", nav: tui.SetupAccepted},
			// Provider submenu -> Verify connection & select model
			{kind: "option", reply: "Verify connection & select model (unset)", nav: tui.SetupAccepted},
			{kind: "model", reply: "gpt-5", nav: tui.SetupAccepted},
			{kind: "thinking", reply: "high", nav: tui.SetupAccepted},
			// Back out to Providers list, then Back to Main Menu
			{kind: "option", reply: backMenuItem, nav: tui.SetupAccepted},
			{kind: "option", reply: "", nav: tui.SetupBack},
			// Main Menu -> Appearance (Theme)
			{kind: "option", reply: "Appearance (Theme)  --->  (default)", nav: tui.SetupAccepted},
			{kind: "theme", reply: "dracula", nav: tui.SetupAccepted},
			// Main Menu -> Save and Leave
			{kind: "option", reply: "Save and Leave", nav: tui.SetupAccepted},
		},
	}

	var stdout, stderr bytes.Buffer
	deps := setupDeps{
		newUI: func(io.Reader, io.Writer) setupUI { return ui },
		loadConfig: func() (config.Config, []string, []error, error) {
			return config.Config{Provider: "ollama"}, []string{cfgPath}, nil, nil
		},
		userCfgPath: func() (string, error) { return cfgPath, nil },
		newWriter: func() (*config.RuntimePreferenceWriter, error) {
			return config.NewRuntimePreferenceWriterForPath(cfgPath), nil
		},
		writeKeyFile: config.WriteAPIKeyFile,
		backupConfig: config.BackupUserConfig,
		probeModels: func(ctx context.Context, cfg config.Config, provider, overrideBaseURL, overrideAPIKey string, policy *redaction.Policy) (setupProbeResult, error) {
			if provider != "openai" || overrideAPIKey != "sk-test-12345" {
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
	if strings.Contains(stdout.String(), "[REDACTED]") {
		t.Fatalf("raw API key leaked in stdout:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Backed up existing config to "+cfgPath+".bak") {
		t.Fatalf("expected backup message in stdout:\n%s", stdout.String())
	}
	if _, err := os.Lstat(cfgPath + ".bak"); err != nil {
		t.Fatalf("expected backup file at %s.bak: %v", cfgPath, err)
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
	if !strings.Contains(cfgText, `provider = "openai"`) || !strings.Contains(cfgText, `model = "gpt-5"`) || !strings.Contains(cfgText, `thinking = "high"`) || !strings.Contains(cfgText, `theme = "dracula"`) {
		t.Fatalf("unexpected saved config:\n%s", cfgText)
	}
}

func TestSetupMenuconfigExitWithoutSavingLeavesConfigUntouched(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	original := "provider = \"ollama\"\n"
	if err := os.WriteFile(cfgPath, []byte(original), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	ui := &fakeSetupUI{
		steps: []fakeSetupStep{
			// Main Menu -> Behavior -> Toggle interactive questions -> Esc back to Main Menu -> Exit without saving
			{kind: "option", reply: "Behavior            --->  (interactive: off)", nav: tui.SetupAccepted},
			{kind: "option", reply: "Toggle interactive questions (currently: disabled)", nav: tui.SetupAccepted},
			{kind: "option", reply: "", nav: tui.SetupBack},
			{kind: "option", reply: "Exit without saving", nav: tui.SetupAccepted},
		},
	}

	var stdout, stderr bytes.Buffer
	deps := setupDeps{
		newUI: func(io.Reader, io.Writer) setupUI { return ui },
		loadConfig: func() (config.Config, []string, []error, error) {
			return config.Config{Provider: "ollama"}, []string{cfgPath}, nil, nil
		},
		userCfgPath: func() (string, error) { return cfgPath, nil },
		newWriter: func() (*config.RuntimePreferenceWriter, error) {
			return config.NewRuntimePreferenceWriterForPath(cfgPath), nil
		},
		writeKeyFile: config.WriteAPIKeyFile,
		backupConfig: config.BackupUserConfig,
		checkConfig:  func() ([]config.Issue, []string, error) { return nil, nil, nil },
		goos:         "linux",
	}

	if err := runSetupWithDeps(nil, strings.NewReader(""), &stdout, &stderr, true, deps); err != nil {
		t.Fatalf("runSetupWithDeps() error = %v", err)
	}
	for i, noun := range ui.seenOptionNouns {
		if noun == "behavior" {
			for _, opt := range ui.seenOptions[i] {
				if strings.Contains(strings.ToLower(opt), "autoreview") {
					t.Fatalf("unexpected autoreview option in behavior menu: %v", ui.seenOptions[i])
				}
			}
		}
	}
	got, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(got) != original {
		t.Fatalf("config modified on Exit without saving:\ngot:\n%s\nwant:\n%s", string(got), original)
	}
	if _, err := os.Lstat(cfgPath + ".bak"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected backup created when exiting without saving")
	}
}

func TestSetupRedactsNewAPIKeyAndResolvesRelativeKeyPath(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, "cfg")
	cfgPath := filepath.Join(cfgDir, "config.toml")
	relKeyPath := filepath.Join("keys", "openai.key")
	wantKeyPath := filepath.Join(cfgDir, "keys", "openai.key")
	newSecret := "sk-fresh-secret-xyz987"

	ui := &fakeSetupUI{
		steps: []fakeSetupStep{
			{kind: "option", reply: "Providers & Models  --->  (active: ollama / unset)", nav: tui.SetupAccepted},
			{kind: "option", reply: "openai", nav: tui.SetupAccepted},
			{kind: "option", reply: "Set as active default provider", nav: tui.SetupAccepted},
			{kind: "option", reply: "Configure credentials & endpoint", nav: tui.SetupAccepted},
			{kind: "prompt", reply: newSecret, nav: tui.SetupAccepted},
			{kind: "prompt", reply: relKeyPath, nav: tui.SetupAccepted},
			{kind: "option", reply: "Use default endpoint", nav: tui.SetupAccepted},
			{kind: "option", reply: "Verify connection & select model (unset)", nav: tui.SetupAccepted},
			// Probe fails once with the raw secret in the error, then enter manual model.
			{kind: "option", reply: "Enter model name manually (skip live check)", nav: tui.SetupAccepted},
			{kind: "prompt", reply: "gpt-5", nav: tui.SetupAccepted},
			{kind: "option", reply: "", nav: tui.SetupBack},
			{kind: "option", reply: "", nav: tui.SetupBack},
			{kind: "option", reply: "Save and Leave", nav: tui.SetupAccepted},
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
		backupConfig: config.BackupUserConfig,
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
			{kind: "option", reply: "Providers & Models  --->  (active: ollama / qwen2.5-coder:7b)", nav: tui.SetupAccepted},
			{kind: "option", reply: "ollama (active, configured)", nav: tui.SetupAccepted},
			{kind: "option", reply: "Verify connection & select model (qwen2.5-coder:7b, thinking: high)", nav: tui.SetupAccepted},
			{kind: "prompt", reply: "qwen2.5-coder:32b", nav: tui.SetupAccepted},
			{kind: "option", reply: "", nav: tui.SetupBack},
			{kind: "option", reply: "", nav: tui.SetupBack},
			{kind: "option", reply: "Save and Leave", nav: tui.SetupAccepted},
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
		backupConfig: config.BackupUserConfig,
		probeModels: func(ctx context.Context, cfg config.Config, provider, overrideBaseURL, overrideAPIKey string, policy *redaction.Policy) (setupProbeResult, error) {
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

func TestSetupChatGPTInlineOAuth(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")

	ui := &fakeSetupUI{
		steps: []fakeSetupStep{
			{kind: "option", reply: "Providers & Models  --->  (active: ollama / unset)", nav: tui.SetupAccepted},
			{kind: "option", reply: "chatgpt", nav: tui.SetupAccepted},
			{kind: "option", reply: "Set as active default provider", nav: tui.SetupAccepted},
			{kind: "option", reply: "Configure credentials & endpoint", nav: tui.SetupAccepted},
			{kind: "option", reply: "Verify connection & select model (unset)", nav: tui.SetupAccepted},
			{kind: "model", reply: "gpt-5.6-codex", nav: tui.SetupAccepted},
			{kind: "option", reply: "", nav: tui.SetupBack},
			{kind: "option", reply: "", nav: tui.SetupBack},
			{kind: "option", reply: "Save and Leave", nav: tui.SetupAccepted},
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
		backupConfig: config.BackupUserConfig,
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

func strPtr(s string) *string {
	return &s
}
