# qcode

`qcode` is a fast, terminal-first coding agent inspired by [Pi](https://github.com/earendil-works/pi). It is intentionally smaller: one native executable, a keyboard-driven terminal UI, a scriptable one-shot mode, streaming LLM output, and a compact provider/tool interface.

## What is included

- A terminal UI with editable input, history, full word wrapping, streamed responses, ANSI-colored Markdown (including aligned GFM tables), a fixed bottom status bar, cancellable tasks via Ctrl+C, and live prefix-matched slash-command suggestions with descriptions. Use `/model` to fetch the provider's models, search a large catalog by typing, move through matches with Up/Down, and select with Enter. Set `NO_COLOR=1` to disable response styling.
- Ollama and OpenAI-compatible APIs. `openai-like` is an alias intended for services that implement `/v1/chat/completions`.
- `read`, `write`, `edit`, `list`, `search`, `view_image`, and `shell` tools.
- Interactive mode shows a task-level `Waiting` indicator from submission through LLM and local-tool work. Use `/verbose` to toggle detailed telemetry, where every LLM request and tool call appears as one timestamped entry whose spinner is replaced by elapsed time when it finishes.
- Use Page Up and Page Down to scroll through qcode's output history without leaving the interactive prompt.
- Use `/new` to discard the current conversation context and start a fresh session without restarting qcode or changing the provider, model, or workspace.
- Successful interactive runs end with a distinct colored `Completed in ...` notice measuring the complete run across every model turn and tool call.
- Provider-delimited model thinking streams in subtle gray, while the final answer retains normal Markdown styling. Ollama uses `message.thinking`; OpenAI-compatible providers may use `reasoning_content` or `reasoning` deltas.
- Interactive `write` and `edit` tool calls display numbered, 10-line Codex-style diff previews with a file summary, change counts, guided body, and colored additions, removals, and hunk headers. Use `/diff` or `/diff N` to expand a preview up to the 200-line safety limit; tool results sent back to the model remain plain text.
- UTF-8 terminals use Unicode interface glyphs; other locales fall back to ASCII without disabling color. Set `QCODE_ASCII=1` to force the ASCII-safe display mode.
- All text sent to an LLM is collected in [`internal/prompt/prompts.go`](internal/prompt/prompts.go).
- Standard-library networking and file operations. The terminal helper is compiled into the executable.

## Build

Go 1.22 or newer is required.

```sh
make build
./bin/qcode
```

When the current commit has a Git tag, builds use that tag for `qcode
--version`. Untagged builds report `dev`. Set `VERSION=...` explicitly to
override automatic detection.

Release builds set `CGO_ENABLED=0`, so the binary has no C runtime or third-party dynamic-library dependency. On Linux, `ldd bin/qcode` should report `not a dynamic executable`. An operating system may still load its own core system components, particularly on Windows; “self-contained” means no separately installed qcode runtime or third-party shared library.

Build all supported targets from any host with Go installed:

```sh
make release VERSION=0.1.0
```

This creates Linux (`amd64`, `arm64`), macOS (`amd64`, `arm64`), Windows (`amd64`, `arm64`), and FreeBSD (`amd64`) executables in `dist/`.

## Use

Ollama is the default provider:

```sh
ollama pull qwen2.5-coder:7b
qcode --model qwen2.5-coder:7b
```

OpenAI:

```sh
export OPENAI_API_KEY=...
qcode --provider openai --model gpt-5
```

Any OpenAI-compatible endpoint:

```sh
qcode --provider openai-like \
  --base-url http://localhost:8000/v1 \
  --model my-model
```

Vision-capable models can inspect PNG, JPEG, WEBP, and GIF files inside the
workspace. Name the image in the prompt; the agent can load it with
`view_image` and send it through the active provider:

```sh
qcode --provider ollama --model gemma3 "describe assets/screenshot.png"
```

Images are limited to 20 MiB each. Ollama receives native `images` data;
OpenAI, `openai-like`, and `opencode-go` receive standard Chat Completions
image content parts. The selected model and endpoint must support image input.

OpenCode Go (using one of its models served through the Chat Completions endpoint):

```sh
export QCODE_API_KEY=...
qcode --provider opencode-go --model kimi-k3
```

The OpenCode Go API base URL defaults to `https://opencode.ai/zen/go/v1` and
can be overridden with `--base-url`. Consult the OpenCode Go model table when
choosing a model: models assigned to its Responses or Anthropic Messages
endpoints are not supported by qcode yet.

Pass a prompt for non-interactive use. Assistant text goes to stdout and action events go to stderr:

```sh
qcode "explain this repository"
qcode --json-events "run the tests" 2>events.jsonl
```

Try the full agent loop without configuring or connecting to an LLM:

```sh
qcode --demo "show me how qcode works"
```

Demo mode deliberately pauses during both mocked LLM requests and every mocked
tool call, making Ctrl+C cancellation easy to demonstrate. Run `qcode --demo`
interactively and submit any prompt to also see realistic `write` and `edit`
code-diff previews, a rendered Markdown feature table, and enough colored output
to try Page Up and Page Down; `/diff` expansion works normally. `/model` exposes
a large fake catalog for trying search and keyboard navigation. The demo runs
through all available tool schemas, but does not read or change the workspace
and does not execute shell commands. Provider, URL, API key, and model settings
are ignored.

Flags can also be set with `QCODE_PROVIDER`, `QCODE_MODEL`, `QCODE_BASE_URL`, and `QCODE_API_KEY`.

On Linux, pass `--sandbox` to isolate model-triggered tools with
[bubblewrap](https://github.com/containers/bubblewrap). Sandbox mode blocks
network access, hides the user's home directory, exposes the selected workspace
read/write, and mounts the rest of the host filesystem read-only. Temporary
files created under the sandbox's private `/tmp` do not persist. It is disabled
by default.

## Configuration

qcode loads the first `config.toml` file it finds; files are not merged. Copy
[`config.toml.example`](config.toml.example) to one of the locations below and
adapt it as needed. All fields are optional:

```toml
provider = "openai-like"
base_url = "http://localhost:8000/v1"
api_key = "your-api-key"
model = "my-model"
max_steps = 32
sandbox = true
```

The lookup order is platform-specific:

| Priority | Linux | macOS | Windows |
| --- | --- | --- | --- |
| 1 | `config.toml` beside the executable | `config.toml` beside the executable | `config.toml` beside the executable |
| 2 | `~/.local/etc/qcode/config.toml` | `~/Library/Application Support/qcode/config.toml` | `%AppData%\qcode\config.toml` |
| 3 | `/usr/local/etc/qcode/config.toml` | `/Library/Application Support/qcode/config.toml` | `%ProgramData%\qcode\config.toml` |

Other Unix-like systems use the operating system's user configuration
directory followed by `/usr/local/etc/qcode/config.toml`. Explicit command-line
flags take precedence over environment variables, which take precedence over
the configuration file, which takes precedence over built-in defaults. When a
flag or environment variable selects a provider different from the configured
provider, the configured `model`, `base_url`, and `api_key` are not inherited;
set any of them explicitly if they should apply to the selected provider. API
keys can be set with `api_key` in this file, though `QCODE_API_KEY` or
`OPENAI_API_KEY` is preferable on shared systems. `--api-key` takes precedence
over both environment variables and the configuration file. Keep configuration
files containing a key private (for example, mode `0600` on Unix-like systems).

## Extending providers

Providers implement the small `llm.Provider` interface in `internal/llm/types.go`. Add an implementation and register its factory in `init`:

```go
func init() {
	Register("my-provider", newMyProvider)
}
```

The agent loop only sees normalized messages, streamed text, and tool calls, so provider-specific wire formats remain isolated. Add its name to the CLI help after registration.

## Trust model

Without `--sandbox`, file tools reject paths that lexically leave the selected
workspace (`--cwd`), but symbolic links and the `shell` tool can still reach
anything allowed by the operating-system account.

With `--sandbox`, qcode first checks that bubblewrap and the kernel features it
needs are usable. qcode itself remains outside the sandbox so it can load its
configuration and contact the selected provider; API keys and other environment
secrets are removed from shell-tool environments, and the loaded qcode config
file is hidden from model tools. Each shell call receives a
fresh namespace with no network, a hidden real home and runtime directory, a
private temporary directory, a read-only root filesystem, and read/write mounts
for approved directories. Built-in file tools use kernel-assisted path
confinement to prevent symlink escapes.

The model can call `request_directory_access` when work requires another
directory. qcode asks the interactive user to approve an editable directory
path, grants it read/write for the current session, and clears added grants on
`/new`. The filesystem root cannot be granted. Non-interactive directory
requests are denied. If bubblewrap is missing or unusable, or if the selected
workspace contains the user's home, interactive mode offers to continue without
the sandbox or leave; one-shot mode prints a warning and continues unsandboxed.
A shell command's complete requested arguments are shown in the timestamped
start event before execution.
