# Interface

The terminal UI is a full-viewport, keyboard-driven interface with editable input, per-agent history, full word wrapping, streamed responses, and ANSI-colored Markdown, including aligned GFM tables. Fixed tabs sit at the top and a status bar at the bottom. The normal terminal buffer and scrollback are retained.

The active agent's latest received prompt is pinned directly beneath the tabs, above the scrolling transcript. A `✦` marker (`*` in ASCII mode) introduces up to three wrapped lines on a full-width accent background; long prompts end with an ellipsis. The region updates when a task starts or a steer is delivered, and survives completion, cancellation, failure, `/clear`, and session restore. Queued tasks and pending steers leave it unchanged until delivery. `/new` clears it for the active agent. Small terminals reserve fewer prompt rows to leave room for output and input.

The editable composer is pinned above the task indicator and status bar from launch. It displays `>` for an idle agent, `(Plan)>` in Plan mode, `(Skill plan)>` in Skill Plan mode, and `(Steer)>` while the current tab's agent is working. Pending prompts appear in a reserved, multi-row queue area directly above the prompt. It groups pending steering separately from queued tasks, whose text stays in FIFO order; Alt+Q expands it for more room, and Page Up/Page Down scroll that expanded area. Alt+Q folds it back to the smaller queue area, where queued text remains visible. Page Up/Page Down scroll the transcript when the queue is folded. Each agent tab keeps its own queue view and draft. The queue area disappears when its pending work drains. The browser remote control has the same smaller and expanded queue above its input; its expanded list also supports mouse wheel and touch scrolling. The status bar includes `STEP current/max` for the active agent's model-turn progress and `MODE PLAN` when planning is active, or `MODE INTERACTIVE` when normal-mode questions are enabled. Long drafts wrap upward within the input area.

Typing `/` opens up to five matching command suggestions directly above the prompt. Type more characters to filter them or press Tab to complete the first match. Suggestions stay in their own area while output streams, and disappear when the draft no longer matches a command; they are not saved in conversation history.

Input editing follows terminal conventions: Left/Right moves by character, Ctrl+Left/Right and Alt+Left/Right move by word, Alt+B/F provide Meta word movement, Ctrl+W deletes the previous word, and Ctrl+A/E move to the beginning/end of the line. When the current view includes the end of the output, Home/End move to the beginning/end of the prompt. When the view is above the end, Home/End navigate the transcript.

Set `NO_COLOR=1` to disable response styling. UTF-8 terminals use Unicode interface glyphs; other locales fall back to ASCII without disabling color. Set `QCODE_ASCII=1` to force the ASCII-safe display mode.

## Slash commands

The following UI-focused commands are available in interactive mode. Feature-specific commands (`/agent`, `/skill`, `/learn`) are documented in their respective topic pages.

Use `/help` to list commands, or `/help <command>` to see a command's arguments and a short description. The leading `/` is optional in the command name.

Interactive command selectors use the same navigation rule: `Esc` goes back to the previous level, or leaves the command from its top level. `Ctrl+C` cancels the whole command without applying pending changes.

Special command views, including `/history`, plan and skill draft viewers, and
selectors, hide the pinned prompt, composer, and waiting indicator. Their footer
states that Ctrl+C closes the view; a running agent continues in the background.
Closing the view restores the pinned prompt, draft, and conversation reading
position. Ctrl+C in the normal composer cancels the active agent task.

- `/model` fetches the provider's models. Search a large catalog by typing, move through matches with Up/Down, and select with Enter.
- `/new` discards the current conversation context and resets session token totals without restarting qcode or changing the provider, model, or workspace.
- `/resume` opens this workspace's saved sessions with filtering and transcript previews. Up/Down and Page Up/Down navigate; Enter restores; Tab opens Rename, Pin/Unpin, and Delete; Ctrl+D opens Delete all; Esc or Ctrl+C returns to the composer. Selecting the current session keeps it open. Finish or cancel running agents before switching or deleting the current session. See [Saved sessions](#saved-sessions).
- `/clear` redraws the interactive banner, which lists enabled and disabled tools for the active agent.
- `/verbose` adds detailed timestamped telemetry, including raw tool arguments, without disabling concise activity events.
- `/maxsteps` shows the current per-request model-turn limit; `/maxsteps N` updates it for the active agent.
- `/statusline` opens a toggle list for status bar segments (remote, mode, model, think, ws, ctx, step, tok); Space toggles, Enter applies. `/statusline <name> on|off`, `/statusline hide|show a,b`, `/statusline show`, and `/statusline reset` work without the picker. Narrow terminals keep high-priority segments first (remote > mode > model > think > ws > ctx > step > tok) after shortening the workspace path. When segments still overflow one line they wrap to a second left-aligned status line; only when two lines overflow are low-priority segments dropped. Changes on main persist to `statusline_hidden` in config.toml.
- `/theme` opens a terminal-only picker with a live Markdown and status preview. Use Up/Down and Enter to apply, or Esc/Ctrl+C to cancel. It includes Default (Auto) and 22 dark/light palettes from Catppuccin, Dracula/Alucard, Gruvbox, Solarized, Nord, Tokyo Night, One, Rosé Pine, Everforest, Kanagawa, and Ayu. See [Configuration](configuration.md) for theme IDs and source palettes. The choice recolors retained and future terminal output, updates the browser's pinned-prompt accent and background, and saves to the user config from any agent tab.
- `/diff` and `/diff N` expand the latest write/edit diff preview (see below).
- `/history` opens a searchable, newest-first list of completed prompts for the active agent. Select a prompt to read only that prompt and its final response; leaving the browser restores the conversation view without changing its reading position.
- `/tool` enables or disables the web tools independently; see [Web tools](web-tools.md).
- `/plan` enters read-only planning mode; `/plan show` opens the latest plan in a scrollable view; `/plan off` leaves it; `/plan act` implements the latest submitted plan. See [Plan mode](plan-mode.md).
- `/interactive on|off` controls normal-mode questions for the active agent and saves the default for future sessions to the user config; `/interactive` reports the setting. See [Interactive questions](interactive-mode.md).
- `/skillplan [rough intention]` starts guided, review-first skill creation; use `/skillplan show`, `/skillplan create`, or `/skillplan off` to manage the draft. See [Skill Plan mode](skillplan.md).

## Saved sessions

Tab opens **Rename**, **Pin/Unpin**, and **Delete**, in that order, for the highlighted session. Use Up/Down and Enter in the menu, or Esc to return to the list. Short menus scroll to keep the selected action visible. Rename starts with the custom name and supports UTF-8 text, Left/Right, Home/End, Backspace/Delete, and Ctrl+U to clear. Enter saves trimmed text; an empty name restores the automatic label; Esc discards edits.

Delete opens a confirmation showing the session's name, with **Cancel** selected by default. Up/Down chooses Cancel or Delete; Enter applies the choice. Cancel or Esc returns to the actions menu; Ctrl+C closes the picker. Deleting another session retains the filter and selects the next matching row at the deleted position, or the previous row if it was last. An empty list stays open; clear the filter with Ctrl+U or exit. Removal errors appear inline and the list refreshes to reflect any metadata already removed.

**Ctrl+D** opens **Delete all** directly from the session list, as shown in the keyboard hints. It requires confirmation, with **Cancel** selected by default; Cancel or Esc returns to the list with the filter and selection intact, and Ctrl+C closes the picker. It removes every saved session, custom name, and pin in the current workspace, including entries hidden by the filter; other workspaces are unaffected. The hotkey works even when the filter matches no rows. If the current session is saved, Delete all prepares a fresh conversation and removes its snapshot last, after the other removals succeed. A failure retains the current conversation and refreshes the picker to show partial removal. An unsaved current conversation stays open.

Deleting the current session requires all local agents to finish or cancel. It clears all tabs, drafts, conversation history, plans, token totals, work history, added directory grants, and provider session identity, then returns to a fresh composer with one main agent. That agent keeps the current main agent's provider, endpoint, model, thinking level, runtime settings, skills, and tool selections, even when another tab was active. If preparing the replacement or deleting files fails, the current conversation stays open. The empty replacement is saved only once it contains ordinary savable content.

Each row shows its name or automatic conversation preview, prompt activity time, agent count, a current-session marker (`●`, or `*` in ASCII mode), and a distinct pin marker (`◆`, or `^`). Filtering matches custom names and automatic previews. Pinned sessions appear first; both groups keep their prompt-recency order until an accepted user prompt, including queued prompts and steering from the terminal or browser. Rename and pin changes preserve recency and selection; if a renamed session stops matching the filter, the first remaining match is selected.

A panel beneath the list uses the available command-view space for the latest nonempty lines of the saved active tab, including unfinished output. A blank gap and a labeled horizontal divider separate it from the session list, which shows up to eight rows. A bottom horizontal divider and a blank line separate the preview from keyboard hints and any inline error. Preview text keeps saved colors when terminal color is enabled; redaction still applies, and cursor movement and other terminal controls are removed. Small terminals reduce preview lines before list rows and hide the preview when needed. Edit and restore errors remain visible inside the picker.

Interactive sessions autosave changed state every two seconds and after agent events and commands, with a final save on clean exit or session switch. Each launch starts a separate session; resuming continues the selected session. Multiple processes can resume and save the same session. Each save atomically replaces the complete conversation snapshot without merging; the last successful replacement wins. Names and pins each use independent atomic replacement, so autosaves never overwrite those edits. Deletion removes the custom name and pin first, then the snapshot, without parsing them; corrupted sessions can be deleted. A later autosave from another process may recreate a deleted session. Current-session deletion never saves the departing conversation; subsequent saves use the replacement's new ID. Old `.lock` files are ignored. Empty launches and demo/one-shot runs are not saved. `/new` still resets only the active agent within the current saved session.

Resume restores all agent tabs and models, each agent's pinned latest prompt, conversation messages and images, retained styled output (including events, errors, thinking, and diffs), expandable diff data, drafts, reading positions, tool/skill settings, context accounting, and token totals. Existing directory grants are restored when their paths remain valid under current protections; discarded grants produce a notice. Provider credentials come from current configuration and are not saved. The retained history limit remains 5,000 lines per tab; terminal dimensions can change wrapping.

Sessions live outside the workspace: `$XDG_STATE_HOME/qcode/sessions` on Linux (default `~/.local/state/qcode/sessions`), `~/Library/Application Support/qcode/sessions` on macOS, and `%AppData%/qcode/sessions` on Windows. Canonical workspace paths identify session groups, so symlink aliases share sessions and separate worktrees do not. Snapshots contain conversation and tool output and use private file permissions where supported. Sessions are retained without automatic deletion. Custom names and pins live independently in `metadata/<session-id>.name.json` and `metadata/<session-id>.pin.json` beneath the workspace's session directory. Missing metadata means unnamed and unpinned. Metadata errors are reported while valid conversations can still be restored.

After a crash, recovery uses the latest successful checkpoint and marks unfinished agents interrupted. It preserves saved output but never automatically reruns tools or model requests. Unresolved tool results are marked as having an unknown outcome before the next user-directed run. Files on disk and external processes are not rolled back. Session-save errors are shown; a failed save prevents switching away from the current session.

## Steering and pending work

While an agent is busy, terminal Enter steers its current task; Tab queues a separate task (slash-command Tab completion still works). Browser Enter and **Steer** steer, **Queue** queues, and Tab keeps native focus navigation. Idle submission starts an ordinary task. Steering during manual `/compact` is unavailable and keeps your draft; Tab can queue work.

A steer waits for the streaming response or executing tool to finish. Completed output and side effects stay recorded. Unanswered approvals and questions are withdrawn, and unstarted tool actions are skipped before replanning. Esc defers terminal approvals or questions and opens the composer; Esc from the composer returns to the deferred interaction. Ctrl+C cancels active work.

Steering belongs to the current task and its one final answer. The newest pending steer replaces earlier pending steering; delivered instructions remain in the conversation. Pending work shows steering separately from queued tasks. Detailed steering lifecycle messages appear only with `/verbose` enabled. Alt+Q opens the pending panel: Up/Down selects, Page Up/Down scrolls, Delete removes a pending item, and Esc or Alt+Q returns to the composer. Browser pending items have **Remove** controls. Removal stops working once a steer starts replanning. Task cancellation or failure cancels undelivered steers while FIFO work continues. If the task finishes before a steer is accepted, the submission is rejected and its draft is preserved; it cannot steer a different task automatically. Resuming a session marks unfinished work interrupted without resubmitting it.

The terminal groups pending input under separate headings, without inline source or lifecycle labels:

```text
Steer
 ╰─ adjust the button to be more flexible

Queued | Alt+Q expand
 │  1. after that please commit the code
 ╰─ 2. The commit message should be as short as possible
```

ASCII mode uses `|` and `+-` for the tree connectors. Wrapped and multiline prompts keep their indentation; each item retains its identity for selection and removal.

## Activity events

Interactive mode always records concise colored activity events for tool calls and agent coordination, such as `Reading internal/tui/tui.go`, `Writing README.md`, or `Consulting agent-2`. These events are presentation-only and never enter the next model request. A task-level indicator remains visible while the active tab is running and includes the current model operation, time spent in that operation, and queued prompt count. For example, `Waiting (⠋) · 12s · Waiting for model response` identifies a pending provider response even before any text arrives. The indicator updates for streamed thinking or responses and conversation compaction. The waiting spinner and elapsed time clear when the model call finishes. Tool execution and agent coordination appear in the transcript; during those operations the indicator shows only input controls and the queued prompt count. Providers that do not stream thinking remain labeled as waiting for a model response until visible text arrives. Pending approvals and questions show `Waiting for input`. The input remains editable: Enter steers the active task and Tab queues a nonempty prompt as a separate task. Page Up/Page Down, Ctrl+C cancellation of the running prompt, and tab switching remain available.

## Scrolling

Use Page Up and Page Down to scroll through qcode's output history, including while an agent is running. Each page retains two visible rows from the previous page for reading context, including wrapped text. Very short views move at least one row. Scrolling back pauses the live view: new output is still recorded, but the page you are reading stays in place. Page Down to the latest content resumes live output.

When the current view is above the end of the output, Home jumps to the oldest retained transcript row and End jumps to the latest content and resumes live output. When the view includes the end of the output, Home/End move the prompt cursor to the beginning/end. This depends on the visible position, including after resizing or changes to the prompt layout. History browsing is per agent tab; an expanded queue does not change these Home/End bindings. In the `/remote` browser page, use the **Beginning** and **Latest** buttons, or Home/End while focus is outside text fields, command panels, and the expanded queue list. Home/End keep their normal editing behavior in browser text fields.

Each agent tab remembers its reading position across tab switches and terminal resizing. Completion and cancellation do not force a paused view to the bottom. Directory approval requests return to live output so the question is visible before you answer. History is bounded; if the content you were reading is evicted, the next repaint shows the oldest retained content.

## Thinking streams

Provider-delimited model thinking streams in subtle gray, while the final answer retains normal Markdown styling. Ollama uses `message.thinking`; OpenAI-compatible providers may use `reasoning_content` or `reasoning` deltas.

## Diff previews

Interactive `write` and `edit` tool calls display numbered, 10-line Codex-style diff previews with a file summary, change counts, guided body, and colored additions, removals, and hunk headers. Use `/diff` or `/diff N` to expand a preview up to the 200-line safety limit; tool results sent back to the model remain plain text.

## Run completion notice

Successful interactive runs end with a distinct colored `Completed in ... (MM/DD HH:mm)` notice measuring the complete run across every model turn and tool call.

## HTML exports

Use `/export` or `/export pretty` to save a readable conversation export. Each
agent has its own tab, with completed prompts and final responses ordered oldest
first by submission time. Each exchange starts as a prompt card; select a card
to reveal its full response. Responses render as Markdown, including code blocks
and tables. Closed agents with completed history are included. Consultations,
unsuccessful requests, and work still in progress are omitted from this view.

Use the **Show detailed tool events** checkbox to reveal recorded operations
such as shell commands, file edits, searches, and agent actions. Events are
hidden by default and appear in the exchange where they ran. qcode retains up to
256 concise events per request; tool output is not included. Older saved sessions
may not have structured tool events to display.

Use `/export raw` for the full styled transcript, including tool activity,
thinking, errors, and diffs. Raw exports use the full available archive, including
output removed from the terminal's 5,000-line viewing window or by `/clear`.

Both modes generate a standalone HTML file. The terminal saves it in the
workspace as `qcode-session-<mode>-<timestamp>.html`; browser remote control
downloads the same document. Filenames include subsecond precision. Custom output
paths are no longer accepted: `/export report.html` shows the new command usage.

Pretty exports initially select the active agent. Use the tab bar to switch
agents; Left/Right and Home/End work while a tab has keyboard focus. Printing
includes all agents. Without JavaScript, all agent sections remain readable.
Images in Markdown appear as links, so opening an export does not fetch them.

Pretty exports use the saved request journal, so recorded work survives context
compaction, `/new`, and session restores. Older sessions without a journal may
have no structured prompt history; use `/export raw` to read their available
transcript instead. Times use the exporting machine's local timezone and show
the UTC offset.
