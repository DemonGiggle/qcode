# Cache benchmark rollout report — epic #70, issue #82

Date: 2026-11-05. Candidate revision: `89bcfc2` (feat branch tip,
includes #81). Baseline emulation: same binary with
`Request.PromptCacheKey` stripped before the provider, which is
wire-identical to the pre-#81 code at `32efbc9` (whose `openai.go`
never emitted the field). Doc baseline: `docs/cachebench.md` and
`docs/prompt-caching.md` as committed at `89bcfc2`.

**Headline: no live evidence exists. Nothing below measures a real
provider cache. No billing-saving or hit-rate claim is made.**

## 1. Offline comparable run (the only run performed)

Harness: `cmd/qcode-cachebench` (`--compare` mode) plus a temporary
`httptest` OpenAI SSE driver (removed after use, not committed).
Both variants used identical synthetic fixtures, model
(`bench-model`), provider (`openai`), protocol
(`chat_completions`), and settings (`{}`).

- 5 repetitions per workload/variant; each repetition used a fresh
  routing/cache identity, reused across that repetition's turns.
- First (`turn 0`, label `cold`) vs repeated (`turn > 0`, label
  `warm`) turns recorded separately; `cold`/`warm` are order labels,
  never an eviction claim.
- Stable-prefix workload ran alongside the three control workloads
  (early-context, tool-schema, compaction) in one grid.
- Canned usage rotation per turn: full read (1000 in / 800 cached),
  zero-read explicit, partial (no cache detail), none — so coverage
  accounting is exercised, not just the happy path.
- 60 samples per variant (4 scenarios x 3 turns x 5 reps);
  order and interval identical across variants.

Results (`docs/cachebench-offline-compare.json`):

| Scenario | Samples/var | Failed | Cached share before → after | Coverage | Output Δ | Latency p50 Δ |
| --- | --- | --- | --- | --- | --- | --- |
| stable-long-prefix | 15 | 0 | 0.364 → 0.364 | 0.67 | 0 | −0.02 ms |
| dynamic-early-context | 15 | 0 | 0.364 → 0.364 | 0.67 | 0 | +0.01 ms |
| changed-tool-schema | 15 | 0 | 0.800 → 0.800 | 0.33 | 0 | −0.01 ms |
| compacted-history | 15 | 0 | 0.000 → 0.000 | 0.33 | 0 | −0.02 ms |

No warnings from `--compare`. Sub-millisecond latency deltas are
loopback noise, not a cache effect. `provider_reported_cost` is
null on all 120 samples (adapters report no cost; correct per
contract, not missing data). Unknown cache fields stay null, never
zero. Share/coverage values above reflect the canned fixtures, not
provider behavior — the `httptest` server implements no prefix
cache, so before/after equality is the expected accounting-integrity
signal, not an efficacy result.

Compatibility (offline): 0 failed samples in either variant; no
`prompt_cache_key` appeared on any of the 120 wire bodies (test URL
is non-official, gate correctly omits). Request-level check:
baseline carried no key on all 60 calls (pre-#81 shape); candidate
attached exactly one stable identity per repetition (5 distinct
keys x 12 turns). Official-route emission itself is covered by
committed unit tests with injected transports, no live calls
(`TestOfficialOpenAIEmitsStableKey`,
`TestCustomOpenAIBaseURLOmitsKey`, `TestOpenCodeGo*OmitsKey`,
`TestOllamaOmitsKey`, `TestChatGPTRouteOmitsKey`).

## 2. Coverage gaps (untested routes)

No live `--live` run was performed for any route. Standing:

- Official OpenAI Chat Completions: **untested live**. Blocker:
  `OPENAI_API_KEY` absent, `QCODE_API_KEY` absent.
- OpenCode Go routes (Chat Completions default, Responses and
  Messages route maps): **untested live**. Blocker: no gateway
  credentials configured (`QCODE_API_KEY`/`QCODE_BASE_URL` absent).
- ChatGPT (Responses): **untested live**. A
  `~/.config/qcode/chatgpt-oauth.json` file exists (presence only,
  never read), but validity is unknown and no live run was
  attempted; live use needs explicit opt-in with a pinned model.
- Ollama: **untested live**. `OLLAMA_BASE_URL` is set in the
  environment, but no model was pinned and no live run was
  attempted; left for a follow-up `--live` session.
- Model-generation variability vs cache effect: inseparable
  without live repeated-turn data; the harness reports means over
  5 reps but live conclusions need the same treatment.

## 3. Recommendation: RETAIN (with live-evidence caveat)

Retain the #81 session-stable `prompt_cache_key` optimization:

- Compatibility: offline runs show zero failures and the gate
  holds (field only on official endpoint; all other routes omit;
  never retried after rejection — errors surface normally).
- Unit tests pin partitioning (chat/compact/learn purposes),
  isolation (per-agent UUID, legacy regeneration), and absence
  from traces.
- Revert trigger (not met): any cache-field compatibility failure
  on an allowed endpoint, or live data showing harm. If live reuse
  shows no improvement, report it as-is; efficacy expansion needs
  a new reviewed issue.

No billing or hit-rate improvement is claimed or promised.

## 4. Child acceptance check (#72–#81, at `89bcfc2`)

- #72 matrix: `docs/prompt-caching.md` covers all four registered
  providers, route map, and custom-endpoint caveats.
- #73–#76 usage contracts: `llm.Usage` presence-aware
  (`KnownInput`/`KnownCacheRead`/…), inclusive-vs-separate
  normalization in `usage.go`, Anthropic separate-input mapping,
  Ollama presence-aware mapping; no double counts.
- #77 aggregation: per-field coverage preserved, legacy totals
  flagged uncertain, `/new` resets, save compatibility kept.
- #78 reporting: `/usage` view plus compact status bar; reported/
  partial/unavailable distinguished; no price inference.
- #79 context: prepared-request estimates include request-only
  content; prefix-stability tests pass; no role/content moves.
- #80 harness: committed, opt-in, `--dry-run` sends 0 requests,
  offline unit tests, sanitized output (sentinel-tested).
- #81 key lifecycle: endpoint-gated, session-stable, namespaced
  per purpose; Go header unchanged; gateway routes omit.

## 5. Tests (2026-11-05, this branch)

- `go test -count=1 ./...`: all 22 packages pass, 0 failures.
- `go test -count=1 -race ./internal/agent ./internal/tui
  ./internal/session`: pass.
- `tools/qcode-tester`: untouched by this task; module tests not
  re-run per issue #82 scoping.
- `go run ./cmd/qcode-cachebench --dry-run`: 4 scenarios x 5 reps
  = 60 requests planned, 0 sent (credential/config resolution
  only, no network).

## 6. Missing-live-evidence checklist (blocker for closing #82/#70)

- [ ] One live baseline/candidate `--live` pair on official
  OpenAI with identical model/settings, 5 reps, recorded revision
  and date.
- [ ] Representative live runs (or documented unavailability +
  reason) for Go Chat/Responses/Messages routes, ChatGPT, Ollama.
- [ ] Token sums, cache-read/write coverage, paired cached-input
  share, first-token/total latency where exposed, output lengths,
  failures, null-cost confirmation from live JSONL.
- [ ] Parent (#70) checklist updated with evidence links; #70
  stays open until the above land. (Not done here: issue edits
  are outside this branch's scope; left for the orchestrator.)

Evidence files (all sanitized; scanner-verified free of prompts,
responses, tool args, credentials, paths, raw payloads, and cache
identities): `docs/cachebench-offline-baseline.jsonl`,
`docs/cachebench-offline-candidate.jsonl`,
`docs/cachebench-offline-compare.json`.
