# Command line

Run `qcode` without a prompt to open the terminal UI. Pass a quoted prompt or
pipe text into stdin for a one-shot run. From a source checkout, substitute
`./bin/qcode` after `make build` unless you have added the binary to your `PATH`.

```sh
qcode --cwd /path/to/project
qcode --provider ollama --model qwen2.5-coder:7b "explain this repository"
printf '%s\n' 'Review the current changes without editing files.' | qcode
qcode --json-events "run the tests" >answer.txt 2>events.jsonl
qcode --demo
qcode --demo "show me how qcode works"
```

Put flags before the prompt; option parsing stops at the first positional
argument. Positional words are joined into one prompt. Stdin supplies the prompt
only when no nonempty positional prompt was provided; empty piped input is an
error. Assistant text goes to stdout and activity or diagnostics go to stderr.
One-shot and piped runs do not save sessions or wait for interactive questions
or directory approvals. Web tools start disabled. Configured skill autoloading
and learning retrieval still apply.

## Startup flags

`qcode --help` lists the flags supported by the installed binary. The defaults
below are the built-in values before configuration and environment overrides.

| Flag | Default | Purpose |
| --- | --- | --- |
| `--provider NAME` | `ollama` | `ollama`, `openai`, or `opencode-go`. |
| `--model ID` | `qwen2.5-coder:7b` | Model ID for the selected provider. |
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
| `--list-providers` | `false` | Print registered providers and exit. |
| `--update-model-meta` | `false` | Refresh thinking metadata for `openai` or `opencode-go` and exit. |
| `--version` | `false` | Print the build version and exit. |
| `--help`, `-h` | — | Print startup help and exit. |

Boolean flags can explicitly override a true configuration value, for example
`--sandbox=false` or `--disable-auto-compact=false`. There is no startup
`--interactive` or `--theme` flag: set `interactive` and `theme` in
`config.toml`, or use `/interactive` and `/theme` in the terminal.

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
| `QCODE_API_KEY` | Provider credential; takes priority over `OPENAI_API_KEY`. |
| `OPENAI_API_KEY` | Fallback credential when `QCODE_API_KEY` is empty. |
| `NO_COLOR` | Any nonempty value disables terminal styling. |
| `QCODE_ASCII` | `1` forces ASCII interface glyphs; `0`, `false`, `no`, or `off` enables Unicode. |

Without a `QCODE_ASCII` override, qcode checks `LC_ALL`, then `LC_CTYPE`, then
`LANG` for a UTF-8 locale. `TERM=dumb` selects ASCII. These settings affect
interface decorations; user text and model output keep their characters.

## Update and metadata commands

```sh
qcode update
qcode update --arch arm64
qcode update --help
qcode --provider openai --update-model-meta
qcode --provider opencode-go --update-model-meta
```

`update` is a CLI subcommand with its own `--arch` flag. It installs the latest
release for the current operating system and chosen architecture. See
[Building](build.md) for replacement behavior and release targets.

Model metadata refresh updates cached thinking choices. It does not update the
binary or the bundled context-window catalog. See [Providers](providers.md).
