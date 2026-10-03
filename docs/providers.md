# Providers and modes

qcode talks to Ollama and OpenAI-compatible APIs, plus OpenCode Go. Provider-specific wire formats stay isolated behind the small `llm.Provider` interface; see [Extending providers](extending-providers.md) to add one.

## Ollama (default)

```sh
ollama pull qwen2.5-coder:7b
qcode --model qwen2.5-coder:7b
```

### Thinking levels

Thinking-capable Ollama models expose the same two-stage model picker as OpenCode Go:
select a model, then choose `off`, `low`, `medium`, `high`, or `max`. qcode sends
the selected value through Ollama's native `think` request field. GPT-OSS supports
`low`, `medium`, and `high`; its thinking cannot be disabled through that field.

For automation, pass `--thinking` (or set `QCODE_THINKING` / `thinking` in
`config.toml`):

```sh
qcode --provider ollama --model qwen3:8b --thinking high "reply with OK"
```

qcode discovers thinking capability from Ollama's model metadata and preserves
Ollama's default behavior when no explicit level is selected.

## OpenAI-compatible

```sh
export OPENAI_API_KEY=...
qcode --provider openai --model gpt-5
```

### Thinking levels

For the official OpenAI endpoint, qcode reads the exact model's reasoning
effort choices from the public [Models.dev catalog](https://models.dev/api.json)
and sends a selected value through Chat Completions' `reasoning_effort` field.
On first use, qcode caches this metadata at
`qcode/openai-model-meta-v1.json` in the user's cache directory. Refresh it with:

```sh
qcode --provider openai --update-model-meta
```

Only exact model IDs with catalog effort options get thinking support; models
with multiple choices show the thinking selector. Custom OpenAI-compatible
base URLs do not load this metadata and retain their existing behavior. Ollama
continues to discover installed-model thinking support from its local API.

## OpenCode Go

```sh
export QCODE_API_KEY=...
qcode --provider opencode-go --model kimi-k3
```

The OpenCode Go API base URL defaults to `https://opencode.ai/zen/go/v1` and can be overridden with `--base-url`. qcode routes models through the API protocol OpenCode Go assigns to them: OpenAI-compatible Chat Completions, Anthropic Messages, or OpenAI Responses. This makes Muse Spark, GPT Luna, and Grok Responses models available alongside Chat Completions and Qwen/MiniMax Messages models.

### Thinking levels

OpenCode Go models do not share one universal thinking parameter. qcode keeps an
exact model adapter catalog and exposes only that model's valid choices in the
interactive `/model` picker. The selectable choices come from the `opencode-go`
provider's `reasoning_options` in the public [Models.dev catalog](https://models.dev/api.json):
effort options supply effort levels, and toggle options supply `off` / `on`.
qcode applies those choices only to model IDs for which it already has a local
thinking adapter. The adapter's request and reasoning-replay formats remain in
qcode, so upstream metadata does not add new wire protocols.

On first use of the official OpenCode Go endpoint, qcode fetches and caches this
metadata under the user's cache directory at
`qcode/opencode-go-model-meta-v1.json`. Later starts reuse the cache without
checking for updates. Refresh it explicitly with:

```sh
qcode --provider opencode-go --update-model-meta
```

`--update-model-meta` also refreshes the OpenAI catalog when used with
`--provider openai`.

A failed first fetch leaves qcode running without selectable thinking choices;
a failed refresh keeps the last valid cache. Custom `--base-url` endpoints do
not load this Models.dev metadata and keep their existing behavior.

For automation, pass an explicit level with `--thinking` (or `QCODE_THINKING` /
`thinking` in `config.toml`):

```sh
qcode --provider opencode-go --model deepseek-v4-flash --thinking high "reply with OK"
```

Known models may support `off`, `on`, `none`, `minimal`, `low`, `medium`,
`high`, `xhigh`, or `max`; the available set is model-specific. With no
setting, qcode preserves the provider default and omits optional thinking
fields. Unknown model IDs remain usable and also omit those fields. An
unsupported explicit level fails before making a model request. qcode preserves
required reasoning replay data across tool turns for known models that require
it.

Responses-route models receive the selected level as a `reasoning` object with
an `effort` field. GPT 5.6 Luna accepts `none`, `low`, `medium`, `high`,
`xhigh`, and `max`, so it can disable reasoning. Grok 4.6 and the Muse Spark
contributor models accept `minimal`, `low`, `medium`, `high`, and `xhigh`; they
can lower reasoning effort but cannot turn it off. qcode shows reasoning
summaries streamed by these models in its thinking view. These routes omit
`temperature`; Luna rejects it as an unsupported parameter.

## Vision

Vision-capable models can inspect PNG, JPEG, WEBP, and GIF files inside the workspace. Name the image in the prompt; the agent can load it with `view_image` and send it through the active provider:

```sh
qcode --provider ollama --model gemma3 "describe assets/screenshot.png"
```

Images are limited to 20 MiB each. Ollama receives native `images` data; OpenAI and `opencode-go` receive standard Chat Completions image content parts. The selected model and endpoint must support image input.

## One-shot mode

Pass a prompt for non-interactive use. Assistant text goes to stdout and concise action events go to stderr. `--json-events` serializes machine-readable trace and activity records as JSON Lines instead of human-readable events:

```sh
qcode "explain this repository"
qcode --json-events "run the tests" 2>events.jsonl
```

## Demo mode

Demo mode runs the full agent loop without configuring or connecting to an LLM:

```sh
qcode --demo "show me how qcode works"
```

It deliberately pauses during both mocked LLM requests and every mocked tool call, making Ctrl+C cancellation easy to demonstrate. Run `qcode --demo` without a prompt to start an automatic interactive showcase: it submits one full tool tour, then two follow-up prompts while that work is running, so the TUI visibly shows `Queued #1`, `Queued #2`, and its queued count while remaining editable. The follow-ups complete in FIFO order without repeating the full tour. You can continue entering prompts after the scripted showcase. Enter steers a busy task and Tab queues a separate task; Alt+Q opens the pending-work panel. The latest received prompt stays highlighted below the tabs and changes when a task starts or steering is delivered. Interactive demo runs also show realistic `write` and `edit` code-diff previews, a rendered Markdown feature table, and enough colored output to try Page Up and Page Down; `/diff` expansion works normally. `/model` exposes a large fake catalog for trying search and keyboard navigation. The status bar shows a real context percentage: token counts are measured from the scripted conversation and divided by a fake 8192-token window for `scripted-demo`, so the value drops as the demo session grows. The demo runs through all available tool schemas, but does not read or change the workspace and does not execute shell commands. Provider, URL, API key, and model settings are ignored.

## Environment variables

Flags can also be set with `QCODE_PROVIDER`, `QCODE_MODEL`, `QCODE_THINKING`, `QCODE_BASE_URL`, and `QCODE_API_KEY`. See [Configuration](configuration.md) for precedence rules.
