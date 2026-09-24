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
	"time"

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
	Paths         []string `toml:"paths"`
	AutoloadPaths []string `toml:"autoload_paths"`
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
	Thinking             string    `toml:"thinking"`
	Interactive          *bool     `toml:"interactive"`
	ContextWindow        *int      `toml:"context_window"`
	AutoCompactThreshold *int      `toml:"auto_compact_threshold"`
	DisableAutoCompact   *bool     `toml:"disable_auto_compact"`
	MaxSteps             *int      `toml:"max_steps"`
	AgentTimeout         *string   `toml:"agent_timeout"`
	Sandbox              *bool     `toml:"sandbox"`
	SandboxCommandPaths  []string  `toml:"sandbox_command_paths"`
	DangerSkipTLSVerify  *bool     `toml:"danger_skip_tls_verify"`
}

// Load returns the configuration assembled from every existing configuration
// file. Candidate paths are listed from highest to lowest priority, so layers
// are applied in reverse order. paths contains every existing file inspected;
// diagnostics describe files that were skipped without preventing other
// layers from loading.
func Load() (Config, []string, []error, error) {
	executable, err := os.Executable()
	if err != nil {
		return Config{}, nil, nil, fmt.Errorf("locate executable: %w", err)
	}
	home := ""
	userConfigDir := ""
	if runtime.GOOS == "linux" {
		home, err = os.UserHomeDir()
		if err != nil {
			return Config{}, nil, nil, fmt.Errorf("locate home directory: %w", err)
		}
	} else {
		userConfigDir, err = os.UserConfigDir()
		if err != nil {
			return Config{}, nil, nil, fmt.Errorf("locate user config directory: %w", err)
		}
	}
	cfg, paths, diagnostics := load(candidatePaths(runtime.GOOS, executable, home, userConfigDir, os.Getenv("ProgramData")))
	return cfg, paths, diagnostics, nil
}

func candidatePaths(goos, executable, home, userConfigDir, programData string) []string {
	executableDir := filepath.Dir(executable)
	paths := []string{filepath.Join(executableDir, fileName)}
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
	paths = append(paths, filepath.Join(executableDir, "etc", fileName))
	return paths
}

func load(paths []string) (Config, []string, []error) {
	var merged Config
	var inspected []string
	var diagnostics []error
	for i := len(paths) - 1; i >= 0; i-- {
		path := paths[i]
		data, err := os.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		inspected = append(inspected, path)
		if err != nil {
			diagnostics = append(diagnostics, fmt.Errorf("read config %s: %w", path, err))
			continue
		}

		var cfg Config
		decoder := toml.NewDecoder(strings.NewReader(string(data)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&cfg); err != nil {
			diagnostics = append(diagnostics, fmt.Errorf("parse config %s: %w", path, err))
			continue
		}
		if err := validate(path, cfg); err != nil {
			diagnostics = append(diagnostics, err)
			continue
		}
		merge(&merged, cfg)
	}
	return merged, inspected, diagnostics
}

func validate(path string, cfg Config) error {
	if cfg.Learning.ContextBudget != nil && (*cfg.Learning.ContextBudget < 0 || *cfg.Learning.ContextBudget > 12000) {
		return fmt.Errorf("parse config %s: learning.context_budget must be between 0 and 12000", path)
	}
	if cfg.ContextWindow != nil && *cfg.ContextWindow < 0 {
		return fmt.Errorf("parse config %s: context_window must be non-negative", path)
	}
	if cfg.AutoCompactThreshold != nil && (*cfg.AutoCompactThreshold < 1 || *cfg.AutoCompactThreshold > 99) {
		return fmt.Errorf("parse config %s: auto_compact_threshold must be between 1 and 99", path)
	}
	if cfg.MaxSteps != nil && *cfg.MaxSteps <= 0 {
		return fmt.Errorf("parse config %s: max_steps must be greater than zero", path)
	}
	if cfg.AgentTimeout != nil {
		duration, err := time.ParseDuration(*cfg.AgentTimeout)
		if err != nil || duration <= 0 {
			return fmt.Errorf("parse config %s: agent_timeout must be a positive duration, such as \"5m\"", path)
		}
	}
	return nil
}

// merge overlays values supplied by incoming onto dst. Empty string fields are
// intentionally unset, while skill paths are additive across layers.
// Sandbox command paths use override semantics: a higher-priority layer with a
// non-nil list (including an explicit empty list) replaces lower layers, so a
// user can narrow or clear system-wide defaults. Repeatable
// --sandbox-command-path flags override the merged configuration entirely.
func merge(dst *Config, incoming Config) {
	if incoming.Learning.ContextBudget != nil {
		dst.Learning.ContextBudget = incoming.Learning.ContextBudget
	}
	if incoming.WebSearch.Backend != "" {
		dst.WebSearch.Backend = incoming.WebSearch.Backend
	}
	if incoming.Provider != "" {
		dst.Provider = incoming.Provider
	}
	if incoming.BaseURL != "" {
		dst.BaseURL = incoming.BaseURL
	}
	if incoming.APIKey != "" {
		dst.APIKey = incoming.APIKey
	}
	if incoming.Model != "" {
		dst.Model = incoming.Model
	}
	if incoming.Thinking != "" {
		dst.Thinking = incoming.Thinking
	}
	if incoming.Interactive != nil {
		dst.Interactive = incoming.Interactive
	}
	if incoming.ContextWindow != nil {
		dst.ContextWindow = incoming.ContextWindow
	}
	if incoming.AutoCompactThreshold != nil {
		dst.AutoCompactThreshold = incoming.AutoCompactThreshold
	}
	if incoming.DisableAutoCompact != nil {
		dst.DisableAutoCompact = incoming.DisableAutoCompact
	}
	if incoming.MaxSteps != nil {
		dst.MaxSteps = incoming.MaxSteps
	}
	if incoming.AgentTimeout != nil {
		dst.AgentTimeout = incoming.AgentTimeout
	}
	if incoming.Sandbox != nil {
		dst.Sandbox = incoming.Sandbox
	}
	if incoming.SandboxCommandPaths != nil {
		dst.SandboxCommandPaths = append([]string(nil), incoming.SandboxCommandPaths...)
	}
	if incoming.DangerSkipTLSVerify != nil {
		dst.DangerSkipTLSVerify = incoming.DangerSkipTLSVerify
	}
	dst.Skills.Paths = appendUniquePaths(dst.Skills.Paths, incoming.Skills.Paths)
	dst.Skills.AutoloadPaths = appendUniquePaths(dst.Skills.AutoloadPaths, incoming.Skills.AutoloadPaths)
}

func appendUniquePaths(existing, incoming []string) []string {
	seen := make(map[string]struct{}, len(existing))
	for _, path := range existing {
		seen[path] = struct{}{}
	}
	for _, path := range incoming {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		existing = append(existing, path)
	}
	return existing
}
