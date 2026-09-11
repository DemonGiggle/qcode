# Plan mode

Plan mode lets an agent investigate a task without changing the workspace. It
is persistent for the active agent and is restored with the saved session.

Use `/plan` while the agent is idle to enter it and start a fresh planning
handoff. The agent can inspect files,
search text, view images, load skills, and use explicitly enabled web-reading
tools. Mutation tools, shell execution, directory grants, and sub-agent
orchestration are unavailable and are rejected at the tool boundary.

When the investigation is complete, qcode asks the model to submit a complete
plan containing a summary, ordered implementation steps, and validation checks.
That submitted plan becomes the latest executable plan. Earlier revisions stay
visible in the conversation, but only the latest revision is used for the
handoff.

Commands:

- `/plan` enters Plan mode and shows `PLAN` in the status bar.
- During planning, the agent may open a blocking questionnaire for unresolved
  design decisions. Questions are presented one at a time; choose an option or
  type a custom answer to let the same planning request continue. Ctrl+C
  cancels it.
- After a complete plan is submitted, qcode asks whether to start
  implementation (the same action as `/plan act`) or stay in Plan mode.
- `/plan show` opens the latest submitted plan in a temporary, scrollable view.
  It does not add the plan text to the conversation output.
- `/plan off` leaves Plan mode without executing the plan.
- `/plan act` switches to normal mode and starts implementing the latest
  submitted plan. It is available only when a complete plan exists.

Plan commands are accepted only when the active agent has no running or queued
work. `/new` clears the saved plan and returns the agent to normal mode.
