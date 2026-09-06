# Trust model

qcode runs model-triggered tools against your workspace and account. This page describes the protections that apply by default and what `--sandbox` changes on Linux.

## Without the sandbox

Without `--sandbox`, file tools reject paths that lexically leave the selected workspace (`--cwd`), but symbolic links and the `shell` tool can still reach anything allowed by the operating-system account.

## With the sandbox (`--sandbox`, Linux)

qcode first checks that [bubblewrap](https://github.com/containers/bubblewrap) and the kernel features it needs are usable. qcode itself remains outside the sandbox so it can load its configuration and contact the selected provider; API keys and other environment secrets are removed from shell-tool environments, and the loaded qcode config file is hidden from model tools.

Each shell call receives a fresh namespace with no network, a hidden real home and runtime directory, a private temporary directory, a read-only root filesystem, and read/write mounts for approved directories. Built-in file tools use kernel-assisted path confinement to prevent symlink escapes. Temporary files created under the sandbox's private `/tmp` do not persist. The sandbox is disabled by default.

## Requesting more directories

The model can call `request_directory_access` when work requires another directory. qcode asks the interactive user to approve an editable directory path, grants it read/write for the current session, and clears added grants on `/new`. The filesystem root cannot be granted. Non-interactive directory requests are denied.

## Fallback behavior

If bubblewrap is missing or unusable, or if the selected workspace contains the user's home, interactive mode offers to continue without the sandbox or leave; one-shot mode prints a warning and continues unsandboxed. A shell command's complete requested arguments are shown in the timestamped start event before execution.
