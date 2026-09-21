# Trust model

qcode runs model-triggered tools against your workspace and account. This page describes the protections that apply by default and what `--sandbox` changes on Linux.

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

If bubblewrap is missing or unusable, or if the selected workspace contains the user's home, interactive mode offers to continue without the sandbox or leave; one-shot mode prints a warning and continues unsandboxed. A shell command's complete requested arguments are shown in the timestamped start event before execution.
