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
	if err := os.WriteFile(first, []byte("provider = \"openai\"\nmodel = \"gpt-5\"\nbase_url = \"https://example.test/v1\"\napi_key = \"configured-key\"\nmax_steps = 48\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("provider = \"ollama\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, path, err := load([]string{filepath.Join(dir, "missing.toml"), first, second})
	if err != nil {
		t.Fatal(err)
	}
	if path != first || cfg.Provider != "openai" || cfg.Model != "gpt-5" || cfg.BaseURL != "https://example.test/v1" || cfg.APIKey != "configured-key" || cfg.MaxSteps == nil || *cfg.MaxSteps != 48 {
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
	if err != nil || path != "" || cfg != (Config{}) {
		t.Fatalf("load = (%+v, %q, %v), want empty result", cfg, path, err)
	}
}
