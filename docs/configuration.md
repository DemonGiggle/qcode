# Configuration

qcode loads the first `config.toml` file it finds; files are not merged. Copy [`config.toml.example`](../config.toml.example) to one of the locations below and adapt it as needed. All fields are optional:

```toml
provider = "openai"
base_url = "http://localhost:8000/v1"
api_key = "your-api-key"
model = "my-model"
thinking = "high"
max_steps = 32
agent_timeout = "5m"
sandbox = true
auto_compact_threshold = 80
disable_auto_compact = false

[skills]
paths = ["/opt/qcode/team-skills", ".team/skills"]
```

`skills.paths` adds directories to the built-in skill locations. Absolute paths
are used as written; relative paths are resolved from the selected workspace,
and `~` expands to the current user's home directory. A missing directory is
kept in the `/skill` hint and contributes no skills.

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

The lookup order is platform-specific:

| Priority | Linux | macOS | Windows |
| --- | --- | --- | --- |
| 1 | `config.toml` beside the executable | `config.toml` beside the executable | `config.toml` beside the executable |
| 2 | `~/.local/etc/qcode/config.toml` | `~/Library/Application Support/qcode/config.toml` | `%AppData%\qcode\config.toml` |
| 3 | `/usr/local/etc/qcode/config.toml` | `/Library/Application Support/qcode/config.toml` | `%ProgramData%\qcode\config.toml` |

Other Unix-like systems use the operating system's user configuration directory followed by `/usr/local/etc/qcode/config.toml`.

## Precedence

Explicit command-line flags take precedence over environment variables, which take precedence over the configuration file, which takes precedence over built-in defaults.

When a flag or environment variable selects a provider different from the configured provider, the configured `model`, `base_url`, and `api_key` are not inherited; set any of them explicitly if they should apply to the selected provider.

API keys can be set with `api_key` in this file, though `QCODE_API_KEY` or `OPENAI_API_KEY` is preferable on shared systems. `--api-key` takes precedence over both environment variables and the configuration file. Keep configuration files containing a key private (for example, mode `0600` on Unix-like systems).

`thinking` (or `--thinking` / `QCODE_THINKING`) is an optional level such as
`off`, `low`, `high`, or `max`. It is validated against the selected provider
and model. No value is sent for unknown models or when the setting is omitted.

`danger_skip_tls_verify = true` (or `--danger-skip-tls-verify`) disables certificate and hostname verification for qcode's provider and native web HTTP requests. It also supplies insecure-TLS environment settings to shell commands for Git, Node.js, npm, and compatible Python runtimes; arbitrary shell programs may require their own insecure-TLS option.

## Conversation compaction

When the context capacity is known, qcode automatically compacts a conversation after 80% of that capacity is used. Set `auto_compact_threshold` to a value from 1 through 99 to change that point, or set `disable_auto_compact = true` (or pass `--disable-auto-compact`) to disable automatic compaction. `/compact` always remains available for manual compaction.
