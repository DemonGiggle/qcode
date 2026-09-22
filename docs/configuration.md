# Configuration

qcode loads every existing `config.toml` file in the locations below. Lower-priority files load first and higher-priority files override only the settings they provide. Copy [`config.toml.example`](../config.toml.example) to one of the locations below and adapt it as needed. All fields are optional:

```toml
provider = "openai"
base_url = "http://localhost:8000/v1"
api_key = "your-api-key"
model = "my-model"
thinking = "high"
max_steps = 32
agent_timeout = "5m"
sandbox = true
sandbox_command_paths = ["~/.local/bin", "~/go/bin"]
auto_compact_threshold = 80
disable_auto_compact = false

[skills]
paths = ["/opt/qcode/team-skills", ".team/skills"]
```

`sandbox_command_paths` is a persistent trusted-code allowlist for sandbox
mode. Each entry expands `~`, resolves symlinks, deduplicates, and must be an
existing directory. The filesystem root and any directory overlapping a
protected qcode configuration file are rejected with a startup warning.
Accepted directories are mounted read-only at their original absolute paths
(home stays hidden except these mounts) and prepended to the sandbox `PATH`.
Repeatable `--sandbox-command-path` overrides the configured list. Omitting
the key preserves the existing sandbox behaviour without migration.

In the normal interactive UI, `/model` (including its explicit thinking
level) and `/maxsteps` update the user-level file synchronously when issued
from the `main` tab. On Linux this is `~/.local/etc/qcode/config.toml`; other
platforms use the user configuration path shown below. Existing comments and
unrelated settings are preserved. Changes made in other agent tabs, demo
mode, one-shot mode, and piped execution remain session-only. Command-line
flags, environment variables, and higher-priority configuration files retain
their usual precedence.

`skills.paths` adds directories to the built-in skill locations. Paths from all
configuration layers are appended from lowest to highest priority. Blank paths
are ignored and duplicate trimmed path strings are kept only at their first
occurrence, so their ordering is stable. Absolute paths are used as written;
relative paths are resolved from the selected workspace, and `~` expands to the
current user's home directory. A missing directory is kept in the `/skill` hint
and contributes no skills.

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

Explicit command-line flags take precedence over environment variables, which take precedence over the merged configuration, which takes precedence over built-in defaults. Within the configuration, non-empty scalar settings and supplied numeric, duration, and boolean settings override lower-priority values. Empty string settings remain unset and inherit a lower-priority value.

When a flag or environment variable selects a provider different from the configured provider, the configured `model`, `base_url`, and `api_key` are not inherited; set any of them explicitly if they should apply to the selected provider.

API keys can be set with `api_key` in this file, though `QCODE_API_KEY` or `OPENAI_API_KEY` is preferable on shared systems. `--api-key` takes precedence over both environment variables and the configuration file. Keep configuration files containing a key private (for example, mode `0600` on Unix-like systems).

`thinking` (or `--thinking` / `QCODE_THINKING`) is an optional level such as
`off`, `low`, `high`, or `max`. It is validated against the selected provider
and model. No value is sent for unknown models or when the setting is omitted.

`danger_skip_tls_verify = true` (or `--danger-skip-tls-verify`) disables certificate and hostname verification for qcode's provider and native web HTTP requests. It also supplies insecure-TLS environment settings to shell commands for Git, Node.js, npm, and compatible Python runtimes; arbitrary shell programs may require their own insecure-TLS option.

## Conversation compaction

When the context capacity is known, qcode automatically compacts a conversation after 80% of that capacity is used. Set `auto_compact_threshold` to a value from 1 through 99 to change that point, or set `disable_auto_compact = true` (or pass `--disable-auto-compact`) to disable automatic compaction. `/compact` always remains available for manual compaction.
