# Providers and modes

qcode talks to Ollama, OpenAI-compatible APIs (including Google's Gemini API), OpenCode Go, and ChatGPT subscriptions. Provider-specific wire formats stay isolated behind the small `llm.Provider` interface; see [Extending providers](extending-providers.md) to add one.

Easiest onboarding is `qcode setup`: it picks the active provider, stores API
keys in `0600` key files, verifies the connection, and lets you select a model.
You can also configure manually below. Validate with `qcode config check` and
inspect setup status with `qcode --list-providers`.

Normal agent turns leave temperature unspecified, preserving the provider's
model defaults, including parameters stored in an Ollama model.

## Switching providers without restarting

In the normal terminal UI:

```text
/provider
/provider openai
/provider gemini
/provider opencode-go
/provider chatgpt
```

Bare `/provider` lists the same registry as `qcode --list-providers`, highlighting
this tab's current provider. Up/Down and Enter choose; Esc goes back or leaves the
picker, and Ctrl+C cancels the command. Providers without credentials show
` (Need Setup First)`, are dimmed, and cannot be chosen with the keyboard:
configure the credentials in `qcode setup` first. Direct names are validated
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
Official OpenAI, Gemini, and OpenCode Go endpoints require API credentials; custom endpoints can remain unauthenticated. For ChatGPT, sign in through `qcode setup`.
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
qcode --provider ollama --model qwen3:8b --thinking high prompt "reply with OK"
```

qcode discovers thinking capability from Ollama's model metadata and preserves
Ollama's default behavior when no explicit level is selected.

### Context window

qcode prefers the loaded model's allocated window from `/api/ps` and falls
back to the native maximum from `/api/show` (`model_info.*.context_length`)
and then `/api/tags` (`details.context_length`) when the model is not loaded,
so `CTX` keeps a capacity instead of going unknown before the first
completion. See [Context usage](context.md#window-limits).

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

## Gemini

Gemini is a separate provider identity that uses Google's native
GenerateContent API, by default at
`https://generativelanguage.googleapis.com/v1beta`.
Custom `base_url` proxies keep the OpenAI-compatible Chat Completions wire
(`.../v1beta/openai` + `/chat/completions`); official endpoints use
`:streamGenerateContent` with `x-goog-api-key`. It can be selected
at startup or switched to in the terminal UI without occupying the OpenAI
provider slot:

```sh
export GEMINI_API_KEY=...
qcode --provider gemini --model gemini-3-flash-preview
```

`GOOGLE_API_KEY` is also supported as a fallback environment variable. For a
persistent setup, use a Gemini key file and provider-specific model settings:

```toml
[providers.gemini]
api_key_path = "~/.config/qcode/gemini.key"
model = "gemini-3-flash-preview"
thinking = "high"
# base_url = "https://generativelanguage.googleapis.com/v1beta"
```

The API key file should contain only the key on one line and have mode `0600`
or `0400`; see [provider connection profiles](configuration.md#provider-connection-profiles)
for validation and precedence. The `base_url` can also be set with
`--base-url`; omitting it uses Google's default endpoint. `qcode --list-providers`
includes `gemini`, and `/provider gemini` discovers models through the
native `GET /v1beta/models` endpoint (proxies use compat `/models`). `qcode --provider gemini --update-model-meta`
refreshes the per-user thinking metadata cache.

Google's compatible `/models` endpoint returns model IDs but does not certify
chat or tool capability. qcode filters known embedding-only IDs (including the
`gemini-embedding-*` family) from the picker, but other IDs may still be
unavailable for chat or tool calling, and availability varies by key/account.
Unknown IDs are not assigned inferred thinking capabilities. An explicitly
configured model may still fail if the API does not support GenerateContent
for it. qcode does not add OAuth, audio UI, or Vertex ADC; native
GenerateContent (system_instruction/contents/tools/thinkingConfig/usageMetadata)
is the official route, OpenAI-compat remains only for custom proxies.

### Gemini thinking levels

Thinking controls are exact-model specific, and the status bar shows the
effective level (your explicit choice, or the provider default when omitted).
The built-in mappings expose `none`, `minimal`, `low`, `medium`, and `high`
for Gemini 2.5 Flash (default `medium`) and Flash-Lite (default `none`);
Gemini 2.5 Pro exposes `minimal`, `low`, `medium`, and `high` (default `high`,
thinking cannot be disabled). Gemini 3 / 3.x Flash and Pro models expose their
documented efforts (for example `minimal`, `low`, `medium`, `high` for
`gemini-3-flash-preview` with default `high`; `low`, `medium`, `high` for
`gemini-3.1-pro-preview`, `gemini-3.7-flash`, `gemini-3.8-flash`, and the
`-latest` aliases). Gemini 4 Carbon and Argon expose `low`, `medium`, and
`high` (default `medium`); Gemini 4 Hydrogen exposes `low`, `medium`, `high`
(default), and `xhigh`. `minimal` is not supported on any Gemini 4 model and
is rejected by the API with 400 INVALID_ARGUMENT. Model IDs prefixed with
`models/` (as returned by the live `/models` endpoint) are normalized before
lookup. Metadata can narrow these documented choices, but does not create
capabilities for unknown models. With no explicit choice, qcode omits
`generationConfig.thinkingConfig` and leaves Google's model default in effect
(official route uses `thinkingLevel` for 3/4-series and `thinkingBudget` for
2.5-series; proxy compat still uses `reasoning_effort`). Unsupported
explicit levels fail before the model request. Exact model IDs and
availability can change; see [thinking](https://ai.google.dev/gemini-api/docs/thinking)
and [OpenAI compatibility](https://ai.google.dev/gemini-api/docs/openai#thinking).

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

`--update-model-meta` also refreshes the OpenAI or Gemini catalog when used with
`--provider openai` or `--provider gemini`, respectively.

A failed first fetch leaves qcode running without selectable thinking choices;
a failed refresh keeps the last valid cache. Custom `--base-url` endpoints do
not load this Models.dev metadata and keep their existing behavior.

For automation, pass an explicit level with `--thinking` (or `QCODE_THINKING` /
`thinking` in `config.toml`):

```sh
qcode --provider opencode-go --model deepseek-v4-flash --thinking high prompt "reply with OK"
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

Run `qcode setup` and open
**Providers & Models → chatgpt → Configure credentials & endpoint** to sign in.

```sh
qcode --provider chatgpt --model gpt-5.6-codex prompt "explain this repository"
```

The `chatgpt` provider talks to the Sign in with ChatGPT Responses endpoint
(`https://api.openai.com/v1`) with the OAuth file written during sign-in.
The login follows the open-source Sign in with ChatGPT flow: first registration uses `client_id=dynamic_agent_client`
with `agent_name_hint=qcode` and a stable `ext_agent_host_id`, then reuses the
issued client; open the printed URL, approve, and the `127.0.0.1/auth/callback`
loopback completes (paste back only for remote browsers).
Tokens live at `~/.config/qcode/chatgpt-oauth.json` (`[chatgpt] oauth_path`
overrides) with `0600` owner-only rules, refresh automatically with a 5-minute
margin, and retry once on 401. Requests send the SIWC contract
(`store: false`, `stream: true`) with plain Bearer auth and no
`ChatGPT-Account-Id` header. Durable instructions use top-level `instructions`
except for supported cache-breakpoint models on the official endpoint, where
they are sent as a leading developer message; see [prompt caching](prompt-caching.md#chatgpt-siwc-cache-boundaries-current-policy). A `401 rejected_by_access_enforcement /
no_matching_rule` means the request went to the private
`chatgpt.com/backend-api` route: remove any chatgpt `base_url` override so the
default `https://api.openai.com/v1` is used. Then re-authenticate with ChatGPT
in `qcode setup`. Thinking levels come from each model's
`supported_reasoning_levels` in the account catalog, with a bundled Codex
catalog as a fallback. For example,
Luna offers `low`, `medium`, `high`, `xhigh`, and `max` (default `medium`),
while Sol/Terra variants also offer `ultra` and may default to `low`.
Unknown models without metadata remain usable but have no thinking selector.
Explicit unsupported levels fail before inference; empty preserves the
provider default by omitting `reasoning`. Selected levels are sent as the
Responses `reasoning` effort. Context windows come from the bundled catalog
with Codex-suffix fallback, so `CTX` shows remaining capacity instead of
`unknown`.

## Vision

Vision-capable models can inspect PNG, JPEG, WEBP, and GIF files inside the workspace. Name the image in the prompt; the agent can load it with `view_image` and send it through the active provider:

```sh
qcode --provider ollama --model gemma3 prompt "describe assets/screenshot.png"
```

Images are limited to 20 MiB each. Ollama receives native `images` data; OpenAI
uses Chat Completions image content parts. OpenCode Go encodes images for the
selected model's route: Chat Completions image parts, Messages base64 image
blocks, or Responses `input_image` parts. The selected model and endpoint must
support image input.

## One-shot mode

Use the `prompt` subcommand for non-interactive use. Assistant text goes to stdout and concise action events go to stderr. `--json-events` serializes machine-readable trace and activity records as JSON Lines instead of human-readable events:

```sh
qcode prompt "explain this repository"
qcode --json-events prompt "run the tests" 2>events.jsonl
printf '%s\n' 'explain this repository' | qcode prompt
```

Put startup flags before `prompt`. Quote the message as one argument, or pipe
text into `qcode prompt` without a message argument. See the [command-line reference](cli.md) for all startup options.

## Demo mode

Demo mode runs the full agent loop without configuring or connecting to an LLM:

```sh
qcode --demo prompt "show me how qcode works"
```

Run `qcode --demo` to start an automatic interactive showcase.
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

## Multiple endpoints of the same kind

Configure `[providers.ollama-lab]` with `type = "ollama"` and its own `base_url`
to add a second Ollama server. See [named instances](configuration.md#named-provider-instances)
for a complete example and inheritance rules. Use `qcode --list-providers`,
`--provider ollama-lab`, or `/provider ollama-lab` to discover and select it.

Each tab retains its instance, connection, and model/thinking state. Saved
sessions retain the instance name and endpoint, while credentials are resolved
from the current configuration when resumed. Sub-agent model rosters also accept
instance names. ChatGPT instances use the shared OAuth configuration.

Live model catalogs and Ollama thinking discovery belong to each client instance.
Models.dev disk metadata is shared by kind only for the official endpoint: it is
public model metadata, not a credential or endpoint-specific catalog. Custom
endpoints do not load official metadata. `--update-model-meta` accepts an instance
and refreshes its kind's Models.dev metadata.
