# Browser remote control

Run `/remote` in the terminal to control the same live agents, prompts, queues,
and approvals from a phone or browser. Closing a browser view leaves the host
session running; use the terminal's **Close Connection** action or exit qcode
to stop remote access.

## Shared runtime

qcode has one in-process control plane shared by every user interface:

```text
TUI ───────────────┐
HTTP / SSE ────────┼──> control.Host ──> AgentManager ──> agents / tools / LLMs
Tailscale + HTTP ──┘          │
                              ├── replayable event bus
                              └── interaction broker
```

`control.Host` is the owner-facing application boundary. The terminal does not
start a second runtime when remote access is enabled, and a remote adapter must
not create its own `AgentManager`. Local and remote commands therefore operate
on the same agents, queues, session state, and workspace locks.

- Interactive, restored, demo, and one-shot runtimes are created as a
  `control.Host`.
- Existing TUI behavior is preserved through its current presentation event
  channel.
- New adapters use `Host.Snapshot` and `Host.Subscribe`. Every subscriber has
  an independent cursor, and events carry monotonically increasing sequence
  numbers for reconnect detection and bounded replay.
- Human-input requests have a shared `InteractionBroker`. The first controller
  to resolve a request wins; later resolutions fail deterministically.

The network adapter uses JSON for commands and snapshots, plus SSE for
change notifications. `/remote` dynamically starts or stops that adapter on
the existing Host; it does not put the TUI into a separate server mode.

Tailscale mode exposes the loopback HTTP listener through a foreground
`tailscale serve` process using the host's existing Tailscale installation.

Remote control has three terminal-selected modes and serializes human approvals
through the interaction broker. Tailscale binds a browser session to a named
tailnet identity. Pure Web is a trusted-LAN HTTP option whose terminal-issued
login link is its authorization gate. Pure Web (No auth, danger!) provides
open control to anyone with its URL.

## Connecting

Run `/remote` in an interactive qcode session. When remote control is off, a
terminal selector offers **Pure Web**, **Tailscale**, and
**Pure Web (No auth, danger!)**, in that order. Pure Web
binds an HTTP server to a random port on all interfaces and puts the host's
primary LAN IPv4 address in the terminal-only QR link. It is unencrypted and
must be used only on a trusted LAN. Tailscale starts a loopback server and its
foreground `tailscale serve` proxy on a unique HTTPS path; browsers must belong
to the same tailnet. It tries HTTPS port 443 first. If another foreground Serve
session already owns that port, it tries 8443 and then ports 10000-10031. The
selected port appears in the browser URL and QR link. Tailnet access rules must
allow connections to that port.

If more than one active LAN IPv4 interface is available, qcode asks which one
to expose and shows each interface name, address, and CIDR subnet. With only
one eligible interface, it uses that address directly.

The selector also includes **Pure Web (No auth, danger!)**. It uses the same
LAN HTTP listener but encodes the bare service URL in the QR code: no login
link, token, or browser session is required. Anyone who knows that URL can read
and control the qcode session until it is closed. Use it only for deliberately
open, short-lived trusted-network sessions.

When authenticated remote control is already on, `/remote` creates a fresh
one-time QR login link for another browser. In No auth mode it shows the bare
service URL again. The screen shows the mode, address, and current number of
active browser sessions, selects **Accept** by default to keep the connection
open, and also offers **Save QR as PNG** and **Close Connection**. Closing
requires an explicit confirmation and revokes every browser session.

On Linux, the user running qcode must be allowed to manage the local Tailscale
daemon. If `/remote` reports an operator-permission error, run this once as an
administrator:

```sh
sudo tailscale set --operator=$USER
```

qcode intentionally does not invoke `sudo` itself. Closing from the `/remote`
screen and exiting qcode stop the foreground Serve proxy and close the listener.

Tailscale mode requires the `Tailscale-User-Login` header injected by Serve.
Direct anonymous clients, tagged devices without a user identity, and Tailscale
Funnel are not supported in that mode. Pure Web allows the login exchange
without a Tailscale identity, but a valid QR link is still required before any
session can read or control qcode.

The web UI is responsive: agent tabs scroll horizontally on narrow screens,
the composer stacks on phone widths, and selectors remain touch-sized and
scrollable in short or landscape viewports.

The latest received prompt for the selected agent is pinned directly beneath the
header, with a `✦` marker (`*` in ASCII mode). It uses the
theme's accent color on a background spanning the full browser width, and is
clamped to three lines while the transcript scrolls independently. Snapshots
include optional `latest_prompt` text in each agent view and `prompt_color`,
`prompt_marker`, and `prompt_background` in the presentation. The browser displays
prompt text literally, follows theme changes, and clears the region when
authorization is lost. The text follows the same redaction policies as other
terminal, remote, and saved-session content.

The browser mirrors the terminal task indicator: while the active tab's agent is
running, a dim line such as `Waiting (⠋) · 12s · Waiting for model response · N queued`
shows the current model operation, its elapsed time, and
the same animated spinner frames above the status bar. The spinner and model
details clear during tool execution; the Cancel button and queued prompt count
remain available while tool activity appears in the transcript. Pending approvals or
questions show `Waiting for input`. A Cancel button replaces the TUI's
`Ctrl+C` hint so a queued or running prompt can be stopped without the keyboard.

In the authenticated modes, each `/remote` invocation issues a new single-use
login link, valid for three minutes. Issuing another link invalidates the
previous unredeemed link and leaves existing browser sessions connected.

The login link and QR appear in a local panel, outside the shared transcript,
saved sessions, exports, and logs. The panel is dismissed by the next command.
If it remains open, it displays an expiry notice after three minutes. If the QR
does not fit, select **Save QR as PNG** on the `/remote` screen. qcode writes a
private PNG to a temporary file and shows its path in the terminal. The file is
kept on disk when the link expires, is replaced, the login panel is dismissed, or
remote control closes. In WSL,
use `wslpath -w <path>` to get a path that Windows can open. qcode never crops
or wraps a QR code. The clickable heading targets the complete link even when
the visible URL wraps.

The link carries a random token in its URL fragment. The browser removes the
fragment and exchanges the token for a random session key via `POST api/v1/login`.
The exchange consumes the token atomically; Tailscale mode additionally requires
its named identity. Fetching the HTML shell alone does not consume a link. Used
and expired links show a message directing the user to run `/remote` again.

The browser stores its key in `sessionStorage`, scoped to the remote session's
path. Reloading the same tab resumes access. A fresh browser session needs a
new login link. Session storage must be available; qcode does not fall back to
cookies or persistent browser storage.

In authenticated modes, every API request, including transcript reads and the
live event stream, sends `Authorization: Bearer <session_key>`. qcode checks
the key against hashes held in server memory and, in Tailscale mode, requires
the same named identity that redeemed the link. Invalid sessions receive HTTP
401 and the browser disables
controls and clears its stored key. Login tokens cannot be used as session keys.

The three-minute limit only applies to login links. Browser sessions last until
the confirmed Close Connection action, qcode exit, or Serve termination. Those
events revoke all keys and close active event streams; keys never survive a
remote-server restart.

When `/verbose` is enabled, qcode records `Remote connect: <identity>` and
`Remote disconnect: <identity>` in the active screen history for each browser
event stream connection. It also records rejected requests, including the
missing-identity case, to distinguish Tailscale authentication failures from
requests that never reach qcode. The printed URL works without a trailing slash
and opens directly without a redirect. The web page sets its base URL to the
session path so relative API requests still reach that session after Tailscale
Serve strips the path prefix. URLs with a trailing slash also work.

Browser commands are injected as complete lines into the normal TUI command
loop, so they use the same agent manager, queues, session state, and command
handlers as local input. Remote `/exit`, `/quit`, and `/remote` mutations are
rejected; browser `/exit` only closes that browser view.

The browser also provides catalog-backed selectors for `/model`, `/tool`,
`/skill`, `/resume`, `/agent`, `/agent new`, `/agent list`, and `/agent switch`.
Selectors can be filtered, and model selection opens a second selector when the
chosen model supports adjustable thinking levels. Skill/tool selectors preserve
the current multi-selection until Apply. Cancel or Escape leaves the qcode
session unchanged. Saved-session choices keep every saved session in order,
including the current session. A separate Current badge identifies it without changing
the preview text. Selecting that session keeps it open.
Concurrent processes can restore the same session; the last successful save
wins. Unreadable sessions remain visible with an explanatory label. The
terminal `/resume` picker additionally provides transcript previews, rename,
pin, and confirmed deletion; the browser selector restores sessions.

## Steering and pending work

Busy-agent prompts use structured submissions addressed to the browser's selected agent and observed task ID. Enter/**Steer** updates that task; **Queue** starts a separate FIFO task later. Tab navigates controls normally. A rejected submission retains the draft and refreshes state. **Remove** cancels pending input; **Cancel active work** cancels the running task. Steering waits for a streaming response or running tool, replaces the previous pending steer, and withdraws unanswered interactions. See [interface behavior](interface.md).

Authenticated controllers can POST `/api/v1/prompts` with `agent_id`, `observed_task_id`, `prompt`, and `intent` (`automatic`, `steer`, or `queue`). The response includes `request_id`, accepted `intent`, `state`, `parent_task_id`, and `queue_position`. POST `/api/v1/prompts/{id}/cancel` with `agent_id` removes only pending input. Conflicts return HTTP 409. Runtime snapshots expose pending inputs and durable steering lifecycle events with sequence cursors.

## Exporting from the browser

`/export` and `/export pretty` download a standalone HTML document with one tab
per agent and completed prompt/response pairs in chronological order. `/export raw`
downloads the full styled transcript. The browser uses the same renderer and
saved history as the terminal, rather than exporting only its visible output.
Pretty export exchanges show the prompt in a card and reveal the response when
the card is selected.
In pretty exports, the **Show detailed tool events** checkbox reveals concise
activity summaries beside each exchange; it is unchecked by default. Filenames
are generated automatically; custom paths are no longer accepted.

Downloads use `GET api/v1/export?mode=pretty` (or `raw`) through the existing
remote access controls. The response is an HTML attachment with caching disabled.
Browser exports do not write files in the host workspace.
