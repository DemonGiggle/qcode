package tools

// trackedShellCommand keeps jobs started with '&' attached to the shell tool.
// Without this wrapper a non-interactive shell exits immediately after
// starting a background job, which leaves a development server running after
// the tool (and its cancellation scope) has finished.
func trackedShellCommand(command string) string {
	return "trap '_qcode_shell_status=$?; trap - 0; wait; exit \"$_qcode_shell_status\"' 0\n" + command + "\n"
}
