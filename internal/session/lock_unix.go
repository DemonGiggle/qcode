//go:build !windows

package session

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func lockSnapshotFile(f *os.File) error {
	for {
		err := unix.Flock(int(f.Fd()), unix.LOCK_EX)
		if !errors.Is(err, unix.EINTR) {
			return err
		}
	}
}
