package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/pelletier/go-toml/v2"
)

// Severity grades a config finding. Errors prevent reliable startup for the
// affected provider (or fail the check); warnings run today but use a
// deprecated shape that may be removed.
type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
)

// Issue is one human-fixable config finding. File is the config file holding
// the problem ("(merged)" for cross-layer conflicts). Key is the dotted key
// path such as "providers.openai.api_key_path" or "provider_models.openai".
// Message says what is wrong without printing secrets; Fix says how to edit
// the file by hand.
type Issue struct {
	Severity Severity `json:"severity"`
	File     string   `json:"file"`
	Key      string   `json:"key"`
	Message  string   `json:"message"`
	Fix      string   `json:"fix"`
}

func (i Issue) String() string {
	where := i.File
	if i.Key != "" {
		where += " " + i.Key
	}
	return fmt.Sprintf("%s %s: %s Fix: %s", strings.ToUpper(string(i.Severity)), where, i.Message, i.Fix)
}

// HasErrors reports whether any issue is an error.
func HasErrors(issues []Issue) bool {
	for _, issue := range issues {
		if issue.Severity == SeverityError {
			return true
		}
	}
	return false
}

// defaultCandidatePaths mirrors Load's discovery so `config check` inspects
// the same layers startup would merge.
func defaultCandidatePaths() ([]string, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locate executable: %w", err)
	}
	home := ""
	userConfigDir := ""
	if runtime.GOOS == "linux" {
		home, err = os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("locate home directory: %w", err)
		}
	} else {
		userConfigDir, err = os.UserConfigDir()
		if err != nil {
			return nil, fmt.Errorf("locate user config directory: %w", err)
		}
	}
	return candidatePaths(runtime.GOOS, executable, home, userConfigDir, os.Getenv("ProgramData")), nil
}

// CheckDefault inspects every existing candidate config file plus their merged
// result. It returns human-fixable issues and the existing files inspected.
func CheckDefault() ([]Issue, []string, error) {
	paths, err := defaultCandidatePaths()
	if err != nil {
		return nil, nil, err
	}
	return CheckPaths(paths), existingFiles(paths), nil
}

func existingFiles(paths []string) []string {
	var out []string
	for _, path := range paths {
		if _, err := os.Stat(path); err == nil {
			out = append(out, path)
		}
	}
	return out
}

// CheckPaths inspects the given candidate paths (highest to lowest priority,
// like load) and their merged result. Missing files are ignored. Dotted keys
// (providers.openai.api_key) and table form ([providers.openai]) decode
// identically, so style alone never warns.
func CheckPaths(paths []string) []Issue {
	var issues []Issue
	type layer struct {
		path string
		cfg  Config
		data []byte
	}
	var layers []layer
	// Per-file checks first.
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			issues = append(issues, Issue{
				Severity: SeverityError,
				File:     path,
				Key:      "",
				Message:  fmt.Sprintf("cannot read file: %s", err),
				Fix:      fmt.Sprintf("Make %s readable (check permissions and owner).", path),
			})
			continue
		}
		cfg, err := decodeConfig(data, true)
		if err != nil {
			issues = append(issues, Issue{
				Severity: SeverityError,
				File:     path,
				Key:      keyFromDecodeError(err),
				Message:  fmt.Sprintf("invalid TOML or unknown setting: %s", err),
				Fix:      "Fix TOML syntax, remove unknown fields, and keep [providers.<name>] tables valid. See config.toml.example for the supported shape.",
			})
			continue
		}
		if err := validate(path, cfg); err != nil {
			issues = append(issues, validateErrorToIssue(path, err))
			// Still collect warnings from the raw layer? No: an invalid layer
			// is skipped at startup, so warnings would be noise. Continue to
			// keep parity with load() skipping invalid layers.
			continue
		}
		// api_key_path file checks per layer (relative to declaring file).
		for name, profile := range cfg.Providers {
			if profile.APIKeyPath == nil || strings.TrimSpace(*profile.APIKeyPath) == "" {
				continue
			}
			full, err := resolveAPIKeyPath(*profile.APIKeyPath, filepath.Dir(path))
			display := strings.TrimSpace(*profile.APIKeyPath)
			if err != nil {
				issues = append(issues, Issue{
					Severity: SeverityError,
					File:     path,
					Key:      "providers." + name + ".api_key_path",
					Message:  fmt.Sprintf("bad path %q: %s", display, err),
					Fix:      fmt.Sprintf("Set providers.%s.api_key_path to an absolute path or ~/... (for example \"~/.config/qcode/%s.key\").", name, name),
				})
				continue
			}
			if _, err := loadAPIKeyFile(full); err != nil {
				issues = append(issues, Issue{
					Severity: SeverityError,
					File:     path,
					Key:      "providers." + name + ".api_key_path",
					Message:  describeKeyFileError(display, full, err),
					Fix:      fixForKeyFileError(full, err),
				})
			}
		}
		// Warnings: deprecated shapes that still run.
		issues = append(issues, deprecationWarnings(path, data, cfg)...)
		issues = append(issues, allowModelsWarnings(path, cfg)...)
		layers = append(layers, layer{path: path, cfg: cfg, data: data})
	}
	// Merged cross-layer checks (only over valid layers, like load).
	if len(layers) == 0 {
		return sortIssues(issues)
	}
	var merged Config
	apiKeyPathDirs := map[string]string{}
	// layers are in candidate order (highest first); merge low->high.
	for i := len(layers) - 1; i >= 0; i-- {
		// Re-apply legacy binding like load() so merged both-keys detection
		// sees the same effective profiles. Legacy root credentials are
		// already covered by per-file warnings; here we only need the merge
		// for cross-layer api_key/api_key_path conflicts.
		merge(&merged, layers[i].cfg)
		for name, profile := range layers[i].cfg.Providers {
			if profile.APIKeyPath != nil {
				apiKeyPathDirs[name] = filepath.Dir(layers[i].path)
			}
		}
		// Legacy root bindings also merge (same as load, without global fallbacks
		// needed for this check).
		var raw struct {
			BaseURL *string `toml:"base_url"`
			APIKey  *string `toml:"api_key"`
		}
		_ = toml.Unmarshal(layers[i].data, &raw)
		if raw.BaseURL != nil || raw.APIKey != nil {
			owner := layers[i].cfg.Provider
			if owner == "" {
				owner = merged.Provider
			}
			if owner == "" {
				owner = "ollama"
			}
			merge(&merged, Config{Providers: map[string]ProviderProfile{owner: {BaseURL: raw.BaseURL, APIKey: raw.APIKey}}})
		}
	}
	for name, profile := range merged.Providers {
		hasInline := profile.APIKey != nil && *profile.APIKey != ""
		hasPath := profile.APIKeyPath != nil && strings.TrimSpace(*profile.APIKeyPath) != ""
		if hasInline && hasPath {
			issues = append(issues, Issue{
				Severity: SeverityError,
				File:     "(merged)",
				Key:      "providers." + name + ".api_key_path",
				Message:  fmt.Sprintf("providers.%s sets both api_key (inline) and api_key_path across merged layers", name),
				Fix:      fmt.Sprintf("Keep one source: set [providers.%s] api_key_path (for example \"~/.config/qcode/%s.key\") and remove every api_key for %s, or rely on QCODE_API_KEY/--api-key instead.", name, name, name),
			})
		}
		_ = apiKeyPathDirs
	}
	return sortIssues(issues)
}

// allowModelsWarnings reports human-fixable roster issues without failing
// startup. Empty model, unknown role/provider, and invalid UTF-8 warn;
// stale thinking_level keys are ignored (main decides thinking).
func allowModelsWarnings(path string, cfg Config) []Issue {
	if cfg.Agents.AllowModels == nil {
		return nil
	}
	var out []Issue
	for i, raw := range cfg.Agents.AllowModels {
		base := "agents.allow_models"
		provider := strings.TrimSpace(raw.Provider)
		model := strings.TrimSpace(raw.Model)
		role := strings.TrimSpace(raw.Role)
		if !utf8.ValidString(raw.Provider) || !utf8.ValidString(raw.Model) || !utf8.ValidString(raw.Role) {
			out = append(out, Issue{
				Severity: SeverityWarning,
				File:     path,
				Key:      base,
				Message:  "allow_models entry contains invalid UTF-8 (entry ignored)",
				Fix:      "Use plain UTF-8 for provider, model, and role; see config.toml.example.",
			})
			continue
		}
		if provider == "" || !validProvider(provider) {
			out = append(out, Issue{
				Severity: SeverityWarning,
				File:     path,
				Key:      base,
				Message:  "allow_models entry has unknown or empty provider (entry ignored)",
				Fix:      "Set provider to one of ollama, openai, opencode-go, chatgpt.",
			})
		}
		if model == "" {
			out = append(out, Issue{
				Severity: SeverityWarning,
				File:     path,
				Key:      base,
				Message:  "allow_models entry has empty model (entry ignored)",
				Fix:      "Set model to a provider model ID, for example \"gpt-5\".",
			})
		}
		if !validAgentRole(strings.ToLower(role)) {
			out = append(out, Issue{
				Severity: SeverityWarning,
				File:     path,
				Key:      base,
				Message:  "allow_models entry has unknown role (entry ignored)",
				Fix:      "Set role to one of smart, balanced, cheap.",
			})
		}
		if strings.TrimSpace(raw.ThinkingLevel) != "" {
			out = append(out, Issue{
				Severity: SeverityWarning,
				File:     path,
				Key:      base,
				Message:  "allow_models thinking_level is ignored; main decides thinking at create_agent time",
				Fix:      "Remove thinking_level from [[agents.allow_models]]; main picks thinking_level from live capability data.",
			})
		}
		_ = i
	}
	return out
}

func sortIssues(issues []Issue) []Issue {
	sort.Slice(issues, func(i, j int) bool {
		if issues[i].File != issues[j].File {
			return issues[i].File < issues[j].File
		}
		if issues[i].Key != issues[j].Key {
			return issues[i].Key < issues[j].Key
		}
		if issues[i].Severity != issues[j].Severity {
			return issues[i].Severity < issues[j].Severity
		}
		return issues[i].Message < issues[j].Message
	})
	return issues
}

// deprecationWarnings lists runnable-but-deprecated shapes with file + key +
// hand-fix. Dotted vs table style is parser-equivalent and never warns.
func deprecationWarnings(path string, data []byte, cfg Config) []Issue {
	var out []Issue
	var raw struct {
		BaseURL  *string `toml:"base_url"`
		APIKey   *string `toml:"api_key"`
		Model    *string `toml:"model"`
		Thinking *string `toml:"thinking"`
		Provider *string `toml:"provider"`
	}
	_ = toml.Unmarshal(data, &raw)
	owner := ""
	if raw.Provider != nil && strings.TrimSpace(*raw.Provider) != "" {
		owner = strings.TrimSpace(*raw.Provider)
	} else if strings.TrimSpace(cfg.Provider) != "" {
		owner = strings.TrimSpace(cfg.Provider)
	}
	ownerHint := owner
	if ownerHint == "" {
		ownerHint = "<owner>"
	}
	if raw.BaseURL != nil {
		fix := fmt.Sprintf("Move base_url to [providers.%s] base_url and remove root base_url.", ownerHint)
		if owner == "" {
			fix = "Move base_url under its provider, for example [providers.openai] base_url, and remove root base_url. Set provider explicitly if the owner is unclear."
		}
		out = append(out, Issue{
			Severity: SeverityWarning,
			File:     path,
			Key:      "base_url",
			Message:  "root base_url is deprecated; connection settings belong under [providers.<name>]",
			Fix:      fix,
		})
	}
	if raw.APIKey != nil {
		fix := fmt.Sprintf("Move api_key to [providers.%s] api_key_path (preferred) or env QCODE_API_KEY, then remove root api_key.", ownerHint)
		if owner == "" {
			fix = "Move api_key under its provider as api_key_path (for example [providers.openai] api_key_path = \"~/.config/qcode/openai.key\") or env QCODE_API_KEY, then remove root api_key."
		}
		out = append(out, Issue{
			Severity: SeverityWarning,
			File:     path,
			Key:      "api_key",
			Message:  "root api_key is deprecated and stores a secret in config; use per-provider api_key_path or env",
			Fix:      fix,
		})
	}
	if raw.Model != nil {
		fix := fmt.Sprintf("Move model to [providers.%s] model and remove root model.", ownerHint)
		if owner == "" {
			fix = "Move model under its provider, for example [providers.openai] model, and remove root model."
		}
		out = append(out, Issue{
			Severity: SeverityWarning,
			File:     path,
			Key:      "model",
			Message:  "root model is deprecated; set [providers.<name>] model per provider",
			Fix:      fix,
		})
	}
	if raw.Thinking != nil {
		fix := fmt.Sprintf("Move thinking to [providers.%s] thinking and remove root thinking.", ownerHint)
		if owner == "" {
			fix = "Move thinking under its provider, for example [providers.openai] thinking, and remove root thinking."
		}
		out = append(out, Issue{
			Severity: SeverityWarning,
			File:     path,
			Key:      "thinking",
			Message:  "root thinking is deprecated; set [providers.<name>] thinking per provider",
			Fix:      fix,
		})
	}
	for name, profile := range cfg.Providers {
		if profile.APIKey != nil && *profile.APIKey != "" {
			out = append(out, Issue{
				Severity: SeverityWarning,
				File:     path,
				Key:      "providers." + name + ".api_key",
				Message:  "inline api_key stores a secret in config (deprecated)",
				Fix:      fmt.Sprintf("Replace with [providers.%s] api_key_path = \"~/.config/qcode/%s.key\" (file mode 0600, one line, no quotes) or env QCODE_API_KEY/--api-key, then remove the inline api_key.", name, name),
			})
		}
	}
	for name := range cfg.ProviderModels {
		out = append(out, Issue{
			Severity: SeverityWarning,
			File:     path,
			Key:      "provider_models." + name,
			Message:  "provider_models is deprecated; per-provider defaults belong under [providers.<name>] model",
			Fix:      fmt.Sprintf("Move provider_models.%s to [providers.%s] model and remove the provider_models entry.", name, name),
		})
	}
	for name := range cfg.ProviderThinking {
		// Empty thinking entries are removal markers, not deprecated use.
		if strings.TrimSpace(cfg.ProviderThinking[name]) == "" {
			continue
		}
		out = append(out, Issue{
			Severity: SeverityWarning,
			File:     path,
			Key:      "provider_thinking." + name,
			Message:  "provider_thinking is deprecated; per-provider defaults belong under [providers.<name>] thinking",
			Fix:      fmt.Sprintf("Move provider_thinking.%s to [providers.%s] thinking and remove the provider_thinking entry.", name, name),
		})
	}
	return out
}

func keyFromDecodeError(err error) string {
	msg := err.Error()
	// go-toml strict errors mention the key; surface a dotted path when present.
	for _, key := range []string{"providers", "provider_models", "provider_thinking", "redaction", "learning", "skills", "agents", "web_search", "chatgpt"} {
		if strings.Contains(msg, key) {
			return key
		}
	}
	return ""
}

// validateErrorToIssue maps load validation failures to file + key + fix
// without printing secret values.
func validateErrorToIssue(path string, err error) Issue {
	msg := err.Error()
	key, fix := keyAndFixForValidateError(msg)
	return Issue{
		Severity: SeverityError,
		File:     path,
		Key:      key,
		Message:  strings.TrimPrefix(msg, "parse config "+path+": "),
		Fix:      fix,
	}
}

func keyAndFixForValidateError(msg string) (string, string) {
	switch {
	case strings.Contains(msg, "unknown provider profile"):
		return "providers.<name>", "Rename to one of ollama, openai, opencode-go, chatgpt, or remove the table."
	case strings.Contains(msg, "providers.chatgpt.api_key_path"):
		return "providers.chatgpt.api_key_path", "Remove api_key_path; ChatGPT uses OAuth. Run `qcode login chatgpt` and optionally set [chatgpt] oauth_path."
	case strings.Contains(msg, "providers.chatgpt.api_key"):
		return "providers.chatgpt.api_key", "Remove api_key; ChatGPT uses OAuth. Run `qcode login chatgpt`."
	case strings.Contains(msg, "chatgpt api_key"):
		return "api_key", "Remove root api_key with provider = \"chatgpt\"; run `qcode login chatgpt` instead."
	case strings.Contains(msg, "both api_key and api_key_path"):
		return "providers.<name>.api_key_path", "Keep one source: prefer api_key_path = \"~/.config/qcode/<name>.key\" (0600) or env, and remove the inline api_key."
	case strings.Contains(msg, ".api_key_path contains invalid UTF-8"):
		return "providers.<name>.api_key_path", "Use a plain UTF-8 path such as \"~/.config/qcode/<name>.key\"."
	case strings.Contains(msg, "provider_models"):
		return "provider_models", "Use [providers.<name>] model (for example [providers.openai] model = \"...\") and remove provider_models entries."
	case strings.Contains(msg, "provider_thinking"):
		return "provider_thinking", "Use [providers.<name>] thinking and remove provider_thinking entries."
	case strings.Contains(msg, "learning.context_budget"):
		return "learning.context_budget", "Set [learning] context_budget to 0-12000 (for example 1200)."
	case strings.Contains(msg, "context_window"):
		return "context_window", "Set context_window to a non-negative integer (for example 32768) or remove it for automatic."
	case strings.Contains(msg, "auto_compact_threshold"):
		return "auto_compact_threshold", "Set auto_compact_threshold to 1-99 (default 80) or remove it."
	case strings.Contains(msg, "max_steps"):
		return "max_steps", "Set max_steps to a positive integer (for example 32)."
	case strings.Contains(msg, "agent_timeout"):
		return "agent_timeout", "Set agent_timeout to a positive duration such as \"5m\"."
	case strings.Contains(msg, "statusline_hidden"):
		return "statusline_hidden", "Use only remote, mode, model, think, ws, ctx, step, tok (for example statusline_hidden = [\"tok\"])."
	case strings.Contains(msg, "theme must be one of"):
		return "theme", "Pick a theme from docs/configuration.md (for example theme = \"catppuccin-mocha\") or remove it."
	case strings.Contains(msg, "skills.selected_always"):
		return "skills.selected_always", "Every selected_always name must also appear in skills.selected with 1-64 lowercase letters/digits/hyphens/underscores."
	case strings.Contains(msg, "skills.selected"):
		return "skills.selected", "Use skill names with 1-64 lowercase letters/digits/hyphens/underscores."
	case strings.Contains(msg, "redaction"):
		return "redaction", "Fix the [redaction] entry; custom_patterns must be valid Go regexes."
	default:
		return "", "Fix the reported setting; see config.toml.example and docs/configuration.md."
	}
}

func describeKeyFileError(display, full string, err error) string {
	msg := err.Error()
	// Never include file content; only the path problem.
	switch {
	case strings.Contains(msg, "not found"):
		return fmt.Sprintf("api_key_path %q points at missing file %q", display, full)
	case strings.Contains(msg, "must not be a symlink"):
		return fmt.Sprintf("api_key_path %q resolves to symlink %q (refused)", display, full)
	case strings.Contains(msg, "must be a regular file"):
		return fmt.Sprintf("api_key_path %q resolves to non-regular file %q", display, full)
	case strings.Contains(msg, "must have mode"):
		return fmt.Sprintf("api_key_path file %q has unsafe permissions (%s)", full, msg)
	case strings.Contains(msg, "must be owned"):
		return fmt.Sprintf("api_key_path file %q is not owned by the current user", full)
	case strings.Contains(msg, "ownership unknown"):
		return fmt.Sprintf("api_key_path file %q ownership unknown", full)
	case strings.Contains(msg, "exceeds"):
		return fmt.Sprintf("api_key_path file %q is too large", full)
	case strings.Contains(msg, "is empty"):
		return fmt.Sprintf("api_key_path file %q is empty", full)
	case strings.Contains(msg, "exactly one key"):
		return fmt.Sprintf("api_key_path file %q must hold exactly one key line", full)
	case strings.Contains(msg, "invalid UTF-8"):
		return fmt.Sprintf("api_key_path file %q contains invalid UTF-8", full)
	default:
		return fmt.Sprintf("api_key_path %q unreadable: %s", display, msg)
	}
}

func fixForKeyFileError(full string, err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "not found"):
		return fmt.Sprintf("Create %s holding only the key (one line, no quotes), then run chmod 600 %s.", full, full)
	case strings.Contains(msg, "must not be a symlink"), strings.Contains(msg, "must be a regular file"):
		return fmt.Sprintf("Replace %s with a regular file holding only the key, then run chmod 600 %s.", full, full)
	case strings.Contains(msg, "must have mode"):
		return fmt.Sprintf("Run chmod 600 %s (or 400 for read-only) so only you can read it.", full)
	case strings.Contains(msg, "must be owned"), strings.Contains(msg, "ownership unknown"):
		return fmt.Sprintf("Move the key to a file you own (for example %s) with chmod 600.", full)
	case strings.Contains(msg, "exceeds"):
		return fmt.Sprintf("Rewrite %s with only the key on one line (no JSON, no extra text).", full)
	case strings.Contains(msg, "is empty"):
		return fmt.Sprintf("Write only the key into %s (one line, no quotes) with chmod 600.", full)
	case strings.Contains(msg, "exactly one key"):
		return fmt.Sprintf("Keep exactly one non-empty key line in %s.", full)
	default:
		return fmt.Sprintf("Ensure %s is a regular 0600 file holding only the key.", full)
	}
}
