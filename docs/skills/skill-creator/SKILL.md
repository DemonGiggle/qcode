---
name: skill-creator
description: Create or update qcode-compatible skills from a user's workflow brief, including focused instructions, constraints, and validation.
---

# Skill Creator

## When to use

Use this skill when a user asks to create, generate, author, scaffold, or
revise a reusable skill for qcode. The result should be a focused
`<skill-name>/SKILL.md` that another qcode agent can load and follow.

Do not use this skill merely to perform the workflow that a skill might
describe. If the user asks for both a skill and the underlying task, create the
skill first and perform the underlying task only when that work is explicitly
requested.

## Workflow

### 1. Understand the workflow brief

Identify the decisions that materially affect the skill:

- the recurring goal and intended users;
- requests the skill covers and requests it should not cover;
- required inputs, expected outputs, and output formats;
- tools, integrations, files, or repository conventions it may use;
- side effects, approval boundaries, privacy requirements, and failure modes;
- checks that establish a successful result.

Ask a concise clarification only when a missing decision would materially
change the skill's scope or output. Otherwise make a reasonable assumption and
state it in the handoff.

### 2. Choose the skill shape and location

Respect the user's requested name and path. When they do not provide a name,
choose a short, action-oriented name using only lowercase `a-z`, `0-9`, `-`,
or `_`, with 1–64 characters. The required output path is:

```text
<skill-root>/<skill-name>/SKILL.md
```

If the target already exists, inspect it before editing and preserve useful
behavior unless the user asks for a replacement. Do not overwrite unrelated
working-tree changes.

qcode loads only `SKILL.md` as the skill document. Keep every instruction
needed for the workflow in that file; do not make the skill depend on an
adjacent README, reference file, manifest, or placeholder that qcode will not
automatically load. Add supporting files only when the skill explicitly needs
them and the instructions explain how they are used.

### 3. Write the skill

Begin the file with a qcode-compatible front matter block containing a single,
specific, one-line `description:`:

```markdown
---
description: Review Go changes for correctness and test coverage.
---
```

Keep the description near the top and concise enough to remain useful in the
skill selector. Do not rely on a `name:` field: qcode derives the skill name
from the directory.

Write instructions that change an agent's decisions for this workflow. Prefer
the following structure when it fits:

```markdown
# Skill title

## When to use

Requests covered by this skill and important exclusions.

## Workflow

The decisions, tools, and artifacts that matter.

## Constraints

Workflow-specific safety, compatibility, and stopping conditions.

## Validation

Checks to run and what success looks like.
```

Make the instructions self-contained and concrete. Preserve the user's scope,
distinguish required behavior from optional recommendations, and encode
reusable decision criteria instead of tailoring the skill to one example. Say
when to stop and ask the user for a missing decision. Selecting a skill does
not authorize writes, shell commands, network access, external messages, or
other side effects; describe those boundaries when they matter to the
workflow.

### 4. Validate the result

Before handing it off, check all of the following:

1. The directory name is valid and the file is exactly `SKILL.md`.
2. `SKILL.md` is a regular UTF-8 Markdown file no larger than 65,536 bytes.
3. The front matter starts at the top, has a later closing `---`, and contains
   a usable single-line `description:` within the first 8 KiB.
4. The body states when the skill applies, the workflow's important
   constraints, and observable validation criteria.
5. The instructions do not silently expand permissions or depend on content
   qcode will not load.

When the skill is placed in a directory qcode scans, refresh discovery with
`/skill`, select the exact skill name, and try one representative request when
practical. `docs/skills/` is a repository release collection, not one of
qcode's default runtime roots; explain that the user must configure it through
`skills.paths` or copy the skill to a runtime skill root before expecting
automatic discovery.

## Handoff

Report the created or updated path, the skill name and summary, the validation
performed, and any assumptions or runtime-installation step the user still
needs to make.
