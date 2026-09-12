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

## Phase 2: remote adapters

The initial network adapter should use REST for commands and snapshots, plus
SSE for the event stream. `/remote` dynamically starts or stops that adapter on
the existing Host; it does not put the TUI into a separate server mode.

Tailscale support should initially expose the loopback HTTP listener through
`tailscale serve`. An embedded `tsnet` listener can remain a later option when
qcode needs its own tailnet node or programmatic Tailscale identity.

Remote control must default to loopback, require authentication for non-loopback
listeners, distinguish viewers from controllers, and serialize human approvals
through the interaction broker.
