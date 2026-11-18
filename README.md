# qcode

[![Go Version](https://img.shields.io/badge/Go-1.23+-00ADD8?style=flat&logo=go)](https://go.dev/)
[![License](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Build Status](https://img.shields.io/badge/build-passing-brightgreen)]()

> A fast, terminal-first coding agent inspired by Pi

qcode is a lightweight, keyboard-driven AI coding assistant that runs entirely in your terminal. One native executable, streaming LLM output, and a compact provider/tool interface.

## 🎬 Demo

![qcode demo: an offline tour of pinned prompts, todo checklist, steering, queues, diffs, themes, and model search](docs/assets/demo.gif)

## ✨ Features

- 🖥️ **Full-viewport TUI** with pinned latest prompts, editable input, per-agent history, and ANSI-colored Markdown
- ⌨️ **Steering and queues**: Enter updates busy work; Tab queues a separate task
- 🤖 **Multi-agent support** with up to 20 tabs including main
- 🔧 **Rich toolset** including read, write, edit, list, search, shell, directory switching, and web tools
- 🔌 **MCP servers** for extra tools via local stdio commands or remote Streamable HTTP endpoints (`qcode setup` → `MCP servers`, `[mcp_servers.<name>]` in config; trusted without per-call approval, disabled in sandbox/plan modes)
- 🧭 **Plan mode** for read-only workspace investigation and explicit implementation handoff
- ✅ **Execution checklist** with a live Todo panel (`Alt+T`) and `/todo show` review
- 💬 **Interactive questions** to clarify ambiguous tasks before broad searches (inline, per-agent toggle)
- 🧠 **Global learning** that persists preferences across sessions
- 🎯 **Skills system** with lazy loading and always-load, per-tab selection, and [official release skills](docs/skills/)
- 🔌 **Providers** for Ollama, OpenAI-compatible, Gemini, OpenCode Go, and ChatGPT subscriptions (`qcode login chatgpt`, `/provider` switching without restart, named `[providers.<instance>]` endpoints with `type`)
- ✅ **Automatic code review** with `/autoreview` (reusable `review` tab, `AR` status badge, file-change-gated review-fix loop; `[skip review]` opts out)
- 🔒 **Sandbox mode** for isolated tool execution on Linux
- 🛡️ **Local redaction** for detected credentials and payment-card data in output, saved sessions, and exports
- 📊 **Context tracking** with real-time token usage display
- 🎨 **Terminal themes** with Default (Auto), 29 dark/light palettes, and live previews
- 💾 **Saved sessions** with transcript previews, rename, pin, and confirmed deletion
- 🌐 **Browser control** for the same live agents, steering, queues, and approvals

## 🚀 Quick Start

### Installation

```sh
# Build from source (requires Go 1.23+)
make build
./bin/qcode
```

The examples below assume `qcode` is on your `PATH`. From a source checkout,
use `./bin/qcode` instead. Installed release binaries can self-update (see
`qcode update` below); source builds update with `git pull` + rebuild.

### First-time setup (recommended)

Run the interactive wizard once instead of hand-editing `config.toml`:

```sh
qcode setup
qcode config check
qcode --list-providers
```

`qcode setup` is a menuconfig-style wizard that writes your user config
(e.g. `~/.local/etc/qcode/config.toml` on Linux). It picks the active
provider, saves API keys to `0600` key files, verifies the connection and
lets you select a model (plus thinking level when supported), and configures
theme, sandbox, interactive questions, and MCP servers. Saving backs up the
existing config to `config.toml.bak` and re-validates with `qcode config check`.
Requires an interactive terminal.

What it looks like:

```text
qcode setup (menuconfig)
User config: ~/.local/etc/qcode/config.toml
Navigate with Up/Down, Enter to open/select, Esc to go back.

  Providers & Models  --->  (active: ollama / qwen2.5-coder:7b)
  Appearance (Theme)  --->  (default)
  Sandbox (Linux)     --->  (disabled)
  Behavior            --->  (interactive: off)
  MCP servers         --->
  Save and Leave
  Exit without saving
```

Pick `Providers & Models`, then a provider (`ollama`, `openai`,
`opencode-go`, `gemini`, `chatgpt` — marked `(active)` / `(configured)`):

```text
  Set as active default provider
  Configure credentials & endpoint
  Verify connection & select model (gpt-5, thinking: high)
  < Back
```

- `openai` / `gemini` / `opencode-go`: enter an API key (stored in a
  `0600` key file such as `~/.config/qcode/openai.key`, never inline),
  optionally set a custom `base_url`, then `Verify connection & select model`
  to fetch the live model list (`Verifying openai connection and fetching
  models...`).
- `ollama`: just set the base URL (default `http://127.0.0.1:11434`).
- `chatgpt`: runs `qcode login chatgpt` OAuth flow (Sign in with ChatGPT in
  the browser, loopback to `http://127.0.0.1:1455/auth/callback`) instead of
  an API key.

Saving prints a summary, for example:

```text
Saved setup to ~/.local/etc/qcode/config.toml:
  provider: openai
  providers.openai.api_key_path: ~/.config/qcode/openai.key (0600)
  providers.openai.model: gpt-5
  providers.openai.thinking: high
```

### Usage

If you ran `qcode setup`, just start qcode — flags and environment variables
override the saved defaults:

```sh
# After `qcode setup`, just run:
qcode

# Ad-hoc overrides (flags > env > config file > built-ins)
ollama pull qwen2.5-coder:7b
qcode --model qwen2.5-coder:7b

export OPENAI_API_KEY=...  # or GEMINI_API_KEY, QCODE_API_KEY
qcode --provider openai --model gpt-5

# ChatGPT subscription (OAuth, no API key)
qcode login chatgpt
qcode --provider chatgpt --model gpt-5.6-codex

# Interactive tour without configuration
qcode --demo

# One-shot text demo
qcode --demo "show me how qcode works"

# Update an installed qcode binary
qcode update

# Update using a different architecture, or include pre-releases
qcode update --arch arm64
qcode update --pre
```

Switch providers without restarting from inside the TUI with `/provider`;
run `qcode config check` to validate config files and
`qcode --list-providers` to see setup status.

## 📖 Documentation

- [User Manual (PDF)](docs/manual/qcode-user-manual.pdf) - How to use qcode, with example screens for common commands
- [Command line](docs/cli.md) - Startup flags, piped prompts, environment variables, and updates
- [Interface](docs/interface.md) - Terminal UI, slash commands, and navigation
- [Plan mode](docs/plan-mode.md) - Read-only planning and explicit execution handoff
- [Interactive questions](docs/interactive-mode.md) - Per-agent question toggle and terminal workflow
- [Browser control](docs/remote-control.md) - Use the same live session from a phone or browser
- [Skill Plan mode](docs/skillplan.md) - Guided, review-first qcode skill creation
- [Token usage](docs/token-usage.md) - Per-session totals and structured usage events
- [Context usage](docs/context.md) - Remaining capacity, estimates, and automatic compaction
- [Prompt caching](docs/prompt-caching.md) - Provider usage semantics and cache capability evidence
- [Providers](docs/providers.md) - Ollama, OpenAI-compatible, Gemini, OpenCode Go, and ChatGPT setup, plus named provider instances
- [Multi-agent](docs/agents.md) - Concurrent agent tabs and orchestration
- [Skills](docs/skills.md) - Workspace-local instruction bundles, [official release skills](docs/skills/), and authoring guidance
- [Web Tools](docs/web-tools.md) - HTTP fetching and search capabilities
- [Learning](docs/learning.md) - Persistent preferences and procedures
- [Configuration](docs/configuration.md) - Config file locations and options
- [Privacy](docs/privacy.md) - Local redaction, saved sessions, and provider disclosure
- [Trust Model](docs/trust-model.md) - Security and sandbox behavior
- [Building](docs/build.md) - Build instructions and release process
- [Extending Providers](docs/extending-providers.md) - Adding new LLM providers

## 🔧 Configuration

qcode merges every `config.toml` file it finds, with higher-priority
locations overriding only the settings they provide. Copy
[`config.toml.example`](config.toml.example) to one of the supported locations
and adapt it as needed. All fields are optional:

```toml
provider = "ollama"
max_steps = 32
theme = "catppuccin-mocha"
sandbox = false

[providers.ollama]
base_url = "http://127.0.0.1:11434"
model = "qwen2.5-coder:7b"

[providers.openai]
api_key_path = "~/.config/qcode/openai.key"
model = "gpt-5"
thinking = "high"
```

- Root `provider` selects the startup default; each endpoint, credential,
  model, and thinking level lives under its own `[providers.<name>]` table.
- Prefer `api_key_path` (a `0600`/`0400` file holding only the key) or
  environment variables (`QCODE_API_KEY`, `OPENAI_API_KEY`,
  `GEMINI_API_KEY`/`GOOGLE_API_KEY`) over inline `api_key`. Root `base_url` /
  `api_key` / `model` / `thinking` are deprecated.
- Validate without starting a session with `qcode config check`.
- Extra tools via [MCP servers](docs/configuration.md#mcp-servers):
  `[mcp_servers.local]` (`command` + `args`/`env`) or
  `[mcp_servers.remote]` (`url`); prefer `qcode setup` → `MCP servers`.

See [Configuration](docs/configuration.md) for lookup locations, precedence,
named provider instances, MCP servers, and the full option list.

## 🤝 Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

## 📄 License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.

## 🙏 Acknowledgments

- Inspired by [Pi](https://github.com/earendil-works/pi)
- Built with Go and standard library networking
