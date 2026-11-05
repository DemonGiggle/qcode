# Providers and modes

qcode talks to Ollama and OpenAI-compatible APIs, plus OpenCode Go and ChatGPT subscriptions. Provider-specific wire formats stay isolated behind the small `llm.Provider` interface; see [Extending providers](extending-providers.md) to add one.

Normal agent turns leave temperature unspecified, preserving the provider's
model defaults, including parameters stored in an Ollama model.

## Switching providers without restarting

In the normal terminal UI:

```text
/provider
/provider openai
/provider opencode-go
/provider chatgpt
```

Bare `/provider` lists the same registry as `qcode --list-providers`, highlighting
this tab's current provider. Up/Down and Enter choose; Esc goes back or leaves the
picker, and Ctrl+C cancels the command. Providers without credentials show
` (Need Setup First)`, are dimmed, and cannot be chosen with the keyboard:
configure the key or run `qcode login chatgpt` first. Direct names are validated
against that registry. `/help provider` shows usage and examples.

Only the active tab changes. Finish or cancel its work first; running, queued,
approval-waiting, or closing work cannot be switched. Discovery is cancellable,
and if new work or configuration changes arrive during selection, the pending
switch is rejected rather than overwriting them.

qcode associates each provider with its last-used model. Switching restores
that provider's remembered model when it is still in the target catalog,
without prompting. If no remembered model exists, the current model is kept
when the target catalog contains it. Otherwise qcode opens the model picker
and, when applicable, the target's thinking-level picker. A restored or
retained model keeps its thinking level only if the target supports it;
otherwise the level resets to the target default. Provider/model/thinking
commit together:
cancelling a picker, failed credentials/discovery, or an empty catalog leaves
the old selection and saved default unchanged. Selecting the already active
provider does not rebuild the backend, but main can still save that selection.

History, skills, tools, drafts, modes, and cumulative token totals are kept.
Context accounting is refreshed for the new backend, clearing the old custom
capacity override. Because reasoning/tool history can be backend-specific, qcode
warns that the next request may fail; run `/new` if a fresh conversation is needed.

Configure separate endpoints/keys with [connection profiles](configuration.md#provider-connection-profiles).
Official OpenAI/OpenCode Go endpoints require API credentials; custom endpoints
can remain unauthenticated. For ChatGPT, run `qcode login chatgpt` beforehand.
Missing credentials produce setup guidance rather than changing the live tab.
On main, provider/model/thinking save to the user config. Worker selections are
session-local. New tabs inherit their creator's provider, and `/resume` restores
each saved tab with its own provider and current matching credentials.

`/provider` is terminal-only: browser status reflects switches but browser
provider commands are rejected. One-shot, piped, JSON-event, and demo runs do not
support runtime provider switching; continue using startup flags there. There
are no new provider arguments to `/new`, `/resume`, or `/agent new`, and no
in-session endpoint/key editor.

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

For the per-route evidence behind usage accounting and prompt-cache
capabilities (endpoints, usage fields, streaming locations, and unknowns),
see [Prompt caching](prompt-caching.md).

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

## ChatGPT subscription

```sh
qcode login chatgpt
qcode --provider chatgpt --model gpt-5.6-codex "explain this repository"
```

The `chatgpt` provider talks to the Sign in with ChatGPT Responses endpoint
(`https://api.openai.com/v1`) with the OAuth file written by
`qcode login chatgpt` — no API key. The login follows the open-source Sign in
with ChatGPT flow: first registration uses `client_id=dynamic_agent_client`
with `agent_name_hint=qcode` and a stable `ext_agent_host_id`, then reuses the
issued client; open the printed URL, approve, and the `127.0.0.1/auth/callback`
loopback completes (paste back only for remote browsers).
Tokens live at `~/.config/qcode/chatgpt-oauth.json` (`[chatgpt] oauth_path`
overrides) with `0600` owner-only rules, refresh automatically with a 5-minute
margin, and retry once on 401. Requests send the SIWC contract
(`instructions`, `store: false`, `stream: true`) with plain Bearer auth and no
`ChatGPT-Account-Id` header. A `401 rejected_by_access_enforcement /
no_matching_rule` means the request went to the private
`chatgpt.com/backend-api` route: remove any chatgpt `base_url` override so the
default `https://api.openai.com/v1` is used, then run `qcode login chatgpt`
again. Thinking levels (`minimal`, `low`, `medium`, `high`, `xhigh`) are sent
as the Responses `reasoning` effort when selected; empty preserves the
provider default (`medium`). Context windows come from the bundled catalog
with Codex-suffix fallback, so `CTX` shows remaining capacity instead of
`unknown`.

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
