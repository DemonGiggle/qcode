package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func openTestLockFile(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// Normal acquisition works, and a second acquisition waits for (serializes
// behind) the first instead of bypassing it or failing immediately.
func TestSnapshotLockAcquireSerializes(t *testing.T) {
	path := filepath.Join(t.TempDir(), snapshotLockName)

	holder := openTestLockFile(t, path)
	defer holder.Close()
	if err := lockSnapshotFile(holder); err != nil {
		t.Fatalf("first acquire failed: %v", err)
	}

	waiter := openTestLockFile(t, path)
	defer waiter.Close()
	acquired := make(chan error, 1)
	go func() { acquired <- lockSnapshotFile(waiter) }()
	start := time.Now()

	select {
	case err := <-acquired:
		t.Fatalf("second acquire bypassed a held lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	if err := holder.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-acquired:
		if err != nil {
			t.Fatalf("second acquire failed after release: %v", err)
		}
	case <-time.After(snapshotLockTimeout):
		t.Fatal("second acquire did not serialize behind release")
	}
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond {
		t.Fatalf("second acquire did not wait for release: %v", elapsed)
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("lock file must stay on disk: %v", err)
	}
}

// While the lock is held, acquisition fails with the busy error at the
// deadline instead of blocking indefinitely.
func TestSnapshotLockTimeout(t *testing.T) {
	oldTimeout, oldPoll := snapshotLockTimeout, snapshotLockPollInterval
	snapshotLockTimeout, snapshotLockPollInterval = 300*time.Millisecond, 10*time.Millisecond
	defer func() { snapshotLockTimeout, snapshotLockPollInterval = oldTimeout, oldPoll }()

	path := filepath.Join(t.TempDir(), snapshotLockName)

	holder := openTestLockFile(t, path)
	defer holder.Close()
	if err := lockSnapshotFile(holder); err != nil {
		t.Fatalf("holder acquire failed: %v", err)
	}

	contender := openTestLockFile(t, path)
	defer contender.Close()
	start := time.Now()
	err := lockSnapshotFile(contender)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("acquire succeeded while the lock was held")
	}
	if !strings.Contains(err.Error(), "session snapshots busy") {
		t.Fatalf("timeout error is not actionable: %v", err)
	}
	if elapsed < snapshotLockTimeout {
		t.Fatalf("acquire gave up before the deadline: %v", elapsed)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("acquire blocked past the deadline: %v", elapsed)
	}
}

// After the holder releases, acquisition succeeds again on the same fd.
func TestSnapshotLockAcquireAfterRelease(t *testing.T) {
	oldTimeout, oldPoll := snapshotLockTimeout, snapshotLockPollInterval
	snapshotLockTimeout, snapshotLockPollInterval = 200*time.Millisecond, 10*time.Millisecond
	defer func() { snapshotLockTimeout, snapshotLockPollInterval = oldTimeout, oldPoll }()

	path := filepath.Join(t.TempDir(), snapshotLockName)

	holder := openTestLockFile(t, path)
	if err := lockSnapshotFile(holder); err != nil {
		holder.Close()
		t.Fatalf("holder acquire failed: %v", err)
	}

	contender := openTestLockFile(t, path)
	defer contender.Close()
	if err := lockSnapshotFile(contender); err == nil {
		holder.Close()
		t.Fatal("acquire succeeded while the lock was held")
	}

	if err := holder.Close(); err != nil {
		t.Fatal(err)
	}
	if err := lockSnapshotFile(contender); err != nil {
		t.Fatalf("acquire failed after release: %v", err)
	}
}
