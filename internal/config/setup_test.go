package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteAPIKeyFilePermissionsAndGuards(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "keys", "openai.key")

	full, err := WriteAPIKeyFile(keyPath, "  sk-test-12345  ")
	if err != nil {
		t.Fatalf("WriteAPIKeyFile() error = %v", err)
	}
	if full != keyPath {
		t.Fatalf("full path = %q, want %q", full, keyPath)
	}
	info, err := os.Lstat(keyPath)
	if err != nil {
		t.Fatalf("Lstat(%q) error = %v", keyPath, err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("permissions = %04o, want 0600", perm)
	}
	data, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", keyPath, err)
	}
	if string(data) != "sk-test-12345\n" {
		t.Fatalf("key content = %q, want %q", string(data), "sk-test-12345\n")
	}

	// Reject empty and multi-line keys.
	if _, err := WriteAPIKeyFile(keyPath, "   "); err == nil {
		t.Fatal("expected error for empty API key")
	}
	if _, err := WriteAPIKeyFile(keyPath, "line1\nline2"); err == nil {
		t.Fatal("expected error for multi-line API key")
	}

	// Reject symlinks.
	sym := filepath.Join(dir, "sym.key")
	if err := os.Symlink(keyPath, sym); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	if _, err := WriteAPIKeyFile(sym, "sk-other"); err == nil {
		t.Fatal("expected error when target is a symlink")
	}
}

func TestPersistProviderConnectionPreservesComments(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	keyPath := filepath.Join(dir, "openai.key")
	if _, err := WriteAPIKeyFile(keyPath, "sk-file-secret"); err != nil {
		t.Fatalf("WriteAPIKeyFile() error = %v", err)
	}

	original := "# User header comment\nprovider = \"openai\"\n\n[providers.openai]\napi_key = \"inline-old\"\nmodel = \"gpt-5\"\n"
	if err := os.WriteFile(cfgPath, []byte(original), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	w := newRuntimePreferenceWriter(cfgPath)
	base := "https://custom.example/v1"
	if err := w.PersistProviderConnection("openai", &base, &keyPath); err != nil {
		t.Fatalf("PersistProviderConnection() error = %v", err)
	}

	updatedBytes, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	updated := string(updatedBytes)
	if !strings.Contains(updated, "# User header comment") {
		t.Fatalf("expected comment to be preserved, got:\n%s", updated)
	}
	if strings.Contains(updated, "inline-old") {
		t.Fatalf("expected inline api_key to be removed, got:\n%s", updated)
	}

	cfg, err := decodeConfig(updatedBytes, true)
	if err != nil {
		t.Fatalf("decodeConfig() error = %v", err)
	}
	resolvedBase, resolvedKey := cfg.ProviderConnection("openai")
	if resolvedBase != base {
		t.Fatalf("base_url = %q, want %q", resolvedBase, base)
	}
	if resolvedKey != "" {
		// decodeConfig does not resolve api_key_path; verify profile fields directly.
		t.Fatalf("unexpected inline key %q", resolvedKey)
	}
	if cfg.Providers["openai"].APIKeyPath == nil || *cfg.Providers["openai"].APIKeyPath != keyPath {
		t.Fatalf("api_key_path = %v, want %q", cfg.Providers["openai"].APIKeyPath, keyPath)
	}
}

func TestPersistSandboxAndCommandPaths(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte("# Keep me\nprovider = \"ollama\"\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	w := newRuntimePreferenceWriter(cfgPath)
	if err := w.PersistSandbox(true); err != nil {
		t.Fatalf("PersistSandbox() error = %v", err)
	}
	if err := w.PersistSandboxCommandPaths([]string{"~/bin", " ~/.local/bin ", "~/bin"}); err != nil {
		t.Fatalf("PersistSandboxCommandPaths() error = %v", err)
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !strings.Contains(string(data), "# Keep me") {
		t.Fatalf("comment lost:\n%s", string(data))
	}
	cfg, err := decodeConfig(data, true)
	if err != nil {
		t.Fatalf("decodeConfig() error = %v", err)
	}
	if cfg.Sandbox == nil || !*cfg.Sandbox {
		t.Fatalf("Sandbox = %v, want true", cfg.Sandbox)
	}
	if len(cfg.SandboxCommandPaths) != 2 || cfg.SandboxCommandPaths[0] != "~/bin" || cfg.SandboxCommandPaths[1] != "~/.local/bin" {
		t.Fatalf("SandboxCommandPaths = %v, want [~/bin ~/.local/bin]", cfg.SandboxCommandPaths)
	}
}

func TestBackupUserConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")

	// Missing file returns empty backup path without error.
	backup, err := BackupUserConfig(cfgPath)
	if err != nil {
		t.Fatalf("BackupUserConfig(missing) error = %v", err)
	}
	if backup != "" {
		t.Fatalf("backup = %q, want empty", backup)
	}

	// Existing regular file is backed up to <path>.bak with same content.
	content := "provider = \"ollama\"\n"
	if err := os.WriteFile(cfgPath, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	backup, err = BackupUserConfig(cfgPath)
	if err != nil {
		t.Fatalf("BackupUserConfig() error = %v", err)
	}
	if backup != cfgPath+".bak" {
		t.Fatalf("backup = %q, want %q", backup, cfgPath+".bak")
	}
	got, err := os.ReadFile(backup)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", backup, err)
	}
	if string(got) != content {
		t.Fatalf("backup content = %q, want %q", string(got), content)
	}

	// Reject symlinks.
	sym := filepath.Join(dir, "sym.toml")
	if err := os.Symlink(cfgPath, sym); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	if _, err := BackupUserConfig(sym); err == nil {
		t.Fatal("expected error when backing up a symlink")
	}
}
