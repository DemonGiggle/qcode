# Remote control architecture

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

## Phase 1: control-plane foundation

- Interactive, restored, demo, and one-shot runtimes are created as a
  `control.Host`.
- Existing TUI behavior is preserved through its current presentation event
  channel.
- New adapters use `Host.Snapshot` and `Host.Subscribe`. Every subscriber has
  an independent cursor, and events carry monotonically increasing sequence
  numbers for reconnect detection and bounded replay.
- Human-input requests have a shared `InteractionBroker`. The first controller
  to resolve a request wins; later resolutions fail deterministically.
- No network listener or `/remote` command is enabled in this phase.

The first event types cover agent state, consultation notifications, and the
interaction lifecycle. Streaming response, thinking, tool activity, usage, and
diff events can be added to the same envelope as their current writer-based
presentation paths are separated from the TUI.

## Phase 2: browser remote adapter

The initial network adapter uses JSON for commands and snapshots, plus SSE for
change notifications. `/remote` dynamically starts or stops that adapter on
the existing Host; it does not put the TUI into a separate server mode.

Tailscale support should initially expose the loopback HTTP listener through
`tailscale serve`. An embedded `tsnet` listener can remain a later option when
qcode needs its own tailnet node or programmatic Tailscale identity.

Remote control has two terminal-selected modes and serializes human approvals
through the interaction broker. Tailscale binds a browser session to a named
tailnet identity. Pure Web is a trusted-LAN HTTP option whose terminal-issued
login link is its sole authorization gate. A viewer role can be added later
with Tailscale application capabilities.

### Using it

Run `/remote` in an interactive qcode session. When remote control is off, a
terminal selector offers **Pure Web** first and **Tailscale** second. Pure Web
binds an HTTP server to a random port on all interfaces and puts the host's
primary LAN IPv4 address in the terminal-only QR link. It is unencrypted and
must be used only on a trusted LAN. Tailscale starts a loopback server and its
foreground `tailscale serve` proxy on a unique HTTPS path; browsers must belong
to the same tailnet.

If more than one active LAN IPv4 interface is available, qcode asks which one
to expose and shows each interface name, address, and CIDR subnet. With only
one eligible interface, it uses that address directly.

The selector also includes **Pure Web (No auth, danger!)**. It uses the same
LAN HTTP listener but encodes the bare service URL in the QR code: no login
link, token, or browser session is required. Anyone who knows that URL can read
and control the qcode session until it is closed. Use it only for deliberately
open, short-lived trusted-network sessions.

When remote control is already on, `/remote` creates a fresh one-time QR login
link for another browser and shows the mode, address, and current number of
active browser sessions. The screen selects **Keep connection open** by default
and exposes **Close Connection** as a secondary action. Closing requires an
explicit confirmation and revokes every browser session.

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

The browser mirrors the terminal task indicator: while the active tab's agent is
running, a dim `Waiting (⠋) · N queued` line with the same animated spinner
frames appears above the status bar, and a Cancel button replaces the TUI's
`Ctrl+C` hint so a queued or running prompt can be stopped without the keyboard.

Each `/remote` invocation issues a new single-use login link, valid for three
minutes. Issuing another link invalidates the previous unredeemed link and
leaves existing browser sessions connected.

The login link and QR appear in a local panel, outside the shared transcript,
saved sessions, exports, and logs. The panel is dismissed by the next command.
If it remains open, it displays an expiry notice after three minutes. Resize
the terminal if the QR does not fit; qcode never crops or wraps a QR code.
The clickable heading targets the complete link even when the visible URL wraps.

The link carries a random token in its URL fragment. The browser removes the
fragment and exchanges the token for a random session key via `POST api/v1/login`.
The exchange consumes the token atomically; Tailscale mode additionally requires
its named identity. Fetching the HTML shell alone does not consume a link. Used
and expired links show a message directing the user to run `/remote` again.

The browser stores its key in `sessionStorage`, scoped to the remote session's
path. Reloading the same tab resumes access. A fresh browser session needs a
new login link. Session storage must be available; qcode does not fall back to
cookies or persistent browser storage.

Every API request, including transcript reads and the live event stream, sends
`Authorization: Bearer <session_key>`. qcode checks the key against hashes held
in server memory and, in Tailscale mode, requires the same named identity that
redeemed the link. Invalid sessions receive HTTP 401 and the browser disables
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
session unchanged. Saved-session choices exclude the current session and
snapshots that are busy or unreadable.

### Exporting from the browser

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
