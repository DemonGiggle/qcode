# Trust model

qcode runs model-triggered tools against your workspace and account. This page describes the protections that apply by default and what `--sandbox` changes on Linux.

## Prompt-injection boundary

The current user's request and qcode's built-in instructions set the task.
Web pages and search snippets, local files and search results, shell output and
errors, loaded skills, agent handoffs, saved conversations, and imported context
are reference data. They can supply facts, code, and quotations, but text inside
them cannot authorize tool calls or override the task. Links and encoded text
inside those sources have the same status. A page asking the agent to read a
credential, run a command, change files, disable safeguards, or contact an
endpoint is still page content.

Each model-visible tool result carries its tool name and an explicit
`untrusted` label in a JSON envelope. JSON escaping keeps source text inside
the content field even when it contains delimiters or fake role headers. Saved
sessions keep provenance metadata. Restored user and assistant turns retain
their conversation text, with system guidance identifying them as reference
data. Only imported turns get restored provenance; new user requests and
assistant replies keep their normal roles. Recognized legacy restored-history
JSON envelopes are decoded on resume, including nested copies, while tool
result envelopes and other JSON remain intact. The saved system prompt is replaced by
the current runtime prompt on restore. Providers that expose only plain tool
output receive the envelope as text; qcode does not assume native provider
support for trust labels.

When qcode sees an actionable instruction pattern in untrusted content, it
prints a prominent, calibrated warning without repeating that content. A
quoted or fenced example can still match if it contains a complete action
request; the warning does not assert that the document's author had malicious
intent. Later calls that can access data, mutate state, or send data require the interactive user's
approval for the exact call. In one-shot or other non-interactive use, those
calls are denied. An isolated quoted phrase does not trigger the warning. Tool
output remains readable so factual extraction and ordinary coding tasks
continue.

The detector is deliberately conservative and can miss novel or heavily
obfuscated attacks. The content boundary and model instructions apply to all
untrusted results regardless of detection, but neither can guarantee that a
model will always distinguish an instruction from data. The approval gate is
enforced after a detected attempt; other actions that appear to be prompted by
untrusted content depend on the model asking the user. Use sandbox mode for
filesystem and network confinement when stronger isolation is needed.

Interactive [Plan mode](plan-mode.md) adds a stricter per-agent policy: only
inspection tools and plan submission are advertised and every mutation-capable
tool call is rejected before execution. Plan mode does not run shell commands,
request additional directories, or delegate work to other agents.

## Without the sandbox

Without `--sandbox`, `read`, `list`, `search`, and `view_image` can inspect paths outside the agent's current directory, subject to operating-system permissions. Absolute paths and parent-relative paths such as `../notes.txt` are accepted; relative paths resolve against the agent's current directory (initially `--cwd`). The `write` and `edit` tools still reject paths that lexically leave that directory, but symbolic links and the `shell` tool can still reach anything allowed by the operating-system account. The `change_directory` tool can select an existing directory outside the launch workspace; subsequent write and edit confinement follows that selected directory.

## With the sandbox (`--sandbox`, Linux)

qcode first checks that [bubblewrap](https://github.com/containers/bubblewrap) and the kernel features it needs are usable. qcode itself remains outside the sandbox so it can load its configuration and contact the selected provider; API keys and other environment secrets are removed from shell-tool environments, and every existing qcode configuration file inspected at startup is hidden from model tools.

Each shell call receives a fresh namespace with no network by default, a hidden real home and runtime directory, a private temporary directory, a read-only root filesystem, and read/write mounts for approved directories. Fresh sandbox sessions disable both web tools; outside the sandbox both start enabled. Enabling either web tool allows networking for native web tools and sandboxed shell commands; disabling both or using `/new` restores isolation. Resume restores saved tool selections, and helpers inherit them, so an enabled web tool also allows networking in those sandbox sessions. Built-in file tools use kernel-assisted path confinement to prevent symlink escapes. Temporary files created under the sandbox's private `/tmp` do not persist. The sandbox is disabled by default.

## Requesting more directories

The model can call `request_directory_access` when work requires another directory. qcode asks the interactive user to approve an editable directory path, grants it read/write for the current session, and clears added grants on `/new`. The filesystem root cannot be granted. Non-interactive directory requests are denied.

`change_directory` and `create_agent.cwd` use the same authorization rules.
Switching to an already approved directory needs no prompt; an unapproved
destination requires user approval before the switch or creation proceeds.
`/resume` restores saved grants before validating directories and falls back to
the launch workspace with a warning when access is unavailable. `/new` also
resets the active directory to launch.

## Persistent command directories

For tools you always want available, set `sandbox_command_paths` (or repeatable
`--sandbox-command-path`, which overrides it):

```toml
sandbox = true
sandbox_command_paths = ["~/.local/bin", "~/go/bin"]
```

Each directory is an explicit persistent trusted-code allowlist entry: it is
mounted read-only at its original path, only its empty ancestors are created
under the hidden home, and it is prepended to the sandbox `PATH` ahead of the
safe system path. Siblings and the rest of home stay hidden. Unlike
`request_directory_access`, these mounts survive `/new`, are never read/write,
and never prompt. Symlinks resolve to their target directories before validation
and deduplication. Invalid or missing directories, the filesystem root,
duplicates, and protected-path overlaps are skipped with a warning.

## Fallback behavior

If bubblewrap is missing or unusable, or if the selected workspace contains the user's home, interactive mode offers to continue without the sandbox or leave; one-shot mode prints a warning and continues unsandboxed. A shell command's complete requested arguments are normally shown in the timestamped start event before execution. After a prompt-injection warning, activity and trace arguments are redacted; the approval prompt shows the exact proposed call for review.
