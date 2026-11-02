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
	"qcode/internal/redaction"
	"qcode/internal/theme"
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
//
// AutoloadPaths is the legacy lazy-only form: every valid skill in those
// directories is selected at startup but stays lazy (catalog only).
// Autoload is the new representation: each entry marks a directory as
// lazy or always-load (full text eagerly injected). Both forms are
// additive across configuration layers.
type Skills struct {
	Paths         []string        `toml:"paths"`
	AutoloadPaths []string        `toml:"autoload_paths"`
	Autoload      []AutoloadEntry `toml:"autoload"`
}

// AutoloadEntry marks one autoload directory with its load mode.
type AutoloadEntry struct {
	Path       string `toml:"path"`
	AlwaysLoad bool   `toml:"always_load"`
}

// AutoloadPathsByMode splits the merged autoload configuration into lazy
// directories and always-load directories. Legacy autoload_paths entries
// are always lazy. Entries are trimmed, deduped, and order-stable.
func (s Skills) AutoloadPathsByMode() (lazy []string, always []string) {
	seenLazy := map[string]bool{}
	seenAlways := map[string]bool{}
	add := func(dst *[]string, seen map[string]bool, path string) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		if seen[path] {
			return
		}
		seen[path] = true
		*dst = append(*dst, path)
	}
	for _, path := range s.AutoloadPaths {
		add(&lazy, seenLazy, path)
	}
	for _, entry := range s.Autoload {
		if strings.TrimSpace(entry.Path) == "" {
			continue
		}
		if entry.AlwaysLoad {
			add(&always, seenAlways, entry.Path)
		} else {
			add(&lazy, seenLazy, entry.Path)
		}
	}
	return lazy, always
}

// AllAutoloadPaths returns every autoload directory (lazy + always) in
// discovery order: legacy lazy paths first, then the new table entries.
func (s Skills) AllAutoloadPaths() []string {
	lazy, always := s.AutoloadPathsByMode()
	return append(append([]string(nil), lazy...), always...)
}

// Config contains settings that may be supplied by a qcode config file.
type Config struct {
	Redaction            redaction.Config `toml:"redaction"`
	Learning             Learning         `toml:"learning"`
	Skills               Skills           `toml:"skills"`
	WebSearch            WebSearch        `toml:"web_search"`
	Provider             string           `toml:"provider"`
	BaseURL              string           `toml:"base_url"`
	APIKey               string           `toml:"api_key"`
	Model                string           `toml:"model"`
	Thinking             string           `toml:"thinking"`
	Interactive          *bool            `toml:"interactive"`
	ContextWindow        *int             `toml:"context_window"`
	AutoCompactThreshold *int             `toml:"auto_compact_threshold"`
	DisableAutoCompact   *bool            `toml:"disable_auto_compact"`
	MaxSteps             *int             `toml:"max_steps"`
	AgentTimeout         *string          `toml:"agent_timeout"`
	Sandbox              *bool            `toml:"sandbox"`
	SandboxCommandPaths  []string         `toml:"sandbox_command_paths"`
	DangerSkipTLSVerify  *bool            `toml:"danger_skip_tls_verify"`
	StatuslineHidden     []string         `toml:"statusline_hidden"`
	Theme                string           `toml:"theme"`
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
	for _, diagnostic := range diagnostics {
		var invalid *redaction.InvalidPatternError
		if errors.As(diagnostic, &invalid) {
			return Config{}, paths, diagnostics, diagnostic
		}
	}
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
	if err := redaction.Validate(cfg.Redaction); err != nil {
		return fmt.Errorf("parse config %s: %w", path, err)
	}
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
	if cfg.StatuslineHidden != nil {
		for _, segment := range cfg.StatuslineHidden {
			if !validStatuslineSegment(segment) {
				return fmt.Errorf("parse config %s: statusline_hidden contains unknown segment %q (want one of remote, mode, model, think, ws, ctx, step, tok)", path, segment)
			}
		}
	}
	if cfg.Theme != "" && !theme.ValidID(cfg.Theme) {
		var ids []string
		for _, palette := range theme.All() {
			ids = append(ids, palette.ID)
		}
		return fmt.Errorf("parse config %s: theme must be one of %s", path, strings.Join(ids, ", "))
	}
	return nil
}

// validStatuslineSegment reports whether a statusline_hidden entry names a
// known status bar segment. Matching is case-insensitive; surrounding spaces
// are ignored so TOML lists stay forgiving.
func validStatuslineSegment(segment string) bool {
	switch strings.ToLower(strings.TrimSpace(segment)) {
	case "remote", "mode", "model", "think", "ws", "ctx", "step", "tok":
		return true
	}
	return false
}

// merge overlays values supplied by incoming onto dst. Empty string fields are
// intentionally unset, while skill paths are additive across layers.
// Sandbox command paths use override semantics: a higher-priority layer with a
// non-nil list (including an explicit empty list) replaces lower layers, so a
// user can narrow or clear system-wide defaults. Repeatable
// --sandbox-command-path flags override the merged configuration entirely.
func merge(dst *Config, incoming Config) {
	mergeRedaction(&dst.Redaction, incoming.Redaction)
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
	if incoming.StatuslineHidden != nil {
		dst.StatuslineHidden = append([]string(nil), incoming.StatuslineHidden...)
	}
	if incoming.Theme != "" {
		dst.Theme = incoming.Theme
	}
	dst.Skills.Paths = appendUniquePaths(dst.Skills.Paths, incoming.Skills.Paths)
	dst.Skills.AutoloadPaths = appendUniquePaths(dst.Skills.AutoloadPaths, incoming.Skills.AutoloadPaths)
	dst.Skills.Autoload = appendUniqueAutoload(dst.Skills.Autoload, incoming.Skills.Autoload)
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

func appendUniqueAutoload(existing, incoming []AutoloadEntry) []AutoloadEntry {
	seen := make(map[string]int, len(existing))
	for i, entry := range existing {
		seen[strings.TrimSpace(entry.Path)] = i
	}
	for _, entry := range incoming {
		entry.Path = strings.TrimSpace(entry.Path)
		if entry.Path == "" {
			continue
		}
		if idx, ok := seen[entry.Path]; ok {
			// An always-load mark is sticky across layers.
			existing[idx].AlwaysLoad = existing[idx].AlwaysLoad || entry.AlwaysLoad
			continue
		}
		seen[entry.Path] = len(existing)
		existing = append(existing, entry)
	}
	return existing
}

func mergeRedaction(dst *redaction.Config, src redaction.Config) {
	for _, pair := range []struct {
		dst **bool
		src *bool
	}{{&dst.Terminal, src.Terminal}, {&dst.Persistence, src.Persistence}, {&dst.Exports, src.Exports}, {&dst.JSONEvents, src.JSONEvents}, {&dst.Remote, src.Remote}} {
		if pair.src != nil {
			*pair.dst = pair.src
		}
	}
	for _, pair := range []struct {
		dst *[]string
		src []string
	}{{&dst.CustomPatterns, src.CustomPatterns}, {&dst.SensitivePaths, src.SensitivePaths}, {&dst.SensitiveFields, src.SensitiveFields}} {
		if pair.src != nil {
			*pair.dst = append([]string{}, pair.src...)
		}
	}
}
