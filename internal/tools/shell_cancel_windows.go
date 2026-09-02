//go:build windows

package tools

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
)

// Windows has no POSIX process groups, so taskkill provides the equivalent
// recursive termination for cmd.exe and every process it has started.
func configureShellCancellation(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		treeErr := exec.Command("taskkill.exe", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
		if treeErr == nil {
			return nil
		}
		err := cmd.Process.Kill()
		if errors.Is(err, os.ErrProcessDone) {
			return os.ErrProcessDone
		}
		return err
	}
}
