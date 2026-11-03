# Configuration

qcode loads every existing `config.toml` file in the locations below. Lower-priority files load first and higher-priority files override only the settings they provide. Copy [`config.toml.example`](../config.toml.example) to one of the locations below and adapt it as needed. All fields are optional:

```toml
provider = "openai"
model = "my-model"
thinking = "high"
max_steps = 32
theme = "catppuccin-mocha"
agent_timeout = "5m"
sandbox = true
sandbox_command_paths = ["~/.local/bin", "~/go/bin"]
auto_compact_threshold = 80
disable_auto_compact = false
interactive = false

[providers.openai]
base_url = "http://localhost:8000/v1"
api_key = "your-api-key"

[providers.ollama]
base_url = "http://127.0.0.1:11434"

[skills]
paths = ["/opt/qcode/team-skills", ".team/skills"]
autoload_paths = [".team/always-on-skills"]
```

`sandbox = true` requests the same isolation as `--sandbox`; an explicit
`--sandbox=false` overrides it. Fresh sessions and `/new` enable both web
tools outside the sandbox and disable both inside it. Enabling either web
tool through `/tool` allows networking for native web tools and sandboxed
shell commands; disabling both restores isolation. Resume restores saved
tool selections, and helpers inherit them. If sandbox startup fails and
qcode continues without it, both web tools start enabled.

`[web_search].backend` selects the search backend without changing tool
enablement. See [Web tools](web-tools.md) for defaults and limits.

`sandbox_command_paths` is a persistent trusted-code allowlist for sandbox
mode. Each entry expands `~`, resolves symlinks, deduplicates, and must be an
existing directory. The filesystem root and any directory overlapping a
protected qcode configuration file are rejected with a startup warning.
Accepted directories are mounted read-only at their original absolute paths
(home stays hidden except these mounts) and prepended to the sandbox `PATH`.
Repeatable `--sandbox-command-path` overrides the configured list. Omitting
the key preserves the existing sandbox behaviour without migration.

In the normal interactive UI, `/model` (including its explicit thinking
level), `/provider` (provider, model, and thinking together), `/maxsteps`,
`/statusline`, and `/skill` update the user-level file
synchronously when issued from the `main` tab. On Linux this is `~/.local/etc/qcode/config.toml`; other
platforms use the user configuration path shown below. Existing comments and
unrelated settings, including nested tables such as `[redaction]` and `[skills]`,
are preserved. `/statusline` saves the hidden segment list as the top-level
`statusline_hidden` setting. `/theme` is terminal-wide and saves its
choice from any agent tab, including when the selection is Default (Auto).
`/interactive on|off` also saves from any agent tab, setting `interactive` to
`true` or `false` as the default for new sessions.
Other preference changes in worker tabs, demo mode, one-shot mode, and piped
execution remain session-only. `theme = "default"` keeps the terminal's ANSI
palette and background. The named palettes below are available in `/theme`,
with dark choices followed by light choices. Colors from the linked theme
families are adapted to qcode's prompt, Markdown, status, and diff roles.

| Theme family | Dark ID | Light ID |
| --- | --- | --- |
| Catppuccin | `catppuccin-mocha` | `catppuccin-latte` |
| Dracula | `dracula` | `alucard` |
| Gruvbox | `gruvbox-dark` | `gruvbox-light` |
| Solarized | `solarized-dark` | `solarized-light` |
| Nord | `nord-dark` | `nord-light` |
| [Tokyo Night](https://github.com/folke/tokyonight.nvim) | `tokyo-night` | `tokyo-night-day` |
| [One Dark](https://github.com/atom/one-dark-syntax) / [One Light](https://github.com/atom/one-light-syntax) | `one-dark` | `one-light` |
| [Rosé Pine](https://rosepinetheme.com/palette/) | `rose-pine` | `rose-pine-dawn` |
| [Everforest](https://github.com/sainnhe/everforest) | `everforest-dark` | `everforest-light` |
| [Kanagawa](https://github.com/rebelot/kanagawa.nvim) | `kanagawa-wave` | `kanagawa-lotus` |
| [Ayu](https://github.com/ayu-theme/ayu-colors) | `ayu-dark` | `ayu-light` |

Command-line flags, environment variables, and higher-priority configuration
files retain their usual precedence.

`skills.paths` adds directories to the built-in skill locations. Paths from all
configuration layers are appended from lowest to highest priority. Blank paths
are ignored and duplicate trimmed path strings are kept only at their first
occurrence, so their ordering is stable. Absolute paths are used as written;
relative paths are resolved from the selected workspace, and `~` expands to the
current user's home directory. A missing directory is kept in the `/skill` hint
and contributes no skills.

`skills.autoload_paths` also adds directories to discovery and enables every
valid skill in them at the start of a new interactive or one-shot run. These
directories are searched after `skills.paths`, so later definitions win on name
collisions. The setting is additive across configuration layers, with the same
path expansion and deduplication rules as `skills.paths`. Only skill names and
descriptions enter the initial prompt; full `SKILL.md` files are loaded on
demand. `/skill` can change the selection for the active agent tab. Resumed
sessions keep their saved skill selection, including always-load flags and
cached bodies. A missing directory contributes no skills; a discovery error
produces a startup warning. Legacy `autoload_paths` entries are always lazy.

The new representation `[[skills.autoload]]` marks autoload directories as
lazy or always-load (full text eagerly injected):

```toml
[[skills.autoload]]
path = ".team/always-on-skills"
always_load = true

[[skills.autoload]]
path = ".team/lazy-skills"
always_load = false
```

`always_load = true` directories behave like `autoload_paths` except their
valid skills start as always-loaded: the full `SKILL.md` (same header format
as the `skill` tool, wrapped as untrusted reference data with no extra
authority) is prepended after the system prompt and those skills are excluded
from the lazy catalog so no skill is counted twice. Oversize or unreadable
always skills warn and fall back to lazy. Both forms are additive across
layers; an `always_load = true` mark is sticky. In `/skill`, autoloaded
skills show an `(auto)` / `(auto-always)` badge, and the stacked context bar
splits system vs must-load vs lazy vs other vs remaining with no duplication.

`skills.selected` and `skills.selected_always` persist an explicit `/skill`
selection for new runs:

```toml
[skills]
selected = ["review-go"]
selected_always = []
```

`/skill`, `/skill name`, `/skill name!`, and `/skill none` on the `main` tab
save the active selection here, preserving comments and unrelated tables.
`selected_always` entries must also appear in `selected`. The lists use
override semantics: a higher-priority layer with a non-nil list (including an
explicit `[]`) replaces lower layers, unlike additive `paths`/`autoload`.
At startup the persisted selection is unioned with autoload (always wins);
unknown or missing skills warn and are skipped. Resumed sessions keep their
saved skill selection, including always-load flags and cached bodies, and
`/new` keeps the active tab selection.

`interactive` defaults to `false`. When enabled in a terminal session, normal
mode may ask up to three distinct, focused questions per submitted prompt.
`/interactive on|off` changes the active agent's setting and saves the default
for new sessions to the user config; restored agents keep their saved setting.
One-shot, piped, non-TTY, and `--json-events` runs never ask blocking questions.

The qcode repository also contains optional official release skills in
[`docs/skills/`](skills/). Browse that collection and decide whether any of
its skills are useful for your work.

`agent_timeout` controls how long `main` waits for a batch of agent consultations
in interactive mode. The default is **five minutes**, allowing time for local
models to answer a focused question. Set a positive duration such as `"90s"`,
`"5m"`, or `"15m"`; zero, negative values, and invalid durations are rejected.
`--agent-timeout 90s` overrides the config value. Changes apply at startup,
including to resumed sessions.

The deadline starts when the batch is submitted and includes queue time. It is
shared by the batch, so waiting for several agents does not multiply the timeout.
Completed replies are kept; failed or expired consultations are reported and
skipped. This setting applies to `consult_agents`, not ordinary user prompts or
background tasks submitted through `delegate_task`.

## Provider connection profiles

Each provider can have its own optional `[providers.<name>]` table containing
`base_url` and `api_key`. Supported names match the built-in registry: `ollama`,
`openai`, `opencode-go`, and `chatgpt`. Connection profiles do not add providers
or change the active provider by themselves; the root `provider` selects the
startup default, and `/provider` changes the active terminal tab.

Profiles merge **field by field** through the usual config layers. An omitted
field inherits; an explicit empty string clears a lower-priority profile value
and lets the provider use its built-in endpoint or no configured key. Higher
priority flags/environment still win. ChatGPT uses `[chatgpt].oauth_path`, not
an API key; a nonempty `providers.chatgpt.api_key` is invalid. Edit profiles in
config and restart to load changes; `/provider` does not prompt for or edit keys.

Legacy root `base_url` and `api_key` remain accepted. Each legacy connection is
bound to the provider declared in that config layer, otherwise to the provider
inherited from lower layers, otherwise to `ollama`. A later layer changing the
default does not reassign those credentials. Explicit profile fields win over
legacy values in the same layer. On a successful main-tab `/provider` save,
legacy assignments in the **user file only** move into that original provider's
profile before the default changes. Existing profile fields and comments are
preserved. System/executable layers are not rewritten, and resolved environment
or flag credentials are never copied into config.

Launch `--base-url`, `--api-key`, `QCODE_BASE_URL`, and `QCODE_API_KEY` overrides
stay associated with the provider selected at launch, including when switching
back to it. Switching to another provider uses its own profile/defaults instead
of forwarding the old connection. `OPENAI_API_KEY` is also available when
switching to OpenAI. At startup the existing `--api-key` > `QCODE_API_KEY` >
`OPENAI_API_KEY` > config precedence remains in effect.

Main-tab `/provider` writes root `provider`, `model`, and explicit `thinking`
together atomically, and remembers the model/thinking under that provider in
`[provider_models]` and `[provider_thinking]` so switching back restores it;
an empty thinking selection removes the user thinking override, as `/model`
does. Main-tab `/model` likewise refreshes the current provider's remembered
entry. Other tabs stay session-local. If saving fails, the
live switch remains active and a warning says the default was not saved.
`--provider`, `QCODE_PROVIDER`, and executable-adjacent config can still override
the saved default at the next startup. New terminal tabs use the creating tab's
provider; existing tabs are never switched implicitly. Saved sessions restore
individual providers but get credentials from current config, not checkpoints.

## Lookup order

The lookup order is platform-specific. Priority 1 is highest: it overrides
settings from lower-priority locations, while the last priority supplies the
base layer.

| Priority | Linux | macOS | Windows |
| --- | --- | --- | --- |
| 1 | `config.toml` beside the executable | `config.toml` beside the executable | `config.toml` beside the executable |
| 2 | `~/.local/etc/qcode/config.toml` | `~/Library/Application Support/qcode/config.toml` | `%AppData%\qcode\config.toml` |
| 3 | `/usr/local/etc/qcode/config.toml` | `/Library/Application Support/qcode/config.toml` | `%ProgramData%\qcode\config.toml` |
| 4 | `etc/config.toml` relative to the executable | `etc/config.toml` relative to the executable | `etc/config.toml` relative to the executable |

Other Unix-like systems use the executable-adjacent config, followed by the
operating system's user configuration directory, `/usr/local/etc/qcode/config.toml`,
and finally the executable-relative `etc/config.toml` path.

Missing files are ignored. If an existing configuration file cannot be read,
contains invalid TOML or unknown settings, or fails validation, qcode prints a
startup warning, skips that file, and continues loading the remaining layers.

## Precedence

Explicit command-line flags take precedence over environment variables, which take precedence over the merged configuration, which takes precedence over built-in defaults. Within the configuration, non-empty scalar settings and supplied numeric, duration, and boolean settings override lower-priority values. Empty root string settings remain unset and inherit a lower-priority value; profile connection fields are the exception, with explicit empty overrides as described above.

When a flag or environment variable selects a provider different from the configured provider, the configured `model`, `thinking`, and `context_window` are not inherited; set them explicitly if they should apply to the selected provider. The provider's remembered entry in `[provider_models]` / `[provider_thinking]` still applies when no explicit `--model` / `--thinking` override is given. The selected provider's own connection profile is used; legacy credentials owned by another provider are not inherited. A configured `context_window` also applies only when the configured model is unset or matches the selected model.

API keys can be set with `[providers.<name>].api_key` (or legacy root `api_key`) in this file, though `QCODE_API_KEY` or `OPENAI_API_KEY` is preferable on shared systems. `--api-key` takes precedence over both environment variables and the configuration file. Keep configuration files containing a key private (for example, mode `0600` on Unix-like systems). The `chatgpt` provider uses `qcode login chatgpt` OAuth instead of an API key; set its file with `[chatgpt] oauth_path` (default `~/.config/qcode/chatgpt-oauth.json`).

`thinking` (or `--thinking` / `QCODE_THINKING`) is an optional level such as
`off`, `low`, `high`, or `max`. It is validated against the selected provider
and model. No value is sent for unknown models or when the setting is omitted.

`danger_skip_tls_verify = true` (or `--danger-skip-tls-verify`) disables certificate and hostname verification for qcode's provider and native web HTTP requests. It also supplies insecure-TLS environment settings to shell commands for Git, Node.js, npm, and compatible Python runtimes; arbitrary shell programs may require their own insecure-TLS option.

## Conversation compaction

When the context capacity is known, qcode automatically compacts a conversation after 80% of that capacity is used. In terminal sessions, set `auto_compact_threshold` to a value from 1 through 99 to change that point, or set `disable_auto_compact = true` (or pass `--disable-auto-compact`) to disable automatic compaction. `/compact` always remains available for manual compaction. Current one-shot runs use provider-discovered capacity and the built-in threshold; context overrides and compaction preferences are applied only to terminal agents.

## Local sensitive-data filtering

All five `[redaction]` sinks default to `true`:

```toml
[redaction]
terminal = true
persistence = true
exports = true
json_events = true
remote = true
custom_patterns = ['INTERNAL-[A-Z0-9]+']
sensitive_paths = ['~/.ssh/private-work-key', '$HOME/private-project']
sensitive_fields = ['client_credential']
```

`terminal` covers response and thinking output, submitted prompt history, tool
activity and output previews, diffs, questions, queues, and diagnostics.
`persistence` filters saved conversations and presentation state. `exports`
applies to both raw and pretty HTML exports, including browser downloads.
`json_events` filters event fields before encoding, and `remote` filters browser
snapshots, catalogs, interactions, queued prompts, steering, and errors.

Explicit booleans override lower configuration layers. Each supplied custom
list replaces its lower-layer list; `[]` clears it. Invalid regular expressions
stop startup and report the setting and zero-based index without printing the
pattern. Paths are literal matches, with both their configured and expanded
forms registered. Custom field names extend the built-in credential and
payment-card field names.

Custom regexes use Go's regular-expression syntax and match individual logical
lines, including when `(?s)` is supplied. Provider streams wait for complete
lines before filtering and rendering, so output can appear less frequently.
The final unterminated line is flushed on completion, cancellation, or error.
Lines over 64 KiB are replaced in full through their terminator.

See [Privacy and local redaction](privacy.md) for retained operational metadata,
legacy-session behavior, and the limits of local filtering.
