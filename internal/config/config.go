// Package config loads qcode's optional TOML configuration file.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

const fileName = "config.toml"

// WebSearch selects the search backend at startup.
type WebSearch struct {
	Backend string `toml:"backend"`
}

type Learning struct {
	ContextBudget *int `toml:"context_budget"`
}

// Skills configures additional directories containing skill folders. Each
// directory should contain <name>/SKILL.md entries.
type Skills struct {
	Paths []string `toml:"paths"`
}

// Config contains settings that may be supplied by a qcode config file.
type Config struct {
	Learning             Learning  `toml:"learning"`
	Skills               Skills    `toml:"skills"`
	WebSearch            WebSearch `toml:"web_search"`
	Provider             string    `toml:"provider"`
	BaseURL              string    `toml:"base_url"`
	APIKey               string    `toml:"api_key"`
	Model                string    `toml:"model"`
	ContextWindow        *int      `toml:"context_window"`
	AutoCompactThreshold *int      `toml:"auto_compact_threshold"`
	DisableAutoCompact   *bool     `toml:"disable_auto_compact"`
	MaxSteps             *int      `toml:"max_steps"`
	Sandbox              *bool     `toml:"sandbox"`
	DangerSkipTLSVerify  *bool     `toml:"danger_skip_tls_verify"`
}

// Load returns the first configuration found in lookup order. An empty path
// means no configuration file exists.
func Load() (Config, string, error) {
	executable, err := os.Executable()
	if err != nil {
		return Config{}, "", fmt.Errorf("locate executable: %w", err)
	}
	home := ""
	userConfigDir := ""
	if runtime.GOOS == "linux" {
		home, err = os.UserHomeDir()
		if err != nil {
			return Config{}, "", fmt.Errorf("locate home directory: %w", err)
		}
	} else {
		userConfigDir, err = os.UserConfigDir()
		if err != nil {
			return Config{}, "", fmt.Errorf("locate user config directory: %w", err)
		}
	}
	return load(candidatePaths(runtime.GOOS, executable, home, userConfigDir, os.Getenv("ProgramData")))
}

func candidatePaths(goos, executable, home, userConfigDir, programData string) []string {
	paths := []string{filepath.Join(filepath.Dir(executable), fileName)}
	switch goos {
	case "linux":
		paths = append(paths,
			filepath.Join(home, ".local", "etc", "qcode", fileName),
			filepath.Join(string(filepath.Separator), "usr", "local", "etc", "qcode", fileName),
		)
	case "darwin":
		paths = append(paths,
			filepath.Join(userConfigDir, "qcode", fileName),
			filepath.Join(string(filepath.Separator), "Library", "Application Support", "qcode", fileName),
		)
	case "windows":
		paths = append(paths, filepath.Join(userConfigDir, "qcode", fileName))
		if programData != "" {
			paths = append(paths, filepath.Join(programData, "qcode", fileName))
		}
	default:
		paths = append(paths,
			filepath.Join(userConfigDir, "qcode", fileName),
			filepath.Join(string(filepath.Separator), "usr", "local", "etc", "qcode", fileName),
		)
	}
	return paths
}

func load(paths []string) (Config, string, error) {
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return Config{}, path, fmt.Errorf("read config %s: %w", path, err)
		}

		var cfg Config
		decoder := toml.NewDecoder(strings.NewReader(string(data)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&cfg); err != nil {
			return Config{}, path, fmt.Errorf("parse config %s: %w", path, err)
		}
		if cfg.Learning.ContextBudget != nil && (*cfg.Learning.ContextBudget < 0 || *cfg.Learning.ContextBudget > 12000) {
			return Config{}, path, fmt.Errorf("parse config %s: learning.context_budget must be between 0 and 12000", path)
		}
		if cfg.ContextWindow != nil && *cfg.ContextWindow < 0 {
			return Config{}, path, fmt.Errorf("parse config %s: context_window must be non-negative", path)
		}
		if cfg.AutoCompactThreshold != nil && (*cfg.AutoCompactThreshold < 1 || *cfg.AutoCompactThreshold > 99) {
			return Config{}, path, fmt.Errorf("parse config %s: auto_compact_threshold must be between 1 and 99", path)
		}
		if cfg.MaxSteps != nil && *cfg.MaxSteps <= 0 {
			return Config{}, path, fmt.Errorf("parse config %s: max_steps must be greater than zero", path)
		}
		return cfg, path, nil
	}
	return Config{}, "", nil
}
