# Configuration

qcode loads the first `config.toml` file it finds; files are not merged. Copy [`config.toml.example`](../config.toml.example) to one of the locations below and adapt it as needed. All fields are optional:

```toml
provider = "openai"
base_url = "http://localhost:8000/v1"
api_key = "your-api-key"
model = "my-model"
max_steps = 32
sandbox = true
```

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

`danger_skip_tls_verify = true` (or `--danger-skip-tls-verify`) disables certificate and hostname verification for qcode's provider and native web HTTP requests. It also supplies insecure-TLS environment settings to shell commands for Git, Node.js, npm, and compatible Python runtimes; arbitrary shell programs may require their own insecure-TLS option.
