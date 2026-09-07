# Scroll history during agent runs

Status: planned; implementation has not started.

## Problem and intended behavior

PageUp/PageDown is discarded whenever interruptReader has a cancellation
handler. Simply removing that guard would allow streamed output to overwrite
the page being read. The current page offset is relative to the history tail,
so new output also changes which content an offset identifies.

PageUp should let the user read older output while the agent continues working.
The visible content should stay anchored as output arrives. PageDown reaching
the latest content should resume live display. Cancellation and tab switching
must continue to work. Each tab retains its own reading position. Completion
does not force the user back to the bottom.

## Architecture

### One viewport state per conversation

Introduce a small viewport type with explicit following and browsing modes.
Store it on each agent view, with equivalent state for the standalone UI path.
Replace the overlapping UI pageOffset/pageActive and agentView.page state;
do not maintain a second global copy of the selected tab's viewport.

The viewport owns navigation and its anchor, not terminal I/O. Anchor browsing
to a stable logical history line ID and a position within that line, rather
than distance from the moving tail. Reflow resolves that position into wrapped
rows at the current width. If history retention evicts the anchor, clamp to the
oldest retained content. Keep the existing bounded history policy.

### History records; display decides whether to paint

History remains the source of truth and records all agent output, including
while browsing or viewing another tab. Provide a consistent snapshot with
stable line IDs and the current unfinished line for repainting. Preserve ANSI
styles and existing progress-rewrite handling; partial UTF-8/ANSI input must
remain buffered until valid. Do not make visual repaint output part of history.

The display adapter forwards output only for the active conversation in
following mode. Returning to following mode reconstructs the current visible
tail, including recorded unfinished text, before incremental output resumes.
This must not flush Markdown buffers or alter provider streaming semantics.

### One screen synchronization policy

Use screenMu for active-view selection, viewport mutations, output visibility
decisions, and complete screen updates. Remove pageMu as viewport ownership
moves under screenMu. Consolidate page display, return-to-tail, tab repaint,
and resize repaint behind shared rendering helpers.

Take a history snapshot under its own lock while holding screenMu when needed;
history must never call back into the UI while holding its lock. Avoid acquiring
Markdown writer locks while holding screenMu, since Markdown output enters the
display adapter. Audit line-editor callbacks before expanding lock coverage:
they may enter the UI while the line editor holds its own lock. Resolve any
inverse lock order rather than wrapping existing functions indiscriminately.

Keep the input reader responsible for recognizing keys and cancellation.
Allow page navigation regardless of task cancellation state, preserve raw
selector input, and keep unrelated typing discarded during a running task.
Navigation callbacks must perform bounded work and must not wait for the agent.

### Layout and lifecycle

Use a shared calculation of conversation rows so page content, navigation
information, task indicator, tabs, and status cannot overwrite one another.
Preserve the existing prompt and draft behavior when idle. An approval prompt
must become visible and readable even if the user was browsing; explicitly
return to live display before presenting an approval rather than allowing a
hidden prompt to receive input. Resize and tab switching resolve the stored
anchor against current history. Handle terminals too short for reserved rows.

## Implementation sequence

1. Extend history snapshots with stable coordinates and unfinished-line data;
   implement and test viewport movement, reflow, and retention clamping.
2. Consolidate viewport ownership and rendering synchronization across active
   output, page navigation, tab switching, and resize. Audit lock ordering.
3. Enable page keys during runs and integrate return-to-live, completion,
   cancellation, and approval behavior in both managed and standalone paths.
4. Add focused regressions, verify terminal behavior, and update interface docs.

## Validation and acceptance

- Input tests: page keys during a run, split escape sequences, cancellation,
  tab shortcuts, and raw selector forwarding.
- History/viewport tests: streamed append preserves the anchor; PageDown resumes
  following; long wrapped lines, CJK, ANSI styles, unfinished lines, resize, and
  retention eviction produce coherent pages.
- UI tests: streaming does not overwrite browsing; completion preserves it;
  switching tabs restores each viewport; approvals are visible; terminal rows
  and input drafts remain intact. Exercise concurrent output and navigation.
- Run focused tests during implementation, then `go test ./...` and
  `go test -race ./internal/tui ./internal/agent`.
- Exercise the interactive demo in a terminal with output arriving during
  paging, cancellation, tab switches, and resize. Report any validation limits.

## Scope

No new UI framework, provider changes, unbounded output buffers, or terminal
scrollback dependency. This PR concerns application history paging only.
Keep the PR draft until implementation and validation are complete.
