package config

import (
	"fmt"
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
	}
	for i := range want {
		if paths[i] != filepath.FromSlash(want[i]) {
			t.Fatalf("path %d = %q, want %q", i, paths[i], filepath.FromSlash(want[i]))
		}
	}
}

func TestCandidatePathsWindows(t *testing.T) {
	paths := candidatePaths("windows", filepath.Join("C:", "Tools", "qcode.exe"), "", filepath.Join("C:", "Users", "ada", "AppData", "Roaming"), filepath.Join("C:", "ProgramData"))
	if len(paths) != 3 {
		t.Fatalf("got %d paths, want 3", len(paths))
	}
	if paths[1] != filepath.Join("C:", "Users", "ada", "AppData", "Roaming", "qcode", "config.toml") {
		t.Fatalf("user path = %q", paths[1])
	}
	if paths[2] != filepath.Join("C:", "ProgramData", "qcode", "config.toml") {
		t.Fatalf("system path = %q", paths[2])
	}
}

func TestLoadUsesFirstExistingFile(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first.toml")
	second := filepath.Join(dir, "second.toml")
	if err := os.WriteFile(first, []byte("provider = \"openai\"\nmodel = \"gpt-5\"\nbase_url = \"https://example.test/v1\"\napi_key = \"configured-key\"\nmax_steps = 48\nsandbox = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("provider = \"ollama\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, path, err := load([]string{filepath.Join(dir, "missing.toml"), first, second})
	if err != nil {
		t.Fatal(err)
	}
	if path != first || cfg.Provider != "openai" || cfg.Model != "gpt-5" || cfg.BaseURL != "https://example.test/v1" || cfg.APIKey != "configured-key" || cfg.MaxSteps == nil || *cfg.MaxSteps != 48 || cfg.Sandbox == nil || !*cfg.Sandbox {
		t.Fatalf("load = (%+v, %q), want first config", cfg, path)
	}
}

func TestLoadRejectsNonPositiveMaxSteps(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("max_steps = 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, _, err := load([]string{path})
	if err == nil || !strings.Contains(err.Error(), "max_steps must be greater than zero") {
		t.Fatalf("load error = %v, want max_steps validation error", err)
	}
}

func TestAgentTimeoutConfiguration(t *testing.T) {
	for _, value := range []string{`"5m"`, `"30s"`, `"1h"`, `"0s"`, `"-2m"`, `""`, `"forever"`, `300`, `"999999999999999999h"`} {
		t.Run(value, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte("agent_timeout = "+value+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, _, err := load([]string{path})
			valid := value == `"5m"` || value == `"30s"` || value == `"1h"`
			if valid && (err != nil || cfg.AgentTimeout == nil) {
				t.Fatalf("%+v %v", cfg, err)
			}
			if !valid && err == nil {
				t.Fatal("invalid timeout accepted")
			}
		})
	}
}

func TestAutoCompactConfiguration(t *testing.T) {
	for _, tc := range []struct {
		value string
		valid bool
	}{
		{"1", true}, {"80", true}, {"99", true}, {"0", false}, {"100", false},
	} {
		path := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(path, []byte("auto_compact_threshold = "+tc.value+"\ndisable_auto_compact = true\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, _, err := load([]string{path})
		if (err == nil) != tc.valid {
			t.Fatalf("value %s: %v", tc.value, err)
		}
		if tc.valid && (cfg.AutoCompactThreshold == nil || *cfg.AutoCompactThreshold != mustInt(t, tc.value) || cfg.DisableAutoCompact == nil || !*cfg.DisableAutoCompact) {
			t.Fatalf("config = %+v", cfg)
		}
	}
}

func mustInt(t *testing.T, value string) int {
	t.Helper()
	var result int
	if _, err := fmt.Sscan(value, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("modle = \"typo\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, _, err := load([]string{path})
	if err == nil || !strings.Contains(err.Error(), "strict mode") {
		t.Fatalf("load error = %v, want strict-mode field error", err)
	}
}

func TestLoadReturnsEmptyWhenNoFileExists(t *testing.T) {
	cfg, path, err := load([]string{filepath.Join(t.TempDir(), "missing.toml")})
	if err != nil || path != "" || cfg.Provider != "" || cfg.Model != "" || len(cfg.Skills.Paths) != 0 {
		t.Fatalf("load = (%+v, %q, %v), want empty result", cfg, path, err)
	}
}

func TestWebSearchConfig(t *testing.T) {
	for _, tc := range []struct{ content, backend string }{
		{"", ""}, {"[web_search]\nbackend = \"duckduckgo\"\n", "duckduckgo"},
	} {
		path := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(path, []byte(tc.content), 0600); err != nil {
			t.Fatal(err)
		}
		cfg, _, err := load([]string{path})
		if err != nil || cfg.WebSearch.Backend != tc.backend {
			t.Fatalf("got %+v, %v", cfg, err)
		}
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[web_search]\nbackned = \"duckduckgo\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := load([]string{path}); err == nil {
		t.Fatal("accepted unknown search configuration field")
	}
}

func TestLearningBudgetConfig(t *testing.T) {
	for _, tc := range []struct {
		value string
		valid bool
	}{
		{"0", true}, {"1200", true}, {"12000", true}, {"-1", false}, {"12001", false},
	} {
		path := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(path, []byte("[learning]\ncontext_budget = "+tc.value+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		cfg, _, err := load([]string{path})
		if (err == nil) != tc.valid {
			t.Fatalf("value %s: %v", tc.value, err)
		}
		if tc.valid && cfg.Learning.ContextBudget == nil {
			t.Fatal("budget not loaded")
		}
	}
}

func TestSkillPathsConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[skills]\npaths = [\"/opt/qcode/skills\", \"extra-skills\"]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := load([]string{path})
	if err != nil || strings.Join(cfg.Skills.Paths, ",") != "/opt/qcode/skills,extra-skills" {
		t.Fatalf("skill paths = %#v, error = %v", cfg.Skills.Paths, err)
	}
}
