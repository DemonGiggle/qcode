//go:build !windows

package session

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// lockSnapshotFile acquires an exclusive flock on f, waiting at most
// snapshotLockTimeout for a holder in another process (or another open file
// description in this process) to release it. It polls with LOCK_NB so the
// wait stays bounded and interruptible; blocking LOCK_EX would sleep in the
// kernel indefinitely with no UI feedback. EINTR retries keep the deadline.
//
// The caller must hold the returned lock by keeping f open: closing f
// releases the lock, and the lock file itself stays on disk so all writers
// keep locking the same file.
func lockSnapshotFile(f *os.File) error {
	deadline := time.Now().Add(snapshotLockTimeout)
	for {
		err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return nil
		}
		if errors.Is(err, unix.EINTR) {
			if !time.Now().Before(deadline) {
				return snapshotLockBusyError()
			}
			continue
		}
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			now := time.Now()
			if !now.Before(deadline) {
				return snapshotLockBusyError()
			}
			wait := snapshotLockPollInterval
			if remaining := deadline.Sub(now); remaining < wait {
				wait = remaining
			}
			if wait > 0 {
				time.Sleep(wait)
			}
			continue
		}
		return err
	}
}
