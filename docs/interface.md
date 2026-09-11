# Interface

The terminal UI is a full-viewport, keyboard-driven interface with editable input, per-agent history, full word wrapping, streamed responses, and ANSI-colored Markdown, including aligned GFM tables. Fixed tabs sit at the top and a status bar at the bottom. The normal terminal buffer and scrollback are retained.

The prompt is pinned above the task indicator and status bar from launch. It displays `>` for an idle agent, `(Plan)>` in Plan mode, and `(Queue)>` while the current tab's agent is working. The status bar includes `STEP current/max` for the active agent's model-turn progress and `MODE PLAN` when planning is active. Each tab keeps its own draft and prompt state. Long drafts wrap upward within the input area.

Typing `/` opens up to five matching command suggestions directly above the prompt. Type more characters to filter them or press Tab to complete the first match. Suggestions stay in their own area while output streams, and disappear when the draft no longer matches a command; they are not saved in conversation history.

Input editing follows terminal conventions: Left/Right moves by character, Home/End moves to the line boundaries, Ctrl+Left/Right and Alt+Left/Right move by word, Alt+B/F provide Meta word movement, Ctrl+W deletes the previous word, and Ctrl+A/E move to the beginning/end of the line.

Set `NO_COLOR=1` to disable response styling. UTF-8 terminals use Unicode interface glyphs; other locales fall back to ASCII without disabling color. Set `QCODE_ASCII=1` to force the ASCII-safe display mode.

## Slash commands

The following UI-focused commands are available in interactive mode. Feature-specific commands (`/agent`, `/skill`, `/learn`) are documented in their respective topic pages.

- `/model` fetches the provider's models. Search a large catalog by typing, move through matches with Up/Down, and select with Enter.
- `/new` discards the current conversation context and resets session token totals without restarting qcode or changing the provider, model, or workspace.
- `/resume` opens saved sessions for this workspace. Use Up/Down and Enter to restore, or Ctrl+C to cancel. Each entry shows its latest main-agent conversation preview (up to two lines) and how long ago you left it, newest first. Finish or cancel running agents before switching. Sessions open in another process cannot be selected.
- `/clear` redraws the interactive banner, which lists enabled and disabled tools for the active agent.
- `/verbose` adds detailed timestamped telemetry, including raw tool arguments, without disabling concise activity events.
- `/maxsteps` shows the current per-request model-turn limit; `/maxsteps N` updates it for the active agent.
- `/diff` and `/diff N` expand the latest write/edit diff preview (see below).
- `/tool` enables or disables the web tools independently; see [Web tools](web-tools.md).
- `/plan` enters read-only planning mode; `/plan off` leaves it; `/plan act` implements the latest submitted plan. See [Plan mode](plan-mode.md).

## Saved sessions

Interactive sessions autosave changed state every two seconds and after agent events and commands, with a final save on clean exit or session switch. Each launch starts a separate session; resuming continues the selected session. Empty launches and demo/one-shot runs are not saved. `/new` still resets only the active agent within the current saved session.

Resume restores all agent tabs and models, conversation messages and images, retained styled output (including events, errors, thinking, and diffs), expandable diff data, drafts, reading positions, tool/skill settings, context accounting, and token totals. Existing directory grants are restored when their paths remain valid under current protections; discarded grants produce a notice. Provider credentials come from current configuration and are not saved. The retained history limit remains 5,000 lines per tab; terminal dimensions can change wrapping.

Sessions live outside the workspace: `$XDG_STATE_HOME/qcode/sessions` on Linux (default `~/.local/state/qcode/sessions`), `~/Library/Application Support/qcode/sessions` on macOS, and `%AppData%/qcode/sessions` on Windows. Canonical workspace paths identify session groups, so symlink aliases share sessions and separate worktrees do not. Snapshots contain conversation and tool output and use private file permissions where supported. Sessions are retained without automatic deletion.

After a crash, recovery uses the latest successful checkpoint and marks unfinished agents interrupted. It preserves saved output but never automatically reruns tools or model requests. Unresolved tool results are marked as having an unknown outcome before the next user-directed run. Files on disk and external processes are not rolled back. Session-save errors are shown; a failed save prevents switching away from the current session.

## Activity events

Interactive mode always records concise colored activity events for tool calls and agent coordination, such as `Reading internal/tui/tui.go`, `Writing README.md`, or `Consulting agent-2`. These events are presentation-only and never enter the next model request. A task-level indicator remains visible while the active tab is running and includes its queued prompt count. The input remains editable: another submitted prompt is marked `Queued #N` and runs in FIFO order. Page Up/Page Down, Ctrl+C cancellation of the running prompt, and tab switching remain available.

## Scrolling

Use Page Up and Page Down to scroll through qcode's output history, including while an agent is running. Scrolling back pauses the live view: new output is still recorded, but the page you are reading stays in place. Page Down to the latest content resumes live output.

Each agent tab remembers its reading position across tab switches and terminal resizing. Completion and cancellation do not force a paused view to the bottom. Directory approval requests return to live output so the question is visible before you answer. History is bounded; if the content you were reading is evicted, the next repaint shows the oldest retained content.

## Thinking streams

Provider-delimited model thinking streams in subtle gray, while the final answer retains normal Markdown styling. Ollama uses `message.thinking`; OpenAI-compatible providers may use `reasoning_content` or `reasoning` deltas.

## Diff previews

Interactive `write` and `edit` tool calls display numbered, 10-line Codex-style diff previews with a file summary, change counts, guided body, and colored additions, removals, and hunk headers. Use `/diff` or `/diff N` to expand a preview up to the 200-line safety limit; tool results sent back to the model remain plain text.

## Run completion notice

Successful interactive runs end with a distinct colored `Completed in ...` notice measuring the complete run across every model turn and tool call.
