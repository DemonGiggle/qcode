# Command line

Run `qcode` to open the terminal UI. Use the `prompt` subcommand for a
one-shot run. From a source checkout, substitute
`./bin/qcode` after `make build` unless you have added the binary to your `PATH`.

```sh
qcode --cwd /path/to/project
qcode --provider ollama --model qwen2.5-coder:7b prompt "explain this repository"
printf '%s\n' 'Review the current changes without editing files.' | qcode prompt
qcode --json-events prompt "run the tests" >answer.txt 2>events.jsonl
qcode --demo
qcode --demo prompt 'show me how qcode works'
```

Put startup flags before `prompt`. Use double or single quotes to pass the
message as one argument. Multiple arguments and empty messages are errors.
When you supply no message argument to `qcode prompt`, pipe or redirect nonempty text
to its stdin. Assistant text goes to stdout and activity or diagnostics go to stderr.
One-shot and piped runs do not save sessions or wait for interactive questions
or directory approvals. Both web tools start enabled outside the sandbox and
disabled when sandbox isolation is active. Configured skill autoloading
(lazy or `always_load`) and learning retrieval still apply. `/memo` is
TUI-only and has no one-shot, piped, `--json-events`, or remote-control
surface; see [Memos](memo.md).

## Saved sessions across workspaces

Run `qcode sessions` in a terminal from any directory. The list shows the
workspace for each saved session. Type to filter by workspace path, name, or
preview. Pinned sessions appear first.

| Key | Action |
| --- | --- |
| Up/Down, PgUp/PgDn | Move through the list. |
| Enter | Resume the highlighted session in its original workspace. |
| Tab | Open Rename, Pin/Unpin, and Delete for the highlighted session. |
| Ctrl+X | Mark or unmark a session. Marks persist as you change the filter. |
| Delete | Delete all marked sessions after confirmation. |
| Ctrl+D | Delete every saved session after confirmation, including pinned sessions and sessions hidden by the filter. |
| Ctrl+U | Clear the filter. |
| Esc, Ctrl+C | Exit the session manager. |

Sessions whose original workspace is missing can still be deleted. The
in-app `/resume` picker continues to show sessions in the current workspace.

## Setup wizard

Run `qcode setup` once instead of hand-editing `config.toml`. It is a
menuconfig-style wizard (requires an interactive terminal) that writes your
user config, backs up the previous file to `config.toml.bak`, and runs
`qcode config check` on save. It covers active provider, credentials in
`0600` key files, live model verification, theme, sandbox, interactive
questions, and MCP servers. See the [README](../README.md#first-time-setup-recommended)
for a menu walkthrough.

```sh
qcode setup
qcode config check
qcode --list-providers
```

## Startup flags

`qcode --help` lists the flags supported by the installed binary. The defaults
below are the built-in values before configuration and environment overrides.

| Flag | Default | Purpose |
| --- | --- | --- |
| `--provider NAME` | `ollama` | Built-in kind (`ollama`, `ollama-cloud`, `openai`, `gemini`, `opencode-go`, `chatgpt`) or a configured named instance (e.g. `--provider ollama-lab`). |
| `--model ID` | `qwen2.5-coder:7b` (none for Ollama Cloud) | Model ID for the selected provider. |
| `--thinking LEVEL` | unset | An explicit level supported by that exact model. |
| `--base-url URL` | provider default | Override the provider endpoint. |
| `--api-key KEY` | unset | Provider credential; environment variables avoid putting it in command arguments. |
| `--cwd DIRECTORY` | `.` | Workspace for tools, skills, and saved-session lookup. |
| `--max-steps N` | `32` | Maximum model turns per submitted request. |
| `--agent-timeout DURATION` | `5m` | Consultation batch deadline, including queue time. |
| `--context-window N` | `0` (automatic) | Override terminal-agent context accounting; does not change the provider's allocation. |
| `--disable-auto-compact` | `false` | Disable automatic summaries in terminal sessions; `/compact` remains available. |
| `--sandbox` | `false` | Request Linux bubblewrap isolation. |
| `--sandbox-command-path DIRECTORY` | none | Repeatable trusted executable mounts; supplied flags replace the configured list. |
| `--danger-skip-tls-verify` | `false` | Disable certificate verification for qcode HTTP clients and supply insecure-TLS settings to supported shell clients. |
| `--json-events` | `false` | JSON Lines for trace, activity, and token-usage events; disables blocking questions. |
| `--demo` | `false` | Offline scripted provider and mocked tools; skips configuration and saved sessions. |
| `--list-providers` | `false` | Print built-in providers and configured named instances (with kind and setup status) and exit. |
| `--update-model-meta` | `false` | Refresh thinking metadata for `openai`, `gemini`, or `opencode-go` (named instances refresh their kind) and exit. |
| `--version` | `false` | Print the build version and exit. |
| `--help`, `-h` | — | Print startup help and exit. |

Boolean flags can explicitly override a true configuration value, for example
`--sandbox=false` or `--disable-auto-compact=false`. There is no startup
`--interactive` or `--theme` flag: set `interactive` and `theme` in
`config.toml`, or use `/interactive` and `/theme` in the terminal.

`sandbox = true` and `--sandbox` use the same tool defaults. Fresh sandbox
sessions block networking for native web tools and shell commands. Enabling
either web tool through `/tool` allows networking for both; disabling both
restores isolation. `--sandbox=false` uses the enabled web defaults even if
configuration requests sandbox mode. If sandbox startup fails and qcode
continues without it, both web tools start enabled.

Context-window overrides and compaction preferences are applied to terminal
agents. Current one-shot runs use provider-discovered capacity and the built-in
80% compaction threshold.

## Environment variables

Explicit flags override environment values, which override the merged
configuration. See [Configuration](configuration.md) for provider-specific
inheritance and file lookup order.

| Variable | Setting |
| --- | --- |
| `QCODE_PROVIDER` | Provider name. |
| `QCODE_MODEL` | Model ID. |
| `QCODE_THINKING` | Explicit thinking level. |
| `QCODE_BASE_URL` | Provider endpoint. |
| `QCODE_API_KEY` | Provider credential; takes priority over provider-specific keys. |
| `OLLAMA_API_KEY` | Fallback credential for `ollama-cloud` (and its instances) when `QCODE_API_KEY` is empty. |
| `OPENAI_API_KEY` | Fallback credential for `openai` (and its instances) when `QCODE_API_KEY` is empty. |
| `GEMINI_API_KEY` | Preferred fallback credential for `gemini` (and its instances); takes priority over `GOOGLE_API_KEY`. |
| `GOOGLE_API_KEY` | Fallback credential for `gemini` when `QCODE_API_KEY` and `GEMINI_API_KEY` are empty. |
| `NO_COLOR` | Any nonempty value disables terminal styling. |
| `QCODE_ASCII` | `1` forces ASCII interface glyphs; `0`, `false`, `no`, or `off` enables Unicode. |

Without a `QCODE_ASCII` override, qcode checks `LC_ALL`, then `LC_CTYPE`, then
`LANG` for a UTF-8 locale. `TERM=dumb` selects ASCII. These settings affect
interface decorations; user text and model output keep their characters.

## Update and metadata commands

```sh
qcode update
qcode update --arch arm64
qcode update --pre
qcode update --help
qcode --provider openai --update-model-meta
qcode --provider gemini --update-model-meta
qcode --provider opencode-go --update-model-meta
qcode --provider ollama-lab --update-model-meta
```

Named instances such as `ollama-lab` are accepted anywhere `--provider` is
accepted, including `--update-model-meta` (which refreshes that instance's
kind metadata) and `--list-providers` (which shows instance names alongside
their kind and setup status). See [named instances](configuration.md#named-provider-instances).

## ChatGPT login

Run `qcode setup` in an interactive terminal and open
**Providers & Models → chatgpt → Configure credentials & endpoint**.
For an existing connection, select **Re-authenticate with ChatGPT**.

```sh
qcode --provider chatgpt --model gpt-5.6-codex prompt "explain this repository"
```

The Sign in with ChatGPT flow prints a browser URL. Choose Continue with
ChatGPT and approve. The loopback callback on `http://127.0.0.1:1455/auth/callback` completes automatically (paste the URL
back if your browser is on another machine); credentials are saved with mode
`0600` at `~/.config/qcode/chatgpt-oauth.json` (override with `[chatgpt]
oauth_path`) plus a stable host ID alongside it. The `chatgpt` provider uses
that file instead of an API key; startup refreshes near-expiry tokens and fails
fast with setup guidance when credentials are missing. First sign-in
registers with `client_id=dynamic_agent_client`; later sign-ins reuse the
issued client with `id_token_hint`/`login_hint`.

`update` is a CLI subcommand with its own `--arch` and `--pre` flags. It installs
the latest release for the current operating system and chosen architecture.
Without `--pre` only stable releases are considered; `--pre` also considers
pre-releases such as release candidates. See
[Building](build.md) for replacement behavior and release targets.

Model metadata refresh updates cached thinking choices. It does not update the
binary or the bundled context-window catalog. See [Providers](providers.md).
