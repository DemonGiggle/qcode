package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckDeprecationWarnings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	text := "provider = 'openai'\nbase_url = 'http://old.test'\napi_key = 'old-key'\nmodel = 'old-model'\nthinking = 'high'\n[provider_models]\nopenai = 'remembered'\n[provider_thinking]\nopenai = 'low'\n[providers.openai]\napi_key = 'inline-key'\n"
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	issues := CheckPaths([]string{path})
	warnings := map[string]bool{}
	for _, issue := range issues {
		if issue.Severity == SeverityWarning {
			warnings[issue.Key] = true
			if issue.File != path {
				t.Fatalf("warning file = %q, want %q", issue.File, path)
			}
			if issue.Fix == "" {
				t.Fatalf("warning %s missing fix", issue.Key)
			}
			if issue.Key == "providers.openai.api_key" && strings.Contains(issue.Message, "inline-key") {
				t.Fatal("warning leaks secret")
			}
		}
	}
	for _, want := range []string{"base_url", "api_key", "model", "thinking", "providers.openai.api_key", "provider_models.openai", "provider_thinking.openai"} {
		if !warnings[want] {
			t.Fatalf("missing warning %s in %v", want, issues)
		}
	}
}

func TestCheckDottedAndTableEquivalent(t *testing.T) {
	// Same logical profile via dotted keys must not warn about style; only the
	// inline-secret warning applies in both forms.
	dotted := "providers.openai.api_key_path = '/tmp/openai.key'\n"
	table := "[providers.openai]\napi_key_path = '/tmp/openai.key'\n"
	for _, text := range []string{dotted, table} {
		path := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		issues := CheckPaths([]string{path})
		for _, issue := range issues {
			if strings.Contains(strings.ToLower(issue.Message), "dotted") || strings.Contains(strings.ToLower(issue.Message), "style") {
				t.Fatalf("style warning: %v", issue)
			}
		}
		// Both forms hit the same missing-file error (no style difference).
		found := false
		for _, issue := range issues {
			if issue.Severity == SeverityError && issue.Key == "providers.openai.api_key_path" {
				found = true
			}
		}
		if !found {
			t.Fatalf("expected api_key_path error for %q, got %v", text, issues)
		}
	}
}

func TestCheckMergedBothAcrossLayers(t *testing.T) {
	dir := t.TempDir()
	low := filepath.Join(dir, "low.toml")
	high := filepath.Join(dir, "high.toml")
	if err := os.WriteFile(low, []byte("[providers.openai]\napi_key = 'inline'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(dir, "openai.key")
	if err := os.WriteFile(keyPath, []byte("file-key\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(keyPath, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(high, []byte("[providers.openai]\napi_key_path = '"+keyPath+"'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	issues := CheckPaths([]string{high, low})
	found := false
	for _, issue := range issues {
		if issue.Severity == SeverityError && issue.File == "(merged)" && issue.Key == "providers.openai.api_key_path" {
			found = true
		}
	}
	if !found {
		t.Fatalf("merged conflict missing in %v", issues)
	}
}

func TestCheckCleanNewStyle(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "openai.key")
	if err := os.WriteFile(keyPath, []byte("k\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(keyPath, 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.toml")
	text := "provider = 'openai'\n[providers.openai]\napi_key_path = '" + keyPath + "'\nmodel = 'm'\nthinking = 'high'\n"
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	issues := CheckPaths([]string{path})
	if len(issues) != 0 {
		t.Fatalf("issues = %v, want clean", issues)
	}
}
