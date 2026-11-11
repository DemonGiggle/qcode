# Plan mode

Plan mode lets an agent investigate a task without changing the workspace. It
is persistent for the active agent and is restored with the saved session.

Use `/plan` while the agent is idle to enter it and start a fresh planning
handoff. The agent can inspect files,
search text, view images, load skills, and use explicitly enabled web-reading
tools. Mutation tools, shell execution, directory grants, and sub-agent
orchestration are unavailable and are rejected at the tool boundary.

When the investigation is complete, qcode iterates with you until the spec is
clear enough to implement without major guesses, then asks the model to
submit a complete plan containing a summary, ordered implementation steps,
and validation checks. That submitted plan becomes the latest executable
plan. Earlier revisions stay visible in the conversation, but only the latest
revision is used for the handoff. The model should not submit the plan while
material decisions remain unresolved; `open_questions` lists only items you
explicitly deferred or could not answer.

Commands:

- `/plan` enters Plan mode and shows `PLAN` in the status bar.
- During planning, the agent opens blocking questionnaires for unresolved
  design decisions: goals and scope, what is out of scope, affected files or
  systems, user-visible behavior or API changes, data model and compatibility,
  edge cases, validation, and rollout. Questions are presented with concrete
  choices where alternatives exist, or accept a custom answer; each round lets
  the same planning request continue. Expect follow-up rounds when an answer
  leaves an important ambiguity. The agent keeps asking across turns until
  scope, behavior, edge cases, and validation are clear, so answer each round
  and request revisions if the plan still feels vague. Ctrl+C cancels the
  questionnaire.
- A custom reply can ask for clarification about the question or its choices.
  The agent answers it in text first so you can read it before deciding, then
  asks again for any unresolved decision, continuing the same planning request
  until a complete plan is submitted. The normal model step limit still applies.
- After a complete plan is submitted, qcode asks whether to start
  implementation (the same action as `/plan act`) or stay in Plan mode.
- `/plan show` opens the latest submitted plan in a temporary, scrollable view.
  It does not add the plan text to the conversation output.
- `/plan off` leaves Plan mode without executing the plan.
- `/plan act` switches to normal mode and starts implementing the latest
  submitted plan. It is available only when a complete plan exists.

Plan commands are accepted only when the active agent has no running or queued
work. `/new` clears the saved plan and returns the agent to normal mode.

`/plan` is a one-shot handoff; the execution checklist (`/todo`) is the
execution-time counterpart used while implementing. The model maintains one todo
list per agent tab with its `todo_add`, `todo_update`, and `todo_done` tools,
and `/todo show` opens the latest checklist in a scrollable view. Todo state
is separate from plan state: `/plan act` keeps the todo list, `/new` clears
both, and sub-agents start with their own empty lists. Incomplete todo lists
persist across requests; finished lists are archived when observed and cleared
when the next request starts.
