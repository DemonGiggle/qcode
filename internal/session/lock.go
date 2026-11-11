package session

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const snapshotLockName = ".snapshots.lock"

// defaultSnapshotLockTimeout bounds how long snapshot transactions wait for
// another qcode process holding .snapshots.lock before failing with a clear
// error instead of blocking in the kernel indefinitely with no UI feedback.
// Ten seconds comfortably covers a slow save, LoadForResume rewrite, or
// deletion on a loaded machine while still surfacing a genuinely stuck
// holder fast enough that /resume, autosave, and delete look broken-but-
// explained rather than hung.
const defaultSnapshotLockTimeout = 10 * time.Second

// snapshotLockTimeout is the effective acquisition deadline. It is a variable
// (rather than a constant) solely so tests can shorten it via a test hook;
// production code must not change it.
var snapshotLockTimeout = defaultSnapshotLockTimeout

// snapshotLockPollInterval paces non-blocking acquisition retries.
var snapshotLockPollInterval = 20 * time.Millisecond

// snapshotLockBusyError reports a held-too-long lock in wording that is
// self-explanatory at the call sites ("Cannot resume: <err>",
// "Session save failed: <err>").
func snapshotLockBusyError() error {
	return fmt.Errorf("session snapshots busy: another qcode process is saving this workspace (lock held >%s)", snapshotLockTimeout)
}

// lockSnapshots serializes snapshot transactions across stores and processes.
// Closing the file releases the OS lock, including after a process exits. Keep
// the lock file in place so all writers continue locking the same file.
func (s *Store) lockSnapshots() (*os.File, error) {
	f, err := os.OpenFile(filepath.Join(s.dir, snapshotLockName), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("lock session snapshots: %w", err)
	}
	if err := lockSnapshotFile(f); err != nil {
		f.Close()
		return nil, fmt.Errorf("lock session snapshots: %w", err)
	}
	return f, nil
}
