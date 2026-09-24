package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCandidatePaths(t *testing.T) {
	paths := candidatePaths("linux", filepath.Join("", "opt", "qcode", "bin", "qcode"), filepath.Join("", "home", "ada"), "", "")
	want := []string{
		filepath.Join("", "opt", "qcode", "bin", "config.toml"),
		filepath.Join("", "home", "ada", ".local", "etc", "qcode", "config.toml"),
		filepath.Join(string(filepath.Separator), "usr", "local", "etc", "qcode", "config.toml"),
		filepath.Join("", "opt", "qcode", "bin", "etc", "config.toml"),
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Fatalf("path %d = %q, want %q", i, paths[i], want[i])
		}
	}
}

func TestCandidatePathsDarwin(t *testing.T) {
	paths := candidatePaths("darwin", "/Applications/qcode", "/Users/ada", "/Users/ada/Library/Application Support", "")
	want := []string{
		"/Applications/config.toml",
		"/Users/ada/Library/Application Support/qcode/config.toml",
		"/Library/Application Support/qcode/config.toml",
		"/Applications/etc/config.toml",
	}
	for i := range want {
		if paths[i] != filepath.FromSlash(want[i]) {
			t.Fatalf("path %d = %q, want %q", i, paths[i], filepath.FromSlash(want[i]))
		}
	}
}

func TestCandidatePathsWindows(t *testing.T) {
	paths := candidatePaths("windows", filepath.Join("C:", "Tools", "qcode.exe"), "", filepath.Join("C:", "Users", "ada", "AppData", "Roaming"), filepath.Join("C:", "ProgramData"))
	if len(paths) != 4 {
		t.Fatalf("got %d paths, want 4", len(paths))
	}
	if paths[1] != filepath.Join("C:", "Users", "ada", "AppData", "Roaming", "qcode", "config.toml") {
		t.Fatalf("user path = %q", paths[1])
	}
	if paths[2] != filepath.Join("C:", "ProgramData", "qcode", "config.toml") {
		t.Fatalf("system path = %q", paths[2])
	}
	if paths[3] != filepath.Join("C:", "Tools", "etc", "config.toml") {
		t.Fatalf("executable-relative path = %q", paths[3])
	}
}

func TestLoadLayersConfiguration(t *testing.T) {
	dir := t.TempDir()
	high := writeConfig(t, dir, "high.toml", `provider = "openai"
model = ""
max_steps = 48
sandbox = true
agent_timeout = "90s"
[learning]
context_budget = 0
[web_search]
backend = "brave"
[skills]
paths = [" high ", "low", ""]
autoload_paths = [" high-auto ", "low-auto", ""]
`)
	low := writeConfig(t, dir, "low.toml", `provider = "ollama"
model = "qwen"
base_url = "http://low.test"
api_key = "low-key"
thinking = "high"
context_window = 8192
auto_compact_threshold = 70
disable_auto_compact = false
max_steps = 16
sandbox = false
danger_skip_tls_verify = true
agent_timeout = "5m"
[learning]
context_budget = 1200
[web_search]
backend = "duckduckgo"
[skills]
paths = ["low", " /opt/skills ", ""]
autoload_paths = ["low-auto", " /opt/auto-skills ", ""]
`)

	// Candidate paths are ordered highest to lowest priority.
	cfg, inspected, diagnostics := load([]string{high, filepath.Join(dir, "missing.toml"), low})
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %v", diagnostics)
	}
	if got, want := strings.Join(inspected, ","), strings.Join([]string{low, high}, ","); got != want {
		t.Fatalf("inspected = %q, want %q", got, want)
	}
	if cfg.Provider != "openai" || cfg.Model != "qwen" || cfg.BaseURL != "http://low.test" || cfg.APIKey != "low-key" || cfg.Thinking != "high" {
		t.Fatalf("string settings = %+v", cfg)
	}
	if cfg.ContextWindow == nil || *cfg.ContextWindow != 8192 || cfg.AutoCompactThreshold == nil || *cfg.AutoCompactThreshold != 70 || cfg.DisableAutoCompact == nil || *cfg.DisableAutoCompact || cfg.MaxSteps == nil || *cfg.MaxSteps != 48 || cfg.Sandbox == nil || !*cfg.Sandbox || cfg.DangerSkipTLSVerify == nil || !*cfg.DangerSkipTLSVerify || cfg.AgentTimeout == nil || *cfg.AgentTimeout != "90s" {
		t.Fatalf("scalar settings = %+v", cfg)
	}
	if cfg.Learning.ContextBudget == nil || *cfg.Learning.ContextBudget != 0 || cfg.WebSearch.Backend != "brave" {
		t.Fatalf("nested settings = %+v", cfg)
	}
	if got, want := strings.Join(cfg.Skills.Paths, ","), "low,/opt/skills,high"; got != want {
		t.Fatalf("skill paths = %q, want %q", got, want)
	}
	if got, want := strings.Join(cfg.Skills.AutoloadPaths, ","), "low-auto,/opt/auto-skills,high-auto"; got != want {
		t.Fatalf("autoload paths = %q, want %q", got, want)
	}
}

func TestLoadSkipsUnreadableInvalidAndValidationFailingLayers(t *testing.T) {
	dir := t.TempDir()
	badValue := writeConfig(t, dir, "bad-value.toml", "max_steps = 0\n")
	valid := writeConfig(t, dir, "valid.toml", "model = \"valid-model\"\n")
	badTOML := writeConfig(t, dir, "bad-toml.toml", "model = [\n")
	unreadable := filepath.Join(dir, "unreadable.toml")
	if err := os.Mkdir(unreadable, 0o700); err != nil {
		t.Fatal(err)
	}

	cfg, inspected, diagnostics := load([]string{badTOML, valid, badValue, unreadable})
	if cfg.Model != "valid-model" {
		t.Fatalf("config = %+v, want valid layer", cfg)
	}
	if got, want := strings.Join(inspected, ","), strings.Join([]string{unreadable, badValue, valid, badTOML}, ","); got != want {
		t.Fatalf("inspected = %q, want %q", got, want)
	}
	if len(diagnostics) != 3 || !strings.Contains(diagnostics[0].Error(), "read config "+unreadable) || !strings.Contains(diagnostics[1].Error(), "max_steps must be greater than zero") || !strings.Contains(diagnostics[2].Error(), "parse config "+badTOML) {
		t.Fatalf("diagnostics = %v", diagnostics)
	}
}

func TestLoadRejectsUnknownFieldsBySkippingLayer(t *testing.T) {
	path := writeConfig(t, t.TempDir(), "config.toml", "modle = \"typo\"\n")
	cfg, inspected, diagnostics := load([]string{path})
	if cfg.Provider != "" || len(inspected) != 1 || len(diagnostics) != 1 || !strings.Contains(diagnostics[0].Error(), "strict mode") {
		t.Fatalf("load = (%+v, %q, %v)", cfg, inspected, diagnostics)
	}
}

func TestValidationSkipsInvalidValues(t *testing.T) {
	for _, tc := range []struct{ content, message string }{
		{"[learning]\ncontext_budget = -1\n", "learning.context_budget"},
		{"context_window = -1\n", "context_window"},
		{"auto_compact_threshold = 100\n", "auto_compact_threshold"},
		{"max_steps = 0\n", "max_steps"},
		{"agent_timeout = \"0s\"\n", "agent_timeout"},
	} {
		t.Run(tc.message, func(t *testing.T) {
			path := writeConfig(t, t.TempDir(), "config.toml", tc.content)
			_, _, diagnostics := load([]string{path})
			if len(diagnostics) != 1 || !strings.Contains(diagnostics[0].Error(), tc.message) {
				t.Fatalf("diagnostics = %v", diagnostics)
			}
		})
	}
}

func TestLoadReturnsEmptyWhenNoFileExists(t *testing.T) {
	cfg, inspected, diagnostics := load([]string{filepath.Join(t.TempDir(), "missing.toml")})
	if len(inspected) != 0 || len(diagnostics) != 0 || cfg.Provider != "" || cfg.Model != "" || len(cfg.Skills.Paths) != 0 {
		t.Fatalf("load = (%+v, %q, %v), want empty result", cfg, inspected, diagnostics)
	}
	if cfg.SandboxCommandPaths != nil {
		t.Fatalf("command paths = %q, want nil for missing file", cfg.SandboxCommandPaths)
	}
}

func TestLoadSandboxCommandPathsOverride(t *testing.T) {
	dir := t.TempDir()
	low := writeConfig(t, dir, "low.toml", "sandbox_command_paths = [\"/opt/a\", \"/opt/b\"]\n")
	high := writeConfig(t, dir, "high.toml", "sandbox_command_paths = [\"/opt/c\"]\n")
	cfg, _, diagnostics := load([]string{high, low})
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %v", diagnostics)
	}
	if got, want := strings.Join(cfg.SandboxCommandPaths, ","), "/opt/c"; got != want {
		t.Fatalf("command paths = %q, want %q (override, not additive)", got, want)
	}
	// Existing sandbox=true config without the new key remains valid.
	legacy := writeConfig(t, dir, "legacy.toml", "sandbox = true\n")
	legacyCfg, _, legacyDiags := load([]string{legacy})
	if len(legacyDiags) != 0 || legacyCfg.Sandbox == nil || !*legacyCfg.Sandbox || legacyCfg.SandboxCommandPaths != nil {
		t.Fatalf("legacy config = (%+v, %v)", legacyCfg, legacyDiags)
	}
}

func writeConfig(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
