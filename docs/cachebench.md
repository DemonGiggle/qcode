# Synthetic prompt-cache benchmark (opt-in harness)

Epic #70, issue #80. Harness only: this command replays committed synthetic
workloads and records normalized usage plus monotonic latencies as JSONL. It
produces no live measurements itself; live before/after runs belong to a
follow-up that uses this harness. Baseline: historical `768e834` lacks cache
telemetry and cannot supply a trustworthy cached-token baseline, so do not
treat pre-accounting logs as a baseline.

## Build

```sh
go build ./cmd/qcode-cachebench
go test ./cmd/qcode-cachebench ./internal/llm
```

## Run

Credentials come from qcode config files and environment variables
(`QCODE_BASE_URL`, `QCODE_API_KEY`, `OPENAI_API_KEY`). There is no
`--api-key` flag by design. Network calls require explicit `--live`;
`--dry-run` validates setup and scenarios with zero requests. CI runs only
`--dry-run` and the offline unit tests, never `--live`.

```sh
# Validate without network (0 requests sent):
go run ./cmd/qcode-cachebench --dry-run

# Live before/after pair (example; use the same model for both):
go run ./cmd/qcode-cachebench --live --provider openai --model <id> \
  --variant baseline --repetitions 5 --out /tmp/bench-before.jsonl
go run ./cmd/qcode-cachebench --live --provider openai --model <id> \
  --variant candidate --repetitions 5 --out /tmp/bench-after.jsonl

# Compare the pair:
go run ./cmd/qcode-cachebench --compare /tmp/bench-before.jsonl /tmp/bench-after.jsonl
```

Each repetition uses a fresh routing/cache identity where supported (new
provider instance per repetition, hence a new `opencode-go` session ID; a
fresh `PromptCacheKey` identity is attached and reused within the repetition)
and reuses that identity across the repetition's turns. `cold`/`warm` are
request-order labels (turn 0 vs later turns), never a cache-eviction claim.

## Synthetic workloads (committed text only)

All prompts, tool schemas, and tool results are fixed strings in
`cmd/qcode-cachebench/scenarios.go`. No real tools execute; tool results are
canned replays. Sizes:

| Scenario | Turns | Shared prefix | Per-turn addition | Tools |
| --- | --- | --- | --- | --- |
| `stable-long-prefix` | 3 | ~1277 chars fixture | short tally request (<60 chars) | 2 fixed |
| `dynamic-early-context` | 3 | same, but turns 1–2 prepend a ~110-char shift line | same | 2 fixed |
| `changed-tool-schema` | 3 | same stable prefix | same; schema v1 on turn 0, v2 after | 2 (v2 adds optional `detail`) |
| `compacted-history` | 3 | full history turn 0, 139-char synthetic summary after | same | 2 fixed |

The prefix is long relative to additions so repeated turns share a stable
prefix that real prefix caches can exploit; the three control scenarios each
break one cacheability assumption (early-context stability, schema
stability, history length).

## Comparison output

`--compare` requires matching provider/model/protocol/settings across both
files (variant labels may differ — that is the point) and rejects mismatched
or mixed-identity inputs. Per scenario it reports, split into first
(`turn 0`) vs repeated (`turn > 0`) turns:

- input-weighted cached-input share `sum(cache_read)/sum(input)` over
  eligible requests only (both fields known and valid), plus the eligible
  fraction (coverage);
- latency p50/p90 summaries and the p50 delta;
- mean generated output-token delta and failed-sample counts.

Failed samples are included in totals and flagged via warnings, never
silently dropped; a comparison over only successful high-hit runs is
rejected by reporting failures, not by filtering them.

## Thresholds, nondeterminism, and unknowns (read before drawing conclusions)

- There is no pass/fail hit-rate gate in this harness. Cache-ratio sums
  follow the accounting contract (`docs/prompt-caching.md`): only requests
  with both known complete input and known valid cache-read counts are
  eligible; missing cache metrics are never treated as zero.
- Model output is nondeterministic: output-token counts and latencies vary
  run to run. Compare repeated-turn deltas across the full `--repetitions`
  set, not single samples.
- Routing follows `llm.OpenCodeGoRoute` for `opencode-go` models; protocol
  labels record the route taken without implying capability. A custom
  `--base-url` keeps the same route on a different host.
- Retention and cross-run contamination are provider-side unknowns: prompt
  caches may persist across runs, variants, or repetitions. Space live runs
  apart, use distinct `--variant` labels, and record the `--git-revision`.
- `provider_reported_cost` is always null in this issue: verified adapter
  semantics report no cost, and token-price estimation is out of scope.
  Likewise, adapters without a documented cache-breakdown wire path report
  cache fields as unknown (`null`), never zero.
- Output JSONL contains only metadata, normalized usage (nulls preserved),
  monotonic durations, and sanitized error categories. It never contains
  prompts, responses, tool arguments, credentials, user paths, raw payloads,
  or cache identities.
