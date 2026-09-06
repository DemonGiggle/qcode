# qcode

`qcode` is a fast, terminal-first coding agent inspired by [Pi](https://github.com/earendil-works/pi). It is intentionally smaller: one native executable, a keyboard-driven terminal UI, a scriptable one-shot mode, streaming LLM output, and a compact provider/tool interface.

## What is included

- A terminal UI with editable input, history, full word wrapping, streamed responses, ANSI-colored Markdown (including aligned GFM tables), a fixed bottom status bar, cancellable tasks via Ctrl+C, and live prefix-matched slash-command suggestions with descriptions. Use `/model` to fetch the provider's models, search a large catalog by typing, move through matches with Up/Down, and select with Enter. Set `NO_COLOR=1` to disable response styling.
- Ollama and OpenAI-compatible APIs.
- `read`, `write`, `edit`, `list`, `search`, `view_image`, `shell`, `web_fetch`, and `web_search` tools.
- Interactive mode shows a task-level `Waiting` indicator from submission through LLM and local-tool work. Use `/verbose` to toggle detailed telemetry, where every LLM request and tool call appears as one timestamped entry whose spinner is replaced by elapsed time when it finishes.
- Use Page Up and Page Down to scroll through qcode's output history without leaving the interactive prompt.
- Use `/new` to discard the current conversation context and start a fresh session without restarting qcode or changing the provider, model, or workspace.
- Successful interactive runs end with a distinct colored `Completed in ...` notice measuring the complete run across every model turn and tool call.
- Provider-delimited model thinking streams in subtle gray, while the final answer retains normal Markdown styling. Ollama uses `message.thinking`; OpenAI-compatible providers may use `reasoning_content` or `reasoning` deltas.
- Interactive `write` and `edit` tool calls display numbered, 10-line Codex-style diff previews with a file summary, change counts, guided body, and colored additions, removals, and hunk headers. Use `/diff` or `/diff N` to expand a preview up to the 200-line safety limit; tool results sent back to the model remain plain text.
- UTF-8 terminals use Unicode interface glyphs; other locales fall back to ASCII without disabling color. Set `QCODE_ASCII=1` to force the ASCII-safe display mode.
- All text sent to an LLM is collected in [`internal/prompt/prompts.go`](internal/prompt/prompts.go).
- Skills: place a `SKILL.md` in `~/.qcode/skills/<name>/` for your user account, or `.qcode/skills/<name>/` / `.agents/skills/<name>/` for one workspace. Use `/skill` to choose skills from a checkbox list showing each name and short description. Only selected skills are shared with the model or available to its `skill` tool. Workspace-local `.qcode/skills` takes precedence over `.agents/skills`, which takes precedence over user-level skills.
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

Vision-capable models can inspect PNG, JPEG, WEBP, and GIF files inside the
workspace. Name the image in the prompt; the agent can load it with
`view_image` and send it through the active provider:

```sh
qcode --provider ollama --model gemma3 "describe assets/screenshot.png"
```

Images are limited to 20 MiB each. Ollama receives native `images` data;
OpenAI and `opencode-go` receive standard Chat Completions
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
provider = "openai"
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

## Web tools

`web_fetch(url)` retrieves public HTTP(S) pages as readable text. It supports
UTF-8 HTML and text, strips scripts and styles, and retains source URL metadata.
It does not execute JavaScript or convert PDFs or other binary documents.
`web_search(query, max_results)` returns titles, URLs, and snippets; the default
is 5 results and the supported range is 1–10.

Both tools run inside qcode without a browser, helper process, or local search
service. DuckDuckGo HTML search is the first supported backend and needs no API
key. Its page markup or blocking behavior can change; challenges and unrecognized
pages return errors rather than being presented as empty search results. There
is no automatic fallback to another backend.

Select the backend in `config.toml`:

```toml
[web_search]
backend = "duckduckgo"
```

Omitting the setting defaults to `duckduckgo`. Unknown backend names fail at
startup. Backend changes require restarting qcode; there is no command-line,
environment-variable, model argument, or slash-command override. `/tool` can
enable or disable the two tools independently. Both web tools start disabled
for security. Enable each one explicitly through `/tool` for the current session;
`/new` and restarting qcode disable them again. Disabled tools are neither sent to
the model nor executable through tool calls. Backend configuration does not enable
them. One-shot mode leaves them disabled because it has no interactive opt-in.

Requests have a 20-second deadline, at most 5 redirects, a 2 MiB response limit
(after HTTP decompression), and at most two concurrent web operations. Text
output is capped at 64 KiB with an explicit truncation marker. The HTML tokenizer
avoids building a full page DOM. Requests use normal TLS certificate validation;
the provider-only TLS bypass setting does not apply to these tools.

Web tools are blocked when an active sandbox disables networking. Outside the
sandbox they still accept only public destinations: loopback, private, link-local,
and reserved addresses are rejected, including DNS results and redirects. They
connect directly and do not use environment HTTP proxies, browser cookies, or
provider credentials. Retrieved content is marked as untrusted reference data.

Run the optional live smoke test with
`QCODE_TEST_WEB_LIVE=1 go test ./internal/tools -run '^TestWebLive$' -v`.
Ordinary tests use local HTTP fixtures and do not depend on search availability.

## Global learning

Use `/learn` after a conversation to propose durable preferences and reusable
procedures for future sessions. qcode shows a compact preview with colored Add, Update, and Remove labels,
the exact content and tags, and previous text for updates. Storage metadata stays
out of the preview. qcode asks for approval before saving anything. Answer `y` or `yes` to apply the
batch; Enter, `n`, or Ctrl+C cancels it. Proposals use a separate model request
without tools and do not enter conversation history.

- `/learn` proposes additions or updates from recent user/assistant text.
- `/learn list` shows all global records and IDs.
- `/learn forget <id>` reviews and deletes a record after approval.
- `/learn compact` proposes additions, rewrites, or deletions to consolidate
  duplicates; approval is required and a backup is created before application.

There is one store per OS user on each device, shared across all workspaces.
V1 has no workspace state, `/learn global` selector, cloud sync, or model
training. Repository-only facts should be omitted; reusable procedures should
state the language, framework, or other conditions under which they apply.

Records live in `v1/global/<id>.json` under:

| Platform | Learning directory |
| --- | --- |
| Linux | `$XDG_STATE_HOME/qcode/learning`, or `~/.local/state/qcode/learning` |
| macOS | `~/Library/Application Support/qcode/learning` |
| Windows | `%LocalAppData%\qcode\learning` |

Writes use private files, an OS-level process lock, and a staged directory swap.
If any record changes after review, the operation is rejected and must be
reviewed again. Failed swaps roll back, and interrupted swaps recover on the
next access. Invalid or unsupported-version records are skipped with warnings
and preserved. Compaction backups are retained in `v1/backups/`; deleting a
record does not remove copies from existing backups.

Before each normal model request, qcode ranks global learning against the latest
user prompt. It selects at most three items, requires meaningful keyword overlap,
and checks language/framework tags as applicability hints. Weak matches inject
nothing. Selected learning is reference context and must not override current
user instructions. It is added only to that request, so repeated model turns do
not accumulate learning copies in conversation history.

```toml
[learning]
context_budget = 1200
```

The budget is an approximate token estimate based on UTF-8 bytes, including the
reference preamble; supported values are 0–12000. Set 0 to disable retrieval
without deleting records or disabling `/learn`. `/new` clears conversation and
injected context, but retains global learning. One-shot runs can retrieve existing
learning; mutation commands require the interactive UI. Demo mode does not access
the learning store.

Extraction uses at most 24 KiB of recent user/assistant text and excludes tool
outputs, tool arguments, images, and model reasoning. Common credential patterns
are redacted from extraction input and rejected in proposed records, but this is
not a complete secret detector: review proposals before approving them. Raw
transcripts are not written to the learning store.

V1 limits the store to 512 entries, record content to 4096 bytes, a proposal to
32 changes, and model review input/output to 64 KiB each. When the existing store
is too large for a review request, `/learn forget` can remove selected records
without a model call. Oversized or non-regular files introduced outside qcode
must be repaired before further writes can safely preserve the store.

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

### Startup and session context

The interactive banner lists enabled and disabled tools, reflecting the current
session settings. `/clear` redraws this summary, including changes made with
`/tool`.

The status bar shows `CONTEXT 73% left`, using the latest completion's input and
output token counts (not cumulative billed usage). OpenAI and OpenCode Go request
streamed usage; Ollama uses `prompt_eval_count` and `eval_count`. Cached input
remains part of the context. The value refreshes after each run and after `/new`,
`/model`, `/skill`, or `/tool`.

OpenAI and OpenCode Go limits come from a bundled [Models.dev catalog](https://models.dev/)
(snapshot: 2026-09-06). Ollama discovers the loaded model's allocated window through
[`/api/ps`](https://docs.ollama.com/api/ps), which may be unavailable before the
first completion. Unknown limits show `CONTEXT unknown`. For custom endpoints or
unlisted models, supply `--context-window 32768` or `context_window = 32768` in
`config.toml`. The flag takes precedence. This only sets the display denominator;
it does not change provider limits. Switching models clears the override.

A `~` prefix marks an estimate when usage is unavailable, the session is new, or
messages/tool definitions changed since the last measured response. Estimates use
serialized text size and may be inaccurate for images and model-specific
tokenization. Percentages are clamped to 0–100%; this indicator does not compact
history or reserve room for the next response.

Usage parsing follows the [OpenAI Chat API](https://developers.openai.com/api/reference/resources/chat)
and [Ollama usage fields](https://docs.ollama.com/api/usage).
