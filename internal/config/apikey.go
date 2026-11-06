package config

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"unicode/utf8"
)

// maxAPIKeyFileSize bounds api_key_path files; keys are short.
const maxAPIKeyFileSize = 64 * 1024

// resolveAPIKeyPath expands ~ and environment variables in raw and returns an
// absolute path. Relative paths resolve from the directory holding the config
// file that declared them so layers stay relocatable.
func resolveAPIKeyPath(raw, configDir string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("api_key_path must not be empty")
	}
	if !utf8.ValidString(trimmed) {
		return "", fmt.Errorf("api_key_path contains invalid UTF-8")
	}
	expanded := os.ExpandEnv(trimmed)
	if expanded == "~" || strings.HasPrefix(expanded, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("expand api_key_path: %w", err)
		}
		if expanded == "~" {
			expanded = home
		} else {
			expanded = filepath.Join(home, strings.TrimPrefix(expanded, "~/"))
		}
	}
	if !filepath.IsAbs(expanded) {
		if strings.TrimSpace(configDir) == "" {
			abs, err := filepath.Abs(expanded)
			if err != nil {
				return "", fmt.Errorf("resolve api_key_path: %w", err)
			}
			return abs, nil
		}
		return filepath.Join(configDir, expanded), nil
	}
	return filepath.Clean(expanded), nil
}

func checkAPIKeyFile(stat os.FileInfo, path string) error {
	if stat.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("api key file %q must not be a symlink", path)
	}
	if !stat.Mode().IsRegular() {
		return fmt.Errorf("api key file %q must be a regular file", path)
	}
	if perm := stat.Mode().Perm(); perm != 0600 && perm != 0400 {
		return fmt.Errorf("api key file %q must have mode 0600 or 0400 (has %04o)", path, perm)
	}
	if runtime.GOOS == "linux" {
		native, ok := stat.Sys().(*syscall.Stat_t)
		if !ok {
			return fmt.Errorf("api key file %q ownership unknown", path)
		}
		if native.Uid != uint32(os.Getuid()) {
			return fmt.Errorf("api key file %q must be owned by the current user", path)
		}
	}
	if stat.Size() > maxAPIKeyFileSize {
		return fmt.Errorf("api key file %q exceeds %d bytes", path, maxAPIKeyFileSize)
	}
	return nil
}

// loadAPIKeyFile validates the file at full and returns its trimmed content.
// The file must be owner-only (0600/0400), not a symlink, and hold a nonempty
// single credential. Double-stat (Lstat + Fstat) guards symlink swaps.
func loadAPIKeyFile(full string) (string, error) {
	link, err := os.Lstat(full)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("api key file %q not found; create it with mode 0600 holding only the key", full)
		}
		return "", fmt.Errorf("api key file %q: %w", full, err)
	}
	if err := checkAPIKeyFile(link, full); err != nil {
		return "", err
	}
	f, err := os.Open(full)
	if err != nil {
		return "", fmt.Errorf("api key file %q: %w", full, err)
	}
	defer f.Close()
	live, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("api key file %q: %w", full, err)
	}
	if err := checkAPIKeyFile(live, full); err != nil {
		return "", err
	}
	data, err := io.ReadAll(io.LimitReader(f, maxAPIKeyFileSize+1))
	if err != nil {
		return "", fmt.Errorf("api key file %q: %w", full, err)
	}
	key := strings.TrimSpace(string(data))
	if key == "" {
		return "", fmt.Errorf("api key file %q is empty; write only the key (no quotes)", full)
	}
	if !utf8.ValidString(key) {
		return "", fmt.Errorf("api key file %q contains invalid UTF-8", full)
	}
	if strings.Contains(key, "\n") {
		// Take the first non-empty line to tolerate a trailing newline, but
		// reject multi-line files that likely pasted extra content.
		lines := strings.Split(key, "\n")
		nonempty := make([]string, 0, len(lines))
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line != "" {
				nonempty = append(nonempty, line)
			}
		}
		if len(nonempty) != 1 {
			return "", fmt.Errorf("api key file %q must hold exactly one key (one non-empty line)", full)
		}
		key = nonempty[0]
	}
	return key, nil
}

// validateProfileCredentials rejects ambiguous credential sources. Inline
// api_key and api_key_path must not both be set; an explicit empty string
// clears and does not count as set.
func validateProfileCredentials(path, name string, profile ProviderProfile) error {
	hasInline := profile.APIKey != nil && *profile.APIKey != ""
	hasPath := profile.APIKeyPath != nil && strings.TrimSpace(*profile.APIKeyPath) != ""
	if hasInline && hasPath {
		return fmt.Errorf("parse config %s: providers.%s has both api_key and api_key_path; remove one (prefer api_key_path): %w", path, name, errFatalProviderConfig)
	}
	if profile.APIKeyPath != nil && !utf8.ValidString(*profile.APIKeyPath) {
		return fmt.Errorf("parse config %s: providers.%s.api_key_path contains invalid UTF-8", path, name)
	}
	return nil
}

// resolveProfileAPIKey returns the file content when APIKeyPath is set.
// An empty or nil path means no file key. An error describes the key path
// without exposing the secret.
func resolveProfileAPIKey(profile ProviderProfile, configDir string) (string, error) {
	if profile.APIKeyPath == nil {
		return "", nil
	}
	if strings.TrimSpace(*profile.APIKeyPath) == "" {
		// Explicit empty clears a lower-priority path entry.
		return "", nil
	}
	full, err := resolveAPIKeyPath(*profile.APIKeyPath, configDir)
	if err != nil {
		return "", err
	}
	return loadAPIKeyFile(full)
}
