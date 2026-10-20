---
name: bounded-verification
description: Set stopping conditions for code investigations and fixes; avoid repeated checks once relevant evidence and required validation are complete.
---

# Bounded verification

## When to use

Use for code investigations, explanations, and focused changes when repeated
reading or testing could continue after the user's question is answered.
Apply the user's requested scope and any required repository checks. For an
explicit audit or exhaustive review, define completion against that wider scope.

## Define completion

Before investigating, choose a concrete completion condition appropriate to
the request. Keep it as a short working note; no extra file is needed.

- **Diagnosis or explanation:** Identify the cause or behavior, cite the
  supporting source, and resolve contradictions that could change the answer.
  When a further check is warranted, choose one targeted check that could
  disprove the explanation.
- **Code change:** Implement the requested behavior, complete relevant tests
  and required checks, and review the final diff for unintended changes.
  A simple change may need only direct inspection or an existing check.

Extend this condition only for new evidence that affects correctness or an
explicit change in the user's request.

## Choose checks that can change the answer

Before each additional verification call, identify the unresolved fact it
should establish and how its result could change the conclusion or next action.
If no such fact remains, provide the final answer.

Reuse facts already established by tool results. Re-read a source or rerun a
check when it changed, the earlier result was incomplete or failed, or new
conflicting evidence requires a different observation. After a failure, address
its cause before retrying. Changing a command, search pattern, or line range
solely to reconfirm the same fact still counts as repeated verification.

Keep brief working notes of the conclusion, supporting evidence, and remaining
question. When conversation context is shortened, preserve these facts instead
of restarting the investigation.

## Stop or recover

- Once the completion condition is met, answer immediately. Include the
  conclusion, its evidence or validation, and material uncertainty.
- After two consecutive verification calls add no evidence relevant to an
  unresolved question, stop checking. Answer from existing evidence if it is
  sufficient. Otherwise, state the missing fact and make one targeted attempt
  using a different approach to obtain it. If it remains unresolved, report the
  limitation or ask one focused question when user input can resolve it.
- Reopen a settled conclusion only for a relevant change or new conflicting
  evidence. After validation passes, run further checks only for a new change,
  an unresolved concern, or an outstanding required check.
- Work within the current turn budget. If it is nearly exhausted, report the
  supported result and outstanding work. Do not raise limits, restart the task,
  or claim completion to escape the budget.

For example, after the implementation and related test establish a mismatch,
explain it with source locations. Inspect a build file only if an unresolved
build setting could change that explanation.

## Boundaries

This skill supplies stopping guidance; it does not enforce tool, token, or
time limits. It grants no additional tool permissions and does not authorize
changes beyond the user's request. Report a required check that could not run
and its impact; never treat an unavailable or failed check as a pass.
