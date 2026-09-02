# qcode

`qcode` is a fast, terminal-first coding agent inspired by [Pi](https://github.com/earendil-works/pi). It is intentionally smaller: one native executable, a keyboard-driven terminal UI, a scriptable one-shot mode, streaming LLM output, and a compact provider/tool interface.

## What is included

- A terminal UI with editable input, history, full word wrapping, streamed responses, ANSI-colored Markdown, cancellable tasks via Ctrl+C, and live prefix-matched slash-command suggestions with descriptions. Set `NO_COLOR=1` to disable response styling.
- Ollama and OpenAI-compatible APIs. `openai-like` is an alias intended for services that implement `/v1/chat/completions`.
- `read`, `write`, `edit`, `list`, `search`, and `shell` tools.
- Interactive mode shows a task-level `Waiting` indicator from submission through LLM and local-tool work. Use `/verbose` to toggle detailed telemetry, where every LLM request and tool call appears as one timestamped entry whose spinner is replaced by elapsed time when it finishes.
- Use Page Up and Page Down to scroll through qcode's output history without leaving the interactive prompt.
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
tool call, making Ctrl+C cancellation easy to demonstrate. It runs through all
available tool schemas, but does not read or change the workspace and does not
execute shell commands. Provider, URL, API key, and model settings are ignored.

Flags can also be set with `QCODE_PROVIDER`, `QCODE_MODEL`, `QCODE_BASE_URL`, and `QCODE_API_KEY`.

## Extending providers

Providers implement the small `llm.Provider` interface in `internal/llm/types.go`. Add an implementation and register its factory in `init`:

```go
func init() {
	Register("my-provider", newMyProvider)
}
```

The agent loop only sees normalized messages, streamed text, and tool calls, so provider-specific wire formats remain isolated. Add its name to the CLI help after registration.

## Trust model

File tools reject paths that lexically leave the selected workspace (`--cwd`). qcode is not a security sandbox: symbolic links and the `shell` tool can reach anything allowed by the operating-system account. A shell command's complete requested arguments are shown in the timestamped start event before execution.
