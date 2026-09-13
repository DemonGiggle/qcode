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

Remote control defaults to loopback, requires a named Tailscale identity, and
serializes human approvals through the interaction broker. The first browser
adapter treats every tailnet user allowed to reach the URL as a controller; a
viewer role can be added later with Tailscale application capabilities.

### Using it

Run `/remote` in an interactive qcode session. qcode verifies that Tailscale is
connected, starts a web server on a random `127.0.0.1` port, and runs a
foreground `tailscale serve` proxy on a unique path. The command prints the
HTTPS URL and the exact `tailscale serve` command after Serve reports that it
is ready. Open the URL from a browser on a device signed in to the same
Tailscale tailnet; it is not publicly reachable. qcode also prints the node's
Tailscale IP as a DNS check, but HTTPS must use the hostname because the
certificate is issued for that name.

On Linux, the user running qcode must be allowed to manage the local Tailscale
daemon. If `/remote` reports an operator-permission error, run this once as an
administrator:

```sh
sudo tailscale set --operator=$USER
```

qcode intentionally does not invoke `sudo` itself. `/remote off` and qcode exit
stop the foreground Serve proxy and close the local listener.

The web UI requires the `Tailscale-User-Login` header injected by Serve. Direct
network listeners, anonymous clients, tagged devices without a user identity,
and Tailscale Funnel are not supported. Anyone permitted by the tailnet policy
to open the URL is a controller in this first version.

Use `/remote status` to see the URL and connected browser count. Use `/remote
off` to close browsers, stop the loopback server, and interrupt the foreground
Serve process. Exiting qcode performs the same cleanup without resetting other
Serve mappings on the machine.

When `/verbose` is enabled, qcode records `Remote connect: <identity>` and
`Remote disconnect: <identity>` in the active screen history for each browser
event stream connection.

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
