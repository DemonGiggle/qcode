# Global learning

Use `/learn` after a conversation to propose durable preferences and reusable procedures for future sessions. qcode shows a compact preview with colored Add, Update, and Remove labels, the exact content and tags, and previous text for updates. Storage metadata stays out of the preview. qcode asks for approval before saving anything. Answer `y` or `yes` to apply the batch; Enter, `n`, or Ctrl+C cancels it. Proposals use a separate model request without tools and do not enter conversation history.

## Commands

- `/learn` proposes additions or updates from recent user/assistant text.
- `/learn list` shows all global records and IDs.
- `/learn forget <id>` reviews and deletes a record after approval.
- `/learn compact` proposes additions, rewrites, or deletions to consolidate duplicates; approval is required and a backup is created before application.

One-shot runs can retrieve existing learning; mutation commands require the interactive UI. Demo mode does not access the learning store.

## Scope

There is one store per OS user on each device, shared across all workspaces. V1 has no workspace state, `/learn global` selector, cloud sync, or model training. Repository-only facts should be omitted; reusable procedures should state the language, framework, or other conditions under which they apply.

## Storage

Records live in `v1/global/<id>.json` under:

| Platform | Learning directory |
| --- | --- |
| Linux | `$XDG_STATE_HOME/qcode/learning`, or `~/.local/state/qcode/learning` |
| macOS | `~/Library/Application Support/qcode/learning` |
| Windows | `%LocalAppData%\qcode\learning` |

Each record contains:
- **ID**: 32-character hex random ID
- **Topic**: ≤160 bytes
- **Content**: ≤4096 bytes
- **Tags**: ≤16 tags, ≤64 bytes each (e.g., "go", "testing")
- **Timestamps**: Created and updated times
- **Source**: Session ID that created it

Writes use private files, an OS-level process lock, and a staged directory swap. If any record changes after review, the operation is rejected and must be reviewed again. Failed swaps roll back, and interrupted swaps recover on the next access. Invalid or unsupported-version records are skipped with warnings and preserved. Compaction backups are retained in `v1/backups/`; deleting a record does not remove copies from existing backups.

## Context retrieval and ranking

Before each normal model request, qcode ranks global learning against the latest user prompt:

1. **Tokenize** the query into keywords (lowercase, ≥2 chars, excluding stop words)
2. **Score** each record:
   - Topic/tag match: **+3 points** (primary/anchor)
   - Content match: **+1 point** (body)
3. **Filter** requires:
   - ≥2 matching terms total
   - ≥1 anchor match (topic/tag)
   - If the record has tags, ≥1 tag must match the query
4. **Budget check**: Items are added until the estimated token budget is exhausted
5. **Max 3 items** returned

Selected learning is reference context and must not override current user instructions. It is added only to that request, so repeated model turns do not accumulate learning copies in conversation history.

```toml
[learning]
context_budget = 1200
```

The budget is an approximate token estimate based on UTF-8 bytes, including the reference preamble; supported values are 0–12000. Set 0 to disable retrieval without deleting records or disabling `/learn`. `/new` clears conversation and injected context, but retains global learning.

## Extraction

Extraction uses at most 24 KiB of recent user/assistant text and excludes tool outputs, tool arguments, images, and model reasoning. Common credential patterns are redacted from extraction input and rejected in proposed records, but this is not a complete secret detector: review proposals before approving them. Raw transcripts are not written to the learning store.

## Security and validation

- **Secret detection**: Rejects records containing private keys, API keys, passwords, etc.
- **Path traversal prevention**: IDs must match `^[a-f0-9]{32}$`, preventing `../` escapes
- **No control characters** allowed (except newlines/tabs in content)
- **Strict JSON parsing**: Uses `DisallowUnknownFields()` for validation

## Limits

V1 limits the store to 512 entries, record content to 4096 bytes, a proposal to 32 changes, and model review input/output to 64 KiB each. When the existing store is too large for a review request, `/learn forget` can remove selected records without a model call. Oversized or non-regular files introduced outside qcode must be repaired before further writes can safely preserve the store.
