//go:build !windows

package tools

import (
	"os/exec"
	"syscall"
)

// configureShellCancellation puts each shell command in its own process group.
// Killing that group prevents background children from surviving Ctrl+C or a
// tool timeout after the shell itself has exited.
func configureShellCancellation(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
