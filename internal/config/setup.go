package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// UserConfigPath returns the path to the current user's qcode config.toml.
func UserConfigPath() (string, error) {
	return currentUserConfigPath()
}

// ResolveAPIKeyPath expands ~ and environment variables in raw and resolves
// relative paths against configDir (or CWD when configDir is empty).
func ResolveAPIKeyPath(raw, configDir string) (string, error) {
	return resolveAPIKeyPath(raw, configDir)
}

// NewRuntimePreferenceWriterForPath returns a writer targeting a specific
// config file path (primarily for testing).
func NewRuntimePreferenceWriterForPath(path string) *RuntimePreferenceWriter {
	return newRuntimePreferenceWriter(path)
}

// DefaultAPIKeyPath returns the default key-file path for a built-in provider
// (for example "~/.config/qcode/openai.key").
func DefaultAPIKeyPath(provider string) (string, error) {
	provider = strings.TrimSpace(provider)
	if !validInstanceName(provider) {
		return "", fmt.Errorf("invalid provider name %q", provider)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home directory: %w", err)
	}
	return filepath.Join(home, ".config", "qcode", provider+".key"), nil
}

// WriteAPIKeyFile writes a single-line API key atomically to path with 0600
// permissions, refusing symlinks and multi-line values.
func WriteAPIKeyFile(rawPath, key string) (string, error) {
	full, err := resolveAPIKeyPath(rawPath, "")
	if err != nil {
		return "", err
	}
	trimmed := strings.TrimSpace(key)
	if trimmed == "" {
		return "", errors.New("API key must not be empty")
	}
	if !utf8.ValidString(trimmed) {
		return "", errors.New("API key contains invalid UTF-8")
	}
	if strings.ContainsAny(trimmed, "\r\n") {
		return "", errors.New("API key must be a single line")
	}

	if info, err := os.Lstat(full); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("api key file %q must not be a symlink", full)
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("api key file %q must be a regular file", full)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	parent := filepath.Dir(full)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", fmt.Errorf("create key directory %s: %w", parent, err)
	}

	tmp, err := os.CreateTemp(parent, "."+filepath.Base(full)+".tmp-*")
	if err != nil {
		return "", fmt.Errorf("create temp key file: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("chmod temp key file: %w", err)
	}
	if _, err := tmp.WriteString(trimmed + "\n"); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("write temp key file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("sync temp key file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close temp key file: %w", err)
	}
	if err := os.Rename(tmpPath, full); err != nil {
		return "", fmt.Errorf("install key file %s: %w", full, err)
	}
	cleanup = false
	return full, nil
}

// PersistProviderConnection atomically updates [providers.<name>].base_url
// and/or [providers.<name>].api_key_path in the user config. Passing nil leaves
// that field untouched; passing a pointer to "" removes the field. When a
// non-empty api_key_path is set, any inline [providers.<name>].api_key is
// removed so `qcode config check` stays clean.
func (w *RuntimePreferenceWriter) PersistProviderConnection(provider string, baseURL, apiKeyPath *string) error {
	if w == nil {
		return errors.New("runtime preference writer is nil")
	}
	provider = strings.TrimSpace(provider)
	if !w.hasProvider(provider) {
		return fmt.Errorf("unknown provider %q", provider)
	}
	if baseURL != nil && !utf8.ValidString(*baseURL) {
		return errors.New("base_url contains invalid UTF-8")
	}
	if apiKeyPath != nil && !utf8.ValidString(*apiKeyPath) {
		return errors.New("api_key_path contains invalid UTF-8")
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if strings.TrimSpace(w.path) == "" {
		return errors.New("runtime preference path is empty")
	}
	path := filepath.Clean(w.path)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create user config directory %s: %w", filepath.Dir(path), err)
	}

	data, mode, exists, err := readRuntimeConfig(path)
	if err != nil {
		return err
	}

	changed := false
	if baseURL != nil {
		trimmed := strings.TrimSpace(*baseURL)
		updated, fieldChanged, err := patchProviderProfile(data, provider, "base_url", optionalTOMLString(trimmed))
		if err != nil {
			return fmt.Errorf("update config %s: %w", path, err)
		}
		data = updated
		changed = changed || fieldChanged
	}
	if apiKeyPath != nil {
		trimmed := strings.TrimSpace(*apiKeyPath)
		updated, fieldChanged, err := patchProviderProfile(data, provider, "api_key_path", optionalTOMLString(trimmed))
		if err != nil {
			return fmt.Errorf("update config %s: %w", path, err)
		}
		data = updated
		changed = changed || fieldChanged
		if trimmed != "" {
			updated, keyChanged, err := patchProviderProfile(data, provider, "api_key", nil)
			if err != nil {
				return fmt.Errorf("update config %s: %w", path, err)
			}
			data = updated
			changed = changed || keyChanged
		}
	}

	if !changed {
		return nil
	}
	if !exists {
		mode = 0o600
	}
	if err := writeRuntimeConfigAtomically(path, data, mode); err != nil {
		return fmt.Errorf("write config %s: %w", path, err)
	}
	return nil
}

// PersistSandbox records the Linux sandbox toggle in the user config.
func (w *RuntimePreferenceWriter) PersistSandbox(enabled bool) error {
	if w == nil {
		return errors.New("runtime preference writer is nil")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	val := "false"
	if enabled {
		val = "true"
	}
	return w.persist(map[string]*string{"sandbox": &val})
}

// PersistSandboxCommandPaths records sandbox_command_paths in the user config.
func (w *RuntimePreferenceWriter) PersistSandboxCommandPaths(paths []string) error {
	if w == nil {
		return errors.New("runtime preference writer is nil")
	}
	seen := make(map[string]bool, len(paths))
	encoded := make([]string, 0, len(paths))
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		if !utf8.ValidString(p) {
			return errors.New("sandbox_command_paths contains invalid UTF-8")
		}
		seen[p] = true
		encoded = append(encoded, tomlString(p))
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	literal := "[" + strings.Join(encoded, ", ") + "]"
	return w.persist(map[string]*string{"sandbox_command_paths": &literal})
}

// BackupUserConfig copies an existing regular config file at path to
// "<path>.bak" atomically with 0600 permissions. If path does not exist, it
// returns ("", nil). Symlinks and non-regular files are rejected.
func BackupUserConfig(path string) (string, error) {
	clean := filepath.Clean(strings.TrimSpace(path))
	if clean == "" || clean == "." {
		return "", errors.New("config path must not be empty")
	}
	info, err := os.Lstat(clean)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("stat config %s: %w", clean, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("config file %q must not be a symlink", clean)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("config file %q must be a regular file", clean)
	}
	data, err := os.ReadFile(clean)
	if err != nil {
		return "", fmt.Errorf("read config %s: %w", clean, err)
	}
	backupPath := clean + ".bak"
	if binfo, berr := os.Lstat(backupPath); berr == nil {
		if binfo.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("backup file %q must not be a symlink", backupPath)
		}
	}
	mode := info.Mode().Perm()
	if mode == 0 {
		mode = 0o600
	}
	if err := writeRuntimeConfigAtomically(backupPath, data, mode); err != nil {
		return "", fmt.Errorf("write backup %s: %w", backupPath, err)
	}
	return backupPath, nil
}
