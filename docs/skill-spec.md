# qcode skill specification

This document describes how to create a skill that qcode can discover and
make available to an agent. A qcode skill is a directory containing one
focused `SKILL.md` document with reusable instructions for a recurring kind of
work.

This is a qcode-specific format. qcode does not execute a skill as a plugin,
interpret a general skill manifest, or merge several instruction files
together.

## Required layout

Create one directory per skill. The directory name is the skill name and must
contain a file named exactly `SKILL.md`:

```text
<skill-root>/
└── <skill-name>/
    └── SKILL.md
```

For the built-in locations and their runtime behavior, see
[Skills](skills.md). The usual workspace locations are:

| Location | Scope |
| --- | --- |
| `~/.qcode/skills/` | User account |
| `<workspace>/.agents/skills/` | Workspace |
| `<workspace>/.qcode/skills/` | Workspace override |

Additional roots can be configured with `skills.paths` in `config.toml`.
Relative configured paths are resolved from the selected workspace and `~`
expands to the current user's home directory.

## Naming and file requirements

The following rules are enforced during discovery:

- The skill directory name must contain only lowercase `a-z`, `0-9`, `-`, or
  `_`.
- The name must be between 1 and 64 characters.
- `SKILL.md` must be a regular file.
- The resolved `SKILL.md` path must remain inside its containing skill root;
  symlink escapes are ignored.
- The complete file must be no larger than 64 KiB (65,536 bytes) when qcode
  loads it.

Names come from directory names, not from document metadata. For example:

```text
.qcode/skills/review-go/SKILL.md
```

defines the skill name `review-go`. Invalid names and directories without a
usable `SKILL.md` are not listed. An oversized file may still appear in the
selector, but loading it fails with the 64 KiB limit error.

Keep the document in readable UTF-8 Markdown. qcode passes the complete file
content through the `skill` tool; it does not transform the body into another
format.

## Description and summary parsing

The description is the short summary shown in the `/skill` selector and in
the model's workspace-skill list. qcode reads only the first 8 KiB while
finding this summary, so put it at the top of the document.

The preferred form is a simple, single-line description inside a front matter
block:

```markdown
---
description: Review Go changes for correctness, maintainability, and test coverage.
---

# Review Go changes
```

qcode recognizes only a `description:` line in a block that starts with
`---` and has a later closing `---`. It does not use other front matter
fields, including `name`, to configure the skill. The front matter remains
part of the document returned by the `skill` tool.

If qcode cannot find a description, it uses the first non-empty line after
the front matter, or the first non-empty line in the file when there is no
front matter. If there is no usable text, it displays `Workspace instructions`.
The displayed summary is whitespace-normalized to one line and truncated to
160 bytes when necessary.

Keep the summary specific enough for an agent to decide whether the skill
applies. For example, prefer `Review Go changes for correctness and test
coverage` over `Help with code`.

## Writing the instruction body

The body is free-form Markdown, but it should be focused and self-contained.
Write instructions that change qcode's decisions for the target workflow:

- state when the skill applies and when it does not;
- describe the desired outcome and important workflow decisions;
- identify required inputs, outputs, formats, and repository conventions;
- document safety, privacy, approval, or side-effect boundaries specific to
  the workflow;
- describe the checks that establish a successful result;
- say when qcode should stop and ask the user for a missing decision.

Avoid generic coding advice, exhaustive tutorials, speculative edge cases, and
rules that expand the user's request. Do not assume that merely selecting a
skill authorizes a write, shell command, network request, or other side
effect; qcode's normal tool and user-approval rules still apply.

A practical structure is:

```markdown
# Skill title

## When to use

Requests covered by this skill and important exclusions.

## Workflow

The decisions, tools, and artifacts that matter for this workflow.

## Constraints

Workflow-specific safety, compatibility, and stopping conditions.

## Validation

Checks to run and what success looks like.
```

This structure is a recommendation, not a parser requirement. qcode does not
require particular headings or keywords in the body.

Keep all required instructions in `SKILL.md`. qcode discovers and loads that
file only; additional files placed beside it are not automatically loaded or
executed as part of the skill.

## Discovery, selection, and loading

In interactive mode, `/skill` refreshes the catalog and displays each
discovered skill's name and summary. The user can filter the list, toggle
skills, and press Enter to apply the selection. The selection is associated
with the active agent tab.

Selecting a skill does not immediately add its full document to the model
context. qcode adds only the selected name and summary to the agent's system
prompt. When the selected skill applies, the agent is expected to call the
`skill` tool with the exact directory name. qcode then returns the complete
`SKILL.md` content.

The `skill` tool refuses to load a name that was not selected for the current
agent. Removing a skill from the selection therefore prevents its instructions
from being loaded, even if the skill is still present on disk.

One-shot and piped invocations do not provide the interactive `/skill`
selection step, so a skill is not enabled automatically in those modes.

## Precedence and overrides

qcode searches locations from lower to higher precedence. If multiple roots
contain the same skill name, the later root replaces the earlier one in full:

1. `~/.qcode/skills/`
2. `<workspace>/.agents/skills/`
3. `<workspace>/.qcode/skills/`
4. configured `skills.paths`, in the order listed

Use `<workspace>/.qcode/skills/<name>/SKILL.md` when the workspace must
override a user-level or `.agents` skill. qcode does not merge the documents
from two versions.

Missing roots are harmless and remain visible in the `/skill` location hint.
New or changed skills are picked up when qcode refreshes discovery through
`/skill`.

## Validation checklist

Before relying on a new or changed skill:

1. Confirm the path is `<skill-root>/<valid-name>/SKILL.md`.
2. Confirm the document is no larger than 64 KiB and the summary is within
   the first 8 KiB.
3. Run qcode interactively and invoke `/skill`.
4. Confirm the intended name and summary appear, then select the skill.
5. Run a representative request and verify that the agent loads the skill by
   its exact name when the workflow applies.
6. Check that the result follows the documented constraints and validation
   steps.

## Complete example

Directory:

```text
.qcode/skills/review-go/
└── SKILL.md
```

`SKILL.md`:

```markdown
---
description: Review Go changes for correctness, idiomatic design, concurrency risks, and test coverage.
---

# Review Go changes

## When to use

Use this skill for a Go code review, including a request to compare a branch,
review a patch, or assess whether a change is ready to merge.

## Workflow

- Establish the comparison base and inspect the complete diff.
- Trace changed behavior to its callers and error paths.
- Check tests for the changed behavior and identify meaningful gaps.
- Run focused Go tests, then broader tests when practical.
- Report findings by severity with file and line references.

## Constraints

- Preserve unrelated working-tree changes.
- Do not modify code unless the user asks for a fix.
- Treat environment-dependent test failures separately from code failures.

## Validation

Every finding should be supported by the diff, surrounding code, or a
reproducible test result.
```
