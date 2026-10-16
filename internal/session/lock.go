package session

import (
	"fmt"
	"os"
	"path/filepath"
)

const snapshotLockName = ".snapshots.lock"

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
