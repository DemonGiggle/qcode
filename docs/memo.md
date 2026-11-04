# Memos

`/memo` is a lightweight, global scratchpad for ideas you do not want to lose
while qcode is busy. It never steers the active agent, never queues model
work, and is never injected into model context (unlike `/learn`).

## Quick capture

```text
/memo check edge case where path has spaces after the refactor lands
/memo
```

- `/memo {text}` saves one memo instantly, while the agent is idle and while
  it is busy (Steer/Queue states).
- Saving does not clear or clobber unfinished input drafts, does not submit a
  prompt, and stays on the same view with a brief `Memo saved (N total)`
  confirmation.
- `/memo` with only whitespace opens the manage view instead of saving an
  empty memo.

## Global scope

- The memo list is global: directories and workspaces share the same list.
- Memos persist across sessions, `/new`, `/resume`, tab switches, and full
  restarts, under the OS state dir (`qcode/memos`, e.g.
  `~/.local/state/qcode/memos` on Linux,
  `~/Library/Application Support/qcode/memos` on macOS,
  `%AppData%/qcode/memos` on Windows).
- V1 has no workspace scoping or filtering. Limits: 200 memos, 4 KiB per memo
  (after trimming surrounding whitespace). Multiline text (newlines/tabs) is
  allowed via the manage-view add/edit flow; quick-capture is usually one line.

## Manage view

Bare `/memo` opens the manage view:

- Lists memos newest-first with created time and single-line preview.
- Empty state hints `/memo {text}` usage.
- Actions: view (full scrollable text), edit (composer prefilled), del (with
  confirmation, Cancel default), add (same editor as edit), put to prompt.
- Keys follow `/history` and `/resume` conventions: Up/Down, PgUp/PgDn,
  Enter to view, `v` view, `e` edit, `d` delete, `a` add, `p` put, `q`/Esc
  back, Ctrl+C close. Esc from the top level returns to the composer with the
  draft and reading position restored.

## Put to prompt

- Selecting put fills the current TUI input box without submitting, so you can
  edit before pressing Enter.
- If the input box already has draft text, qcode asks for confirmation and
  only replaces on explicit approval (Cancel is the safe default).
- After confirmation, choose put and remove (default: one-shot ideas are
  consumed) or put and retain (keep the memo). Esc cancels without changes.

## Differences from existing features

- `/learn [list|forget|compact]`: reusable instructions fed to the model.
  `/memo` is user-owned and never enters model context.
- `/todo show`: the agent's execution checklist for the current task.
  `/memo` is global and user-owned.
- `/history`: completed prompts/responses. `/memo` holds un-sent future ideas.

## Non-interactive use

V1 is TUI-only. One-shot prompts, piped input, non-TTY runs, `--json-events`,
demo mode, and remote control do not expose memo operations; the manage view
requires an interactive terminal and quick-capture reports
`Memos are unavailable in this run` when no store is configured.
