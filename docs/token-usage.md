# Session token usage

The status bar shows compact input/output counts such as `TOK I:1.2K O:340` for the active agent's session. It also includes the combined total, such as `Σ:1.5K` (`T:1.5K` in ASCII mode), whenever it fits the actual one- or two-line layout. If space is tight, the total is omitted before the whole token segment is dropped. There is no fixed minimum terminal width. Counts accumulate across completion requests, including tool-loop steps, compaction, and learning requests.

Before any requests, totals are zero. If a provider omits usage, the display shows `unknown` when no tokens have been reported, or adds `?` to reported totals. Failed or cancelled requests without usage also make totals incomplete. These are provider-reported token counts, not cost estimates.

The mocked demo provider omits usage, so its token totals become `unknown`
after a completion. Its estimated `CTX` percentage is separate from reported
token totals; see [Context usage](context.md).

Each agent owns its counters. Switching tabs shows that agent's totals; a new agent starts at zero. Changing models within a conversation retains its totals. `/new` clears both totals and the missing-usage state for the active agent.

## Detailed view (`/usage`)

The narrow status bar never expands with full cache statistics; `/usage` owns the detailed view. It is read-only for the active agent and safe to run while that agent is working. Switching tabs shows the newly active agent's totals.

`/usage` prints input, output, and total tokens, cache-read and cache-write sums, the reported-request coverage for each, the missing-completion count, failed requests without usage, and legacy uncertainty. Partially reported sums print with coverage, for example `500 (reported for 2/3 requests)`. When coverage is zero the field prints as `unavailable` instead of a misleading zero; Ollama never reports cache reads, so its cache-read field stays `unavailable`, never `0`. Gemini official endpoints use native `usageMetadata` (#103/#104): `promptTokenCount` → input, `candidatesTokenCount`+`thoughtsTokenCount` → output (reasoning is a subset, never added), `cachedContentTokenCount` → cache-read, `thoughtsTokenCount` → reasoning. `/usage` therefore shows a real `Cached input share` with coverage. The OpenAI-compat proxy path still reports via `stream_options.include_usage` without cache details, so its cache-read stays `unavailable` — expected, not a zero hit rate.

The ratio line is labeled `Cached input share (reported subset)`. It uses only the paired sums for requests with both known input and known cache-read counts, with request coverage (for example `80.0% (based on 1/3 requests)`). A zero denominator prints as `unavailable`. The line includes the underlying token counts. Positive shares too small to display at one decimal print as `<0.1%`. An explicit zero from SIWC stays zero. When a Responses cache comparison is available, the report also shows the type and reason for the latest completed turn.

`Missing completions` counts successful provider calls with no usable usage. A rejected, interrupted, or otherwise failed request with no usable usage increments `Failed requests without usage`. A failure carrying partial usage contributes those counts once. The request count includes all three outcomes. Older saved sessions can include failed calls in the historical missing count because they recorded no separate failure counter.

## Meaning of the numbers

- Cached tokens occupy context and are included in input totals. Cache-read and cache-write details are subset breakdowns; they are never added to totals.
- Unknown differs from zero: `unknown` or `unavailable` means the provider did not report that field, while `0` means the provider explicitly reported zero. A genuinely empty session with no requests shows zero.
- Partial subtotals are not bills: a sum with `?` or a `(reported for m/n requests)` note covers only the reported subset and must not be treated as a complete bill or cost estimate. Counts have no pricing table and are never presented as billing.
- Mixed-provider totals are token sums only: changing models keeps accumulating into the same session counters, so totals that span providers are plain token sums, not normalized billing units.

With `--json-events`, each completion attempt emits an `event: "usage"` record containing provider `name`, `model`, per-completion `usage` (null if unavailable), and `session_usage`. The latter contains `input_tokens`, `output_tokens`, `total_tokens`, `missing_completions`, and `failed_without_usage` plus per-field coverage (`input_reported`, `output_reported`, `cache_read_reported`, `cache_write_reported`, `requests`), cache sums, paired ratio sums, `legacy_uncertain`, and `usage_schema_version` 2. Per-request `usage` uses numeric values for reported fields and null for unknown fields. Usage events carry accounting only: no prompt content, cache keys, raw provider payloads, credentials, or cost estimates. Usage events remain available with verbose tracing disabled.
