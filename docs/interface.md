# Interface

The terminal UI is a full-viewport, keyboard-driven interface with editable input, per-agent history, full word wrapping, streamed responses, and ANSI-colored Markdown, including aligned GFM tables. Fixed tabs sit at the top and a status bar at the bottom. The normal terminal buffer and scrollback are retained.

Set `NO_COLOR=1` to disable response styling. UTF-8 terminals use Unicode interface glyphs; other locales fall back to ASCII without disabling color. Set `QCODE_ASCII=1` to force the ASCII-safe display mode.

## Slash commands

The following UI-focused commands are available in interactive mode. Feature-specific commands (`/agent`, `/skill`, `/learn`) are documented in their respective topic pages.

- `/model` fetches the provider's models. Search a large catalog by typing, move through matches with Up/Down, and select with Enter.
- `/new` discards the current conversation context and starts a fresh session without restarting qcode or changing the provider, model, or workspace.
- `/clear` redraws the interactive banner, which lists enabled and disabled tools for the active agent.
- `/verbose` adds detailed timestamped telemetry, including raw tool arguments, without disabling concise activity events.
- `/diff` and `/diff N` expand the latest write/edit diff preview (see below).
- `/tool` enables or disables the web tools independently; see [Web tools](web-tools.md).

## Activity events

Interactive mode always records concise colored activity events for tool calls and agent coordination, such as `Reading internal/tui/tui.go`, `Writing README.md`, or `Consulting agent-2`. These events are presentation-only and never enter the next model request. A task-level indicator remains visible while the active tab is running; Page Up/Page Down, Ctrl+C cancellation, and tab switching remain available until its prompt returns.

## Scrolling

Use Page Up and Page Down to scroll through qcode's output history, including while an agent is running. Scrolling back pauses the live view: new output is still recorded, but the page you are reading stays in place. Page Down to the latest content resumes live output.

Each agent tab remembers its reading position across tab switches and terminal resizing. Completion and cancellation do not force a paused view to the bottom. Directory approval requests return to live output so the question is visible before you answer. History is bounded; if the content you were reading is evicted, the next repaint shows the oldest retained content.

## Thinking streams

Provider-delimited model thinking streams in subtle gray, while the final answer retains normal Markdown styling. Ollama uses `message.thinking`; OpenAI-compatible providers may use `reasoning_content` or `reasoning` deltas.

## Diff previews

Interactive `write` and `edit` tool calls display numbered, 10-line Codex-style diff previews with a file summary, change counts, guided body, and colored additions, removals, and hunk headers. Use `/diff` or `/diff N` to expand a preview up to the 200-line safety limit; tool results sent back to the model remain plain text.

## Run completion notice

Successful interactive runs end with a distinct colored `Completed in ...` notice measuring the complete run across every model turn and tool call.
