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

Images are limited to 20 MiB each. Ollama receives native `images` data; OpenAI
uses Chat Completions image content parts. OpenCode Go encodes images for the
selected model's route: Chat Completions image parts, Messages base64 image
blocks, or Responses `input_image` parts. The selected model and endpoint must
support image input.

## One-shot mode

Pass a prompt for non-interactive use. Assistant text goes to stdout and concise action events go to stderr. `--json-events` serializes machine-readable trace and activity records as JSON Lines instead of human-readable events:

```sh
qcode "explain this repository"
qcode --json-events "run the tests" 2>events.jsonl
printf '%s\n' 'explain this repository' | qcode
```

Flags must appear before the prompt. Stdin is read when no positional prompt
was supplied. See the [command-line reference](cli.md) for all startup options.

## Demo mode

Demo mode runs the full agent loop without configuring or connecting to an LLM:

```sh
qcode --demo "show me how qcode works"
```

Run `qcode --demo` without a prompt to start an automatic interactive showcase.
It submits one full tool tour and two follow-up prompts while work is running.
The pending area displays the queued prompts in FIFO order, and the tabs and
task indicator show their count. Mocked model and tool calls pause long enough
to try Ctrl+C, typing, scrolling, and tab navigation. You can continue entering
prompts after the showcase.

Enter steers a busy task and Tab queues a separate task; Alt+Q opens the pending
panel. The latest received prompt stays highlighted below the tabs and changes
when a task starts or steering is delivered. The task indicator shows waiting,
streaming thinking or responses, and elapsed operation time. Demo runs also
show numbered write/edit diffs, a Markdown feature table, `/diff` expansion,
and a large fake `/model` catalog for search and keyboard navigation.

The status bar divides estimated scripted token counts by a fake 8192-token
window for `scripted-demo`. Tools are mocked: the agent's tour does not read or
change workspace files or execute shell commands. Configuration files,
provider credentials, and saved sessions are skipped; model and provider flags
do not select a real model. Interactive commands remain usable, including
`/export`, which writes an HTML file when explicitly invoked.

## Environment variables

Flags can also be set with `QCODE_PROVIDER`, `QCODE_MODEL`, `QCODE_THINKING`, `QCODE_BASE_URL`, and `QCODE_API_KEY`. See [Configuration](configuration.md) for precedence rules.
