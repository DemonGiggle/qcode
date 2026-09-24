# qcode

[![Go Version](https://img.shields.io/badge/Go-1.22+-00ADD8?style=flat&logo=go)](https://go.dev/)
[![License](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Build Status](https://img.shields.io/badge/build-passing-brightgreen)]()

> A fast, terminal-first coding agent inspired by Pi

qcode is a lightweight, keyboard-driven AI coding assistant that runs entirely in your terminal. One native executable, streaming LLM output, and a compact provider/tool interface.

## 🎬 Demo

![qcode demo: a scripted offline session that streams mocked tools, a report, and queued prompts](docs/assets/demo.gif)

## ✨ Features

- 🖥️ **Full-viewport TUI** with editable input, per-agent history, and ANSI-colored Markdown
- 🤖 **Multi-agent support** with up to 20 concurrent agent tabs
- 🔧 **Rich toolset** including read, write, edit, list, search, shell, and web tools
- 🧭 **Plan mode** for read-only workspace investigation and explicit implementation handoff
- 💬 **Interactive questions** to clarify ambiguous tasks before broad searches
- 🧠 **Global learning** that persists preferences across sessions
- 🎯 **Skills system** for workspace-local instruction bundles, including [official release skills](docs/skills/)
- 🔒 **Sandbox mode** for isolated tool execution on Linux
- 📊 **Context tracking** with real-time token usage display

## 🚀 Quick Start

### Installation

```sh
# Build from source (requires Go 1.22+)
make build
./bin/qcode
```

### Usage

```sh
# With Ollama (default)
ollama pull qwen2.5-coder:7b
qcode --model qwen2.5-coder:7b

# With OpenAI
export OPENAI_API_KEY=...
qcode --provider openai --model gpt-5

# Try without configuration
qcode --demo "show me how qcode works"

# Update an installed qcode binary
qcode update

# Update using a different architecture for the current operating system
qcode update --arch arm64
```

## 📖 Documentation

- [Interface](docs/interface.md) - Terminal UI, slash commands, and navigation
- [Plan mode](docs/plan-mode.md) - Read-only planning and explicit execution handoff
- [Interactive questions](docs/interactive-mode.md) - Per-agent question toggle and terminal workflow
- [Skill Plan mode](docs/skillplan.md) - Guided, review-first qcode skill creation
- [Token usage](docs/token-usage.md) - Per-session totals and structured usage events
- [Providers](docs/providers.md) - Ollama, OpenAI, and OpenCode Go setup
- [Multi-agent](docs/agents.md) - Concurrent agent tabs and orchestration
- [Skills](docs/skills.md) - Workspace-local instruction bundles, [official release skills](docs/skills/), and authoring guidance
- [Web Tools](docs/web-tools.md) - HTTP fetching and search capabilities
- [Learning](docs/learning.md) - Persistent preferences and procedures
- [Configuration](docs/configuration.md) - Config file locations and options
- [Trust Model](docs/trust-model.md) - Security and sandbox behavior
- [Building](docs/build.md) - Build instructions and release process
- [Extending Providers](docs/extending-providers.md) - Adding new LLM providers

## 🔧 Configuration

qcode layers every existing `config.toml` file it finds, with higher-priority
locations overriding settings supplied by lower-priority ones. Copy
[`config.toml.example`](config.toml.example) to one of the supported locations:

```toml
provider = "openai"
base_url = "http://localhost:8000/v1"
api_key = "your-api-key"
model = "my-model"
max_steps = 32
sandbox = true
```

See [Configuration](docs/configuration.md) for lookup locations and precedence rules.

## 🤝 Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

## 📄 License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.

## 🙏 Acknowledgments

- Inspired by [Pi](https://github.com/earendil-works/pi)
- Built with Go and standard library networking
