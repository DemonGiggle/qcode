# Skill Plan mode

Skill Plan mode helps turn a rough intention into a focused qcode skill while
keeping creation separate from design approval.

Start the flow with `/skillplan`, then describe the skill you want, or include
the initial idea directly:

```text
/skillplan review database migrations before deployment
```

qcode asks focused questions about when the skill applies, its workflow,
inputs and outputs, constraints, validation, name, and scope. It can ask
follow-up questions when an answer leaves an important decision unresolved.
The mode can inspect workspace context but cannot run shell commands or change
files.

Once the design is coherent, qcode saves a complete `SKILL.md` draft and asks
whether to create it or stay in Skill Plan mode. Staying lets you review the
draft or request refinements; each complete revision replaces the previous
draft.

Commands:

- `/skillplan` enters Skill Plan mode and starts a fresh draft when entering
  from another mode.
- `/skillplan <rough intention>` enters the mode and immediately starts the
  guided conversation.
- `/skillplan show` opens the latest complete draft in a scrollable, quoted
  review view. Use Up/Down or PgUp/PgDn to scroll, `q` or Esc to close, then
  `/skillplan create` to approve and write the draft.
- `/skillplan create` explicitly approves and writes the latest draft.
- `/skillplan off` leaves the mode without creating the draft.

Creation supports qcode's built-in skill roots: `~/.qcode/skills`,
`.agents/skills`, and `.qcode/skills`. The draft must use a valid 1–64
character skill name and fit the 64 KiB `SKILL.md` limit. qcode writes exactly
the reviewed document and never overwrites an existing `SKILL.md`; conflicts
remain in Skill Plan mode so you can choose another name or location.

After creation, run `/skill` to refresh discovery and enable the new skill.
See the [qcode skill specification](skill-spec.md) for the complete format and
loading rules.
