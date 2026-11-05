# Prompt caching: provider usage semantics and cache capability matrix

Evidence and compatibility reference for epic #70 (cross-provider usage
accounting and prompt-cache effectiveness). Architecture baseline: commit
`768e834` (2026-11-05).

This document is planning evidence, not a new provider or pricing engine. It
records, per request route, what qcode sends, where usage arrives on the
stream, what the fields mean, and what is still unknown. It freezes the
first-release control policy so later issues in the epic implement against
reviewed evidence instead of implementer guesswork.

Upstream documentation was accessed on 2026-11-05. Statements are labeled:

- **Documented upstream** — stated by the provider's public documentation.
- **Verified gateway** — observed in qcode source at the baseline commit
  (files named per claim).
- **Unknown** — not established by either source; must not be assumed.

No live API calls were made to produce this document. All gateway claims are
traceable to fixture-based unit tests under `internal/llm/*_test.go`.

## 1. Registered providers versus wire protocols

qcode registers exactly four providers (`internal/llm/types.go` registry via
`Register`, listed by `qcode --list-providers`):

| Registered name | Default endpoint | Wire protocols used by `Complete` |
| --- | --- | --- |
| `openai` | `https://api.openai.com/v1` | OpenAI Chat Completions only |
| `chatgpt` | `https://api.openai.com/v1` (SIWC Responses contract) | Shared Responses only |
| `opencode-go` | `https://opencode.ai/zen/go/v1` | Chat Completions (default), shared Responses (selected models), Anthropic Messages (selected models) |
| `ollama` | `http://127.0.0.1:11434` | Ollama `/api/chat` only |

Anthropic Messages and OpenAI Responses are **protocols**, not separately
registered providers. They are reachable only through the `opencode-go`
route map (Section 3) and through `chatgpt` (Responses only). Do not describe
them as providers in user-facing text, flags, or accounting labels.

Related protocol-sharing facts (verified gateway):

- `chatgpt` and `opencode-go` Responses models share one streaming parser,
  `parseResponsesStream` (`internal/llm/responses.go`), and one input
  encoder, `responsesInput` (`internal/llm/responses.go`). The subscription
  contract differs (`chatgpt` sends top-level `instructions` and
  `store: false`; see Section 4), but the wire format and usage location are
  the same.
- Anthropic-protocol support on `opencode-go` is protocol compatibility only:
  qcode speaks the Anthropic Messages shape to the gateway
  (`internal/llm/anthropic.go`). That compatibility says nothing about
  whether the gateway accepts `cache_control` breakpoints or bills like
  Claude. Treat gateway cache behavior on that route as **unknown**
  (Section 7).

## 2. `Complete` routing matrix

Every branch of `Complete` routing is listed. Conditions are evaluated in
this order (verified gateway, `internal/llm/openai.go`,
`internal/llm/opencode_go.go`, `internal/llm/chatgpt.go`,
`internal/llm/ollama.go`):

| # | Condition | Request path | Parser |
| --- | --- | --- | --- |
| 1 | provider `openai` (any model, any `base_url`) | `POST {base}/chat/completions` | `parseOpenAIStream` |
| 2 | provider `opencode-go`, model in Responses route map | `POST {base}/responses` | `parseResponsesStream` |
| 3 | provider `opencode-go`, model in Messages route map | `POST {base}/messages` | `parseAnthropicStream` |
| 4 | provider `opencode-go`, any other model (including unknown IDs) | `POST {base}/chat/completions` | `parseOpenAIStream` |
| 5 | provider `chatgpt` (any non-empty model) | `POST {base}/responses` | `parseResponsesStream` |
| 6 | provider `ollama` (any model) | `POST {base}/api/chat` | inline NDJSON scan in `Complete` |

Branch 4 is the documented default: `OpenCodeGoRoute` returns
`chat/completions` for unknown models, matching `Complete`'s fallthrough
(verified gateway, `internal/llm/opencode_go.go`).

### Current route-map model IDs

Source: `openCodeGoModelRoutes` in `internal/llm/opencode_go.go` (upstream
table checked 2026-09-12 per the code comment). Listed here without adding
models:

Responses route (`openCodeGoResponsesRoute`):

- `gpt-6.1-sol`, `gpt-5.6-luna`, `grok-4.6`
- `muse-spark-1.2-contributor`, `muse-spark-1.3-contributor`

Messages route (`openCodeGoMessagesRoute`):

- `minimax-m2.5`, `minimax-m2.7`, `minimax-m3`
- `qwen3.5-plus`, `qwen3.6-plus`, `qwen3.7-max`, `qwen3.7-plus`,
  `qwen3.8-flash`, `qwen3.8-max`

Any model ID not in this map uses the Chat Completions route on
`opencode-go` (branch 4). Adding a model ID to either list is a routing
change and needs its own reviewed route/model evidence.

## 3. Per-route reference

### 3.1 OpenAI Chat Completions on the official endpoint

- **Endpoint / override handling (verified gateway):** `POST
  {base}/chat/completions` with `base` defaulting to
  `https://api.openai.com/v1`. `IsOfficialOpenAIEndpoint` is true only for
  the empty/`base_url`-unset case and the exact default URL
  (`internal/llm/openai.go`). A custom `base_url` keeps the same Chat
  Completions path and request shape; it only changes the host. Custom
  endpoints do not load Models.dev thinking metadata
  (documented in `docs/providers.md`).
- **Routing headers (verified gateway):** `Content-Type: application/json`;
  `Authorization: Bearer <key>` when a key is set; `User-Agent` and
  `x-opencode-session` are sent only when non-empty, and the `openai`
  provider constructs both as empty, so official OpenAI requests carry
  neither header.
- **Usage fields (verified gateway):** `stream_options: {include_usage:
  true}` is always sent; usage arrives as a top-level `usage` object on any
  SSE `data:` line and is decoded into `llm.Usage{InputTokens,
  OutputTokens}` (JSON tags `prompt_tokens`, `completion_tokens`)
  (`internal/llm/openai.go`, `parseOpenAIStream`). The adapter reads no
  cache-breakdown subfields today.
- **Inclusive versus separate input semantics (documented upstream):**
  `input_tokens` is the inclusive total. Cache detail, when reported, is a
  subset breakdown (`input_tokens_details.cached_tokens`,
  `cache_write_tokens`, and per-lifetime
  `cache_write_tokens_details.ttl_30m_tokens / ttl_12h_tokens` on GPT-5.6 and
  later). Ordinary input equals `input_tokens - cached_tokens -
  cache_write_tokens`. qcode does not parse these subfields at baseline.
- **Streaming event locations (verified gateway):** text deltas in
  `choices[].delta.content`; reasoning in `delta.reasoning_content` /
  `delta.reasoning`; structured reasoning replay in
  `delta.reasoning_details`; tool-call deltas in `delta.tool_calls`. Usage
  may arrive on a terminal event with no `choices`.
- **Missing-field semantics (verified gateway):** if no event carries
  `usage`, `Response.Usage` stays nil. The agent then increments
  `SessionUsage.Missing` instead of adding tokens (`internal/agent/usage.go`)
  and the status bar shows `unknown` / `?` (`docs/token-usage.md`).
- **Cache read/write reporting (verified gateway):** none parsed at
  baseline. Reads and writes are therefore invisible to accounting on this
  route until a later issue adds the detail fields.
- **Available controls (verified gateway):** `temperature` (sent only when
  non-nil, and for `openai` only when the model's thinking capability is
  unsupported); `reasoning_effort` thinking level for exact catalog models;
  tool definitions; tool-call/reasoning replay fields. No cache control is
  sent at baseline.
- **Billing limitations:** per-token billing with discounted cached input is
  documented upstream and varies by model. qcode token counts are not costs:
  there is no pricing table in the client, and counts must never be
  presented as billing.

### 3.2 OpenAI-compatible custom endpoints (same Chat path, unofficial host)

Same request shape and parser as Section 3.1 (verified gateway, branch 1
covers any `base_url`). Differences:

- **Endpoint (verified gateway):** `IsOfficialOpenAIEndpoint` is false; the
  path stays `/chat/completions` under the custom host.
- **Usage (unknown):** custom servers may omit `usage`, use a different
  schema, or report non-billable proxy counts. Missing `usage` is handled as
  nil (Section 3.1); malformed shapes surface as stream/decode errors.
- **Cache capability (unknown):** nothing about the custom server's cache
  behavior follows from speaking the Chat Completions shape. Do not infer
  `prompt_cache_key` support, breakpoint support, or Claude-style billing
  from protocol shape.
- **Controls (verified gateway):** no Models.dev metadata is loaded, so no
  exact-model thinking choices; otherwise the same fields as Section 3.1. No
  new cache fields under the frozen policy (Section 8).
- **Billing limitations:** pricing is unknown and may not exist (self-hosted
  proxy). Counts are informational only.

### 3.3 ChatGPT subscription (SIWC Responses)

- **Endpoint / override handling (verified gateway,
  `internal/llm/chatgpt.go`):** default `https://api.openai.com/v1`, `POST
  {base}/responses`, Bearer auth with the OAuth access token from
  `qcode login chatgpt`. A `base_url` containing `chatgpt.com/backend-api`
  selects the legacy Codex route: `?client_version=` on `/models` and the
  `ChatGPT-Account-Id` header. The legacy route rejects third-party OAuth
  clients (`rejected_by_access_enforcement / no_matching_rule`); the
  supported route is the default. Auth failures (401/403) are retried once
  after a serialized refresh.
- **Subscription contract (verified gateway):** top-level `instructions`
  (system messages are lifted out of `input`; the SIWC route rejects system
  roles), `store: false`, `stream: true`, plain Bearer auth, no
  `ChatGPT-Account-Id` on the supported route. Thinking level, when set, is
  sent as the Responses `reasoning` effort object; empty preserves the
  provider default (`medium`).
- **Routing headers (verified gateway):** `Authorization: Bearer <OAuth
  token>`; `ChatGPT-Account-Id` only on the legacy base URL. No
  `x-opencode-session`, no `anthropic-version`, no API-key header.
- **Usage fields (verified gateway):** shared `parseResponsesStream`: usage
  arrives only in the `response.completed` event as
  `response.usage{input_tokens, output_tokens}` mapped to `llm.Usage`.
  Partial deltas carry no usage.
- **Inclusive versus separate input semantics (documented upstream):** same
  Responses accounting as Section 3.1: `input_tokens` is inclusive;
  `cached_tokens` / `cache_write_tokens` are subset details. Not parsed at
  baseline.
- **Streaming event locations (verified gateway):**
  `response.output_text.delta` (text),
  `response.reasoning_summary_text.delta` (thinking),
  `response.output_item.added` + `response.function_call_arguments.delta`
  (tool calls), `response.completed` (output items + usage).
- **Missing-field semantics (verified gateway):** no `response.completed`
  usage object means nil `Usage`, counted as missing (Section 3.1).
- **Cache read/write reporting (verified gateway):** none parsed at
  baseline.
- **Available controls (verified gateway):** `reasoning` effort
  (`minimal/low/medium/high/xhigh`), tools, `instructions`. `temperature` is
  never sent. No cache control is sent at baseline.
- **Billing limitations:** ChatGPT plan usage is subscription-based, not
  per-token. Reported tokens are informational and must not be costed.

### 3.4 OpenCode Go Responses route (Muse, Luna, Grok models)

- **Endpoint / override handling (verified gateway,
  `internal/llm/responses.go`, `internal/llm/opencode_go.go`):** `POST
  {base}/responses` under the default `https://opencode.ai/zen/go/v1` or a
  `--base-url` override. Routing is by the Section 2 model map; reasoning
  Responses models reject `temperature` (Luna returns 400), so the adapter
  never sends it.
- **Routing headers (verified gateway):** `User-Agent: qcode` (always),
  `x-opencode-session: <provider UUIDv4>` (always, Section 6),
  `Authorization: Bearer <key>` when set, `Content-Type: application/json`.
- **Usage fields (verified gateway):** same shared Responses parser as
  Section 3.3: only `response.completed` → `response.usage{input_tokens,
  output_tokens}`.
- **Inclusive versus separate input semantics:** **unknown** for this
  gateway. The shape is Responses-compatible, but whether `input_tokens` is
  inclusive of cached tokens and whether detail subfields exist on this
  route is unverified gateway behavior. Do not copy OpenAI's accounting
  semantics onto gateway-reported counts without route evidence.
- **Streaming event locations (verified gateway):** identical to
  Section 3.3.
- **Missing-field semantics (verified gateway):** nil `Usage` when the
  terminal event carries no usage; counted as missing.
- **Cache read/write reporting (verified gateway):** none parsed at
  baseline.
- **Available controls (verified gateway):** `reasoning` effort object for
  adapted models; tools; reasoning-replay via saved `ReasoningDetails`
  output items. No `temperature`. No cache control is sent at baseline.
- **Billing limitations:** gateway billing model is unverified from code;
  counts are informational and must not be costed or compared across
  providers.

### 3.5 OpenCode Go Messages route (Qwen and MiniMax models)

- **Endpoint / override handling (verified gateway,
  `internal/llm/anthropic.go`):** `POST {base}/messages` with
  `anthropic-version: 2023-06-01`, `max_tokens: 8192` (fixed), `stream:
  true`. System messages are lifted to top-level `system`; tool results
  become `tool_result` blocks; assistant turns become `text` + `tool_use`
  blocks.
- **Routing headers (verified gateway):** `x-api-key: <key>` (not
  `Authorization`), plus `User-Agent: qcode` and `x-opencode-session`
  (Section 6).
- **Usage fields (verified gateway):** `message_start.message.usage.
  input_tokens` seeds `Usage{InputTokens}`; a later
  `message_delta.usage.output_tokens` fills `OutputTokens`. Either half may
  be absent, leaving a partially populated `Usage` (the other field stays
  zero). Upstream Claude fields `cache_read_input_tokens` and
  `cache_creation_input_tokens` are **not** read by `parseAnthropicStream`.
- **Inclusive versus separate input semantics:** **unknown** for this
  gateway. Anthropic protocol compatibility does not prove the gateway
  accepts `cache_control` breakpoints, attributes cache reads/writes, or
  bills like Claude.
- **Streaming event locations (verified gateway):** `content_block_start`
  (tool_use), `content_block_delta` with `text_delta` /
  `input_json_delta`, `message_start` (input usage), `message_delta`
  (output usage).
- **Missing-field semantics (verified gateway):** no usage events means nil
  `Usage`, counted as missing. A lone `message_start` yields input-only
  usage with zero output; consumers must treat zero output as
  "unreported", not as "no output tokens".
- **Cache read/write reporting (verified gateway):** none. No
  `cache_control` is sent on any request at baseline.
- **Available controls (verified gateway):** tools with `input_schema`,
  fixed `max_tokens`. `temperature` and thinking request fields are never
  sent on this route. No cache control is sent at baseline.
- **Billing limitations:** same as Section 3.4 — unverified gateway
  billing; counts are informational.

### 3.6 Ollama (`/api/chat`)

- **Endpoint / override handling (verified gateway,
  `internal/llm/ollama.go`):** `POST {base}/api/chat`, default
  `http://127.0.0.1:11434`. Model catalog from `GET /api/tags` with
  `/api/show` fallback for capabilities.
- **Routing headers (verified gateway):** `Content-Type: application/json`
  only. No auth, session, or version headers.
- **Usage fields (verified gateway):** per-line NDJSON
  `prompt_eval_count` (pointer — presence-gated) and `eval_count`, mapped
  to `Usage{InputTokens, OutputTokens}`. No cache subfields exist in this
  API.
- **Inclusive versus separate input semantics:** not applicable — Ollama
  documents no prompt-cache accounting on this endpoint (documented
  upstream, Ollama chat/usage docs). `prompt_eval_count` is the local
  prompt-evaluation count.
- **Streaming event locations (verified gateway):** `message.content`
  (text), `message.thinking` (thinking), `message.tool_calls` (calls),
  `error` (failure). Usage is set when a line carries `prompt_eval_count`.
- **Missing-field semantics (verified gateway):** lines without
  `prompt_eval_count` leave `Usage` nil; counted as missing like other
  routes.
- **Cache read/write reporting:** unsupported (documented upstream has no
  such fields; verified gateway parses none). Record as **unsupported**,
  not unknown.
- **Available controls (verified gateway):** `options.temperature` when
  non-nil, native `think` field, tools, image data. No cache control
  exists or is sent.
- **Billing limitations:** local inference has no billing. Counts are
  informational.

### Unsupported versus unknown

| Behavior | Unsupported (will not implement) | Unknown (needs evidence before use) |
| --- | --- | --- |
| Ollama cache fields/endpoints | Yes — no such API surface | — |
| Custom-endpoint cache semantics | — | Yes — varies by server |
| `chatgpt` cache controls | Not in first release (Section 8) | Whether SIWC honors them at all |
| OpenCode Go Responses cache accounting | Not in first release | Whether counts are inclusive; whether detail fields exist |
| OpenCode Go Messages `cache_control` / Claude billing | — | Yes — protocol shape proves nothing |
| Official OpenAI Chat cache detail fields | Parse-only in a later issue | Exact detail availability per model |

## 4. Sample payloads (absent versus explicit-zero)

Shapes are taken from the baseline adapters. `llm.Usage` serializes as
`{"prompt_tokens": N, "completion_tokens": M}`; a nil `*Usage` serializes as
`null` and means "unreported", which is different from explicit zero.

### Chat Completions usage event

Usage present:

```json
{"id": "chatcmpl-1", "choices": [], "usage": {"prompt_tokens": 1200, "completion_tokens": 340}}
```

Usage absent (any event without `usage`; `Response.Usage == nil`):

```json
{"id": "chatcmpl-1", "choices": [{"delta": {"content": "hi"}}]}
```

Explicit zero (decodes to `Usage{0, 0}`, non-nil — counts as reported):

```json
{"id": "chatcmpl-1", "choices": [], "usage": {"prompt_tokens": 0, "completion_tokens": 0}}
```

Minimal Chat request body sent by qcode (verified gateway):

```json
{"stream": true, "stream_options": {"include_usage": true}, "model": "gpt-5", "messages": []}
```

### Responses usage event (`chatgpt` and OpenCode Go Responses)

Usage present (only inside `response.completed`):

```json
{"type": "response.completed", "response": {"output": [], "usage": {"input_tokens": 15000, "output_tokens": 320}}}
```

Usage absent (`Response.Usage == nil`):

```json
{"type": "response.completed", "response": {"output": []}}
```

Explicit zero:

```json
{"type": "response.completed", "response": {"output": [], "usage": {"input_tokens": 0, "output_tokens": 0}}}
```

Upstream detail shape (documented upstream, **not** parsed at baseline —
shown so later issues know what to add):

```json
{"usage": {"input_tokens": 12000, "input_tokens_details": {"cached_tokens": 4000, "cache_write_tokens": 7000}}}
```

### Anthropic Messages usage events (OpenCode Go Messages route)

Input usage at stream start:

```json
{"type": "message_start", "message": {"usage": {"input_tokens": 900}}}
```

Output usage later:

```json
{"type": "message_delta", "usage": {"output_tokens": 120}}
```

Neither event (`Response.Usage == nil`); or input-only, which decodes to
`Usage{InputTokens: 900}` with `OutputTokens` zero meaning "unreported",
not "free of output".

Upstream Claude cache shape (documented upstream, **not** parsed, and
gateway support **unknown**):

```json
{"usage": {"input_tokens": 900, "cache_read_input_tokens": 800, "cache_creation_input_tokens": 100}}
```

### Ollama usage line

Usage present:

```json
{"message": {"role": "assistant", "content": "hi"}, "prompt_eval_count": 42, "eval_count": 7}
```

Usage absent (`Response.Usage == nil` — any line without
`prompt_eval_count`):

```json
{"message": {"role": "assistant", "content": "hi"}}
```

Explicit zero (presence-gated: decodes to `Usage{0, 0}`, non-nil):

```json
{"message": {"role": "assistant", "content": "hi"}, "prompt_eval_count": 0, "eval_count": 0}
```

## 5. `x-opencode-session` behavior and restore/reset lifecycle

(Verified gateway: `internal/llm/openai.go`, `internal/llm/opencode_go.go`,
`internal/agent/persistence.go`, `internal/agent/agent.go`.)

- **Creation:** `newOpenCodeGo` mints one UUIDv4 per provider instance
  (`newOpenCodeSessionID`) and stores it as `sessionID`. The `openai`
  provider constructs with an empty session ID.
- **Sending:** every `opencode-go` request carries
  `x-opencode-session: <uuid>` — `GET /models` and all three `Complete`
  routes (Chat, Responses, Messages). The same value is reused across all
  requests from one provider instance (covered by
  `TestOpenCodeGoReusesSessionIDForAllRequests`). The `openai`, `chatgpt`,
  and `ollama` providers never send this header.
- **Purpose:** non-secret routing identity that keeps related requests on
  the same gateway backend. It is safe to persist and is not a credential.
- **Checkpoint:** `publishCheckpoint` saves it as `SavedState.ProviderSession`
  whenever the provider implements `SessionIdentity()`.
- **Restore:** `RestoreState` calls
  `RestoreSessionIdentity`, which accepts only UUID-shaped values and only
  when the provider name is `opencode-go`; anything else fails with
  `invalid provider session identity`. `/resume` therefore continues on the
  same gateway session.
- **Fresh state:** `RestoreFreshState` never imports conversation state or
  provider identity — only settings (provider, model, thinking, endpoint,
  context/tools config). A fresh agent mints a new provider instance and a
  new session ID.
- **Reset (`/new`, `ResetSession`):** clears conversation history,
  `sessionUsage`, `contextUsage`, learning state, and tool grants, but does
  **not** rotate the provider instance or its session ID. (Separately,
  provider/model switches refresh context accounting and clear the custom
  capacity override — `docs/providers.md`.) Gateway-side cache entries, if
  any, are unaffected by client resets — a reset changes the rendered prefix
  and therefore normally ends prefix reuse.

## 6. Streaming and accounting notes

- **Every current `Complete` implementation requests streaming, including
  when `onText` is nil** (verified gateway): Chat (`"stream": true` plus
  `stream_options.include_usage`), both Responses adapters (`"stream":
  true`), Anthropic Messages (`"stream": true`), Ollama (`"stream": true`).
  A nil callback only skips fan-out; the transport still streams and usage
  still arrives on stream events. Non-streaming transport support is outside
  this epic.
- **Context usage is not billing.** `CTX` shows remaining capacity from the
  latest completion's input+output counts (or an estimate); it includes
  cached input in the context and must not be read as spend
  (`docs/context.md`).
- **Session totals are reported counts with a missing-counter**, not
  invoices: `SessionUsage{InputTokens, OutputTokens, TotalTokens, Missing}`
  accumulates per completion; nil usage increments `Missing`
  (`internal/agent/usage.go`, `docs/token-usage.md`).
- **A cache key does not guarantee a hit.** Documented upstream: keys
  influence routing/accounting separation, not pinning; reuse additionally
  requires prefix match, minimum length, lifetime, same machine/region/org,
  and unchanged prefix-affecting settings (model, tools, reasoning effort,
  verbosity, compaction).
- **Preserved boundaries (not changed by this issue):** tool-call and
  reasoning replay formats per route, trust wrappers
  (`trust.Wrap`/`UnwrapRestored`, `origin`/`untrusted` provenance),
  output/session redaction, and agent isolation (per-agent counters) all
  stay as-is. Cache work must not move untrusted content between roles to
  chase prefix stability (deferred explicitly below).

## 7. Frozen first-release control policy

Until separately reviewed route/model evidence lands, the only new cache
control permitted is:

- Official OpenAI Chat Completions (`IsOfficialOpenAIEndpoint` true) **may**
  receive a stable `prompt_cache_key`. Key choice and rotation policy belong
  to the implementing issue, with the constraint that keys separate
  accounting groups and never carry secrets.

Everything else receives **no new cache fields**:

- Custom OpenAI-compatible endpoints, `chatgpt`, OpenCode Go
  Responses/Messages routes, and Ollama: no `prompt_cache_key`, no
  breakpoint markers, no retention/TTL fields, no prewarm flags.
- The existing `x-opencode-session` header behavior is preserved exactly
  (Section 5); it is routing identity, not a cache control, and must not be
  repurposed as one.
- No existing request field is removed or renamed to make room for caching.

Additional controls require separately reviewed route/model evidence, not
implementer guesswork — in particular, Anthropic-shaped Messages requests
need gateway evidence that `cache_control` is accepted and how it is
billed before any breakpoint is sent.

Cache writes are reported where the protocol exposes them (a later issue
adds the detail parsers) even though this release does not request
explicit writes.

## 8. Explicitly deferred

- Explicit cache-breakpoint markers (`prompt_cache_breakpoint`,
  `cache_control`).
- Extended retention / TTL selection (`prompt_cache_retention`,
  `prompt_cache_options.ttl`).
- Prewarming (`prompt_cache_options.prewarm`).
- Pricing tables and any cost computation in the client.
- Relocating untrusted content between roles (system/developer/user/tool)
  to engineer prefix stability. Trust boundaries outrank cache-hit rates.

## 9. Resolved versus unresolved evidence

Resolved by this document: the four registered providers; the six-branch
routing matrix; current route-map IDs; per-route endpoints, headers,
usage locations, and missing semantics; sample absent/explicit-zero
payloads; session-header lifecycle; streaming-always; the frozen control
policy.

Unresolved (carried forward, not assumed):

1. Whether OpenCode Go Responses `input_tokens` is inclusive of cached
   tokens, and whether the gateway forwards `input_tokens_details`.
2. Whether OpenCode Go accepts `cache_control` on the Messages route, what
   it bills, and whether Claude cache prices apply.
3. Whether the SIWC (`chatgpt`) endpoint honors `prompt_cache_key`,
   breakpoints, or extended retention.
4. Per-model minimum cacheable lengths and cache lifetimes for the gateway
   routes (upstream values describe OpenAI/Claude directly, not the
   gateway).
5. Custom-endpoint usage schemas and cache behavior (per-server unknowns).
6. Whether gateway Responses usage ever includes per-lifetime write
   breakdowns (`cache_write_tokens_details`).

## 10. References

- OpenAI prompt-caching guide (documented upstream; fetched 2026-11-05):
  `https://developers.openai.com/docs/guides/prompt-caching` — key/rotation
  guidance, breakpoint modes, `input_tokens_details` shapes, lifetime and
  billing rules for GPT-5.6 and later versus earlier models.
- Claude prompt-caching docs (cited, not re-fetched for this issue;
  accessed 2026-11-05):
  `https://platform.claude.com/docs/en/build-with-claude/prompt-caching` —
  `cache_control` breakpoints, `cache_read_input_tokens` /
  `cache_creation_input_tokens`. Gateway applicability unknown (Section 3.5).
- OpenCode Go docs (cited; accessed 2026-11-05):
  `https://opencode.ai/docs/go/` — gateway endpoint and protocol routing
  context. Route evidence comes from qcode source, not this page.
- Ollama chat API docs (cited; accessed 2026-11-05):
  `https://docs.ollama.com/api/chat` — `/api/chat` request/stream shape;
  no cache surface.
- qcode sources at baseline `768e834`: `internal/llm/openai.go`,
  `internal/llm/chatgpt.go`, `internal/llm/opencode_go.go`,
  `internal/llm/responses.go`, `internal/llm/anthropic.go`,
  `internal/llm/ollama.go`, `internal/llm/thinking.go`,
  `internal/llm/types.go`, `internal/agent/persistence.go`,
  `internal/agent/usage.go`, `internal/agent/agent.go`;
  `docs/providers.md`, `docs/token-usage.md`, `docs/context.md`.

## 11. Verification

- Route names, model IDs, headers, and usage locations verified against the
  files listed above; links verified against `docs/providers.md`,
  `docs/token-usage.md`, and `docs/context.md`.
- Fixture-based tests only; no live API calls: `go test ./...`.

## 12. Normalized usage and session coverage contract (schema 2)

Issue #73. Shared contract only — no adapter cache mapping, no aggregation
UI, no costs, no provider capability switches.

### Per-request `llm.Usage`

- All fields are optional `*int`: `InputTokens` (complete logical input),
  `OutputTokens`, `CacheReadTokens`, `CacheWriteTokens`,
  `UncachedInputTokens`, `ReasoningTokens`. `nil` means unknown; a pointer
  to zero is an explicitly reported zero.
- JSON keys: `prompt_tokens`, `completion_tokens` (kept),
  `cache_read_tokens`, `cache_write_tokens`, `uncached_input_tokens`,
  `reasoning_tokens`. Unknown serializes as `null`, never as numeric zero.
- `InputTokens` is inclusive of cache reads/writes exactly once.
  `OutputTokens` already includes reported reasoning when the protocol says
  so. Reasoning and cache counters are breakdown subsets and are never added
  to totals. Per-request total is the sum of known input and known output
  (partial subtotal when either side is missing); 1000 input / 100 output /
  800 cache-read / 20 reasoning yields 1100.
- Nil/all-unknown `Usage` is absent usage. Negative wire values become
  unknown for that field. Cache reads or writes individually exceeding known
  inclusive input — or a known breakdown sum exceeding it — are discarded
  for ratios/aggregation while valid input/output totals are retained.
  Reasoning exceeding output becomes unknown. Inconsistent optional metadata
  never fails successful text generation.
- Provider wire JSON structs stay private to adapters
  (`openAIUsageWire`, `responsesUsageWire`, Anthropic/Ollama event shapes);
  wire usage is never decoded directly into normalized `Usage`. Shared
  validation lives in `llm.NormalizeUsage` with presence-safe accessors
  (`KnownInput`, `KnownOutput`, `KnownCacheRead`, `KnownCacheWrite`,
  `KnownUncached`, `KnownReasoning`, `Total`, `IsAbsent`, `NewUsage`,
  `IntPtr`). Consumers must never dereference unknown fields or display
  them as a reported zero.
- `llm.Request` carries an inert `PromptCacheKey string` transport hint for
  later benchmark/control issues. Adapters must not emit it until a control
  policy is implemented.

### Session `llm.SessionUsage`

- Existing integer sums and JSON names are kept: `input_tokens`,
  `output_tokens`, `total_tokens`, `missing_completions`. `TotalTokens` is
  the sum of the known input and known output components (partial subtotal
  when either side is missing). `Missing` keeps its meaning: completions
  with no usable usage fields.
- Added coverage bookkeeping (all `snake_case`): `requests`,
  `input_reported`, `output_reported`, `cache_read_reported`,
  `cache_write_reported`, `cache_read_tokens`, `cache_write_tokens`,
  `cache_ratio_read_tokens`, `cache_ratio_input_tokens`,
  `cache_ratio_requests`, `legacy_uncertain`. Counters count requests,
  never token quantities.
- Cache-ratio sums include only requests with both known complete input and
  known valid cache-read counts. The hit ratio is
  `cache_ratio_read_tokens / cache_ratio_input_tokens` and is unavailable
  when there are no eligible requests or the denominator is zero; it can
  never be negative or exceed 100%. `cache_ratio_read_tokens` is separate
  from every reported cache-read sum.
- `legacy_uncertain` marks sessions that may mix pre-schema-2 ambiguous
  zeros with presence-aware counts (e.g. checkpoints restored from before
  `UsageSchemaVersion` 2). Trace `usage` events carry
  `usage_schema_version: 2` (`llm.UsageSchemaVersion`).
- Specialized adapter cache mapping (#74–#76) and full aggregation UI (#77)
  build on this contract; this issue only keeps those call sites compiling
  with minimal presence-safe behavior.
