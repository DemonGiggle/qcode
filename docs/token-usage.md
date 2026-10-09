# Session token usage

The status bar shows compact input/output counts such as `TOK I:1.2K O:340` for the active agent's session. It also includes the combined total, such as `Σ:1.5K` (`T:1.5K` in ASCII mode), whenever it fits the actual one- or two-line layout. If space is tight, the total is omitted before the whole token segment is dropped. There is no fixed minimum terminal width. Counts accumulate across completion requests, including tool-loop steps, compaction, and learning requests.

Before any requests, totals are zero. If a provider omits usage, the display shows `unknown` when no tokens have been reported, or adds `?` to reported totals. Failed or cancelled requests without usage also make totals incomplete. These are provider-reported token counts, not cost estimates.

The mocked demo provider omits usage, so its token totals become `unknown`
after a completion. Its estimated `CTX` percentage is separate from reported
token totals; see [Context usage](context.md).

Each agent owns its counters. Switching tabs shows that agent's totals; a new agent starts at zero. Changing models within a conversation retains its totals. `/new` clears both totals and the missing-usage state for the active agent.

With `--json-events`, each completion attempt emits an `event: "usage"` record containing provider `name`, `model`, per-completion `usage` (null if unavailable), and `session_usage`. The latter contains `input_tokens`, `output_tokens`, `total_tokens`, and `missing_completions`. Usage events remain available with verbose tracing disabled.
