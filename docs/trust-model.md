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
sessions keep provenance metadata, and older restored turns are wrapped as
reference data when sent to the model. The saved system prompt is replaced by
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

Without `--sandbox`, file tools reject paths that lexically leave the selected workspace (`--cwd`), but symbolic links and the `shell` tool can still reach anything allowed by the operating-system account.

## With the sandbox (`--sandbox`, Linux)

qcode first checks that [bubblewrap](https://github.com/containers/bubblewrap) and the kernel features it needs are usable. qcode itself remains outside the sandbox so it can load its configuration and contact the selected provider; API keys and other environment secrets are removed from shell-tool environments, and every existing qcode configuration file inspected at startup is hidden from model tools.

Each shell call receives a fresh namespace with no network by default, a hidden real home and runtime directory, a private temporary directory, a read-only root filesystem, and read/write mounts for approved directories. Enabling either web tool grants network access to the current sandbox session; disabling both or starting a new session restores isolation. Built-in file tools use kernel-assisted path confinement to prevent symlink escapes. Temporary files created under the sandbox's private `/tmp` do not persist. The sandbox is disabled by default.

## Requesting more directories

The model can call `request_directory_access` when work requires another directory. qcode asks the interactive user to approve an editable directory path, grants it read/write for the current session, and clears added grants on `/new`. The filesystem root cannot be granted. Non-interactive directory requests are denied.

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
and never prompt. Invalid, missing, root, duplicate, symlinked, and protected
overlaps are skipped with a warning.

## Fallback behavior

If bubblewrap is missing or unusable, or if the selected workspace contains the user's home, interactive mode offers to continue without the sandbox or leave; one-shot mode prints a warning and continues unsandboxed. A shell command's complete requested arguments are normally shown in the timestamped start event before execution. After a prompt-injection warning, activity and trace arguments are redacted; the approval prompt shows the exact proposed call for review.
