package session

import (
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// lockSnapshotFile acquires an exclusive lock on f, waiting at most
// snapshotLockTimeout for another qcode process to release it before
// returning snapshotLockBusyError. It polls with LOCKFILE_FAIL_IMMEDIATELY
// so the wait stays bounded instead of blocking in LockFileEx indefinitely
// with no UI feedback. Closing f releases the lock; the lock file stays on
// disk so all writers keep locking the same file.
func lockSnapshotFile(f *os.File) error {
	deadline := time.Now().Add(snapshotLockTimeout)
	for {
		err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &windows.Overlapped{})
		if err == nil {
			return nil
		}
		if errno, ok := err.(windows.Errno); ok && errno == windows.ERROR_LOCK_VIOLATION {
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
