package learning

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"time"
)

func (s *FileStore) locked(ctx context.Context, create bool, fn func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if create {
		if err := os.MkdirAll(s.root, 0700); err != nil {
			return err
		}
	} else if _, err := os.Stat(s.root); errors.Is(err, os.ErrNotExist) {
		return fn() // Empty store: retrieval must not create directories or files.
	} else if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(s.root, ".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	for {
		ok, err := tryLock(f)
		if err != nil {
			return err
		}
		if ok {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
	defer unlock(f)
	if err := s.recover(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn()
}

// A rename-based transaction has only two observable states under the lock:
// global exists (committed), or .previous exists without global (roll back).
// This also recovers an interrupted directory swap after a process crash.
func (s *FileStore) recover() error {
	previous := filepath.Join(s.root, ".previous")
	if _, err := os.Stat(previous); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	global := filepath.Join(s.root, "global")
	if _, err := os.Stat(global); errors.Is(err, os.ErrNotExist) {
		return os.Rename(previous, global)
	} else if err != nil {
		return err
	}
	return os.RemoveAll(previous)
}

func (s *FileStore) Apply(ctx context.Context, plan Plan) (string, error) {
	backup := ""
	if len(plan.Changes) == 0 {
		return backup, nil
	}
	err := s.locked(ctx, true, func() error {
		snapshot, err := s.snapshot(ctx)
		if err != nil {
			return err
		}
		if snapshot.Revision != plan.Revision {
			return ErrConflict
		}
		current := map[string]Learning{}
		for _, l := range snapshot.Items {
			current[l.ID] = l
		}
		seen := map[string]bool{}
		for _, change := range plan.Changes {
			if change.Before == nil && change.After == nil {
				return fmt.Errorf("empty learning change")
			}
			id := ""
			if change.Before != nil {
				id = change.Before.ID
				stored, ok := current[id]
				if !ok || !reflect.DeepEqual(stored, *change.Before) {
					return ErrConflict
				}
			}
			if change.After != nil {
				if err := validate(*change.After); err != nil {
					return err
				}
				if id != "" && id != change.After.ID {
					return fmt.Errorf("learning ID cannot change")
				}
				id = change.After.ID
				if _, ok := current[id]; ok && change.Before == nil {
					return ErrConflict
				}
			}
			if seen[id] {
				return fmt.Errorf("duplicate learning change")
			}
			seen[id] = true
		}
		stage, err := os.MkdirTemp(s.root, ".stage-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(stage)
		global := filepath.Join(s.root, "global")
		if err := copyRecords(ctx, global, stage); err != nil {
			return err
		}
		for _, change := range plan.Changes {
			if err := ctx.Err(); err != nil {
				return err
			}
			if change.After == nil {
				if err := os.Remove(filepath.Join(stage, change.Before.ID+".json")); err != nil {
					return err
				}
			} else {
				data, err := json.MarshalIndent(change.After, "", "  ")
				if err != nil {
					return err
				}
				if err := writeRecord(filepath.Join(stage, change.After.ID+".json"), append(data, '\n')); err != nil {
					return err
				}
			}
		}
		entries, err := os.ReadDir(stage)
		if err != nil {
			return err
		}
		if len(entries) > MaxRecords {
			return fmt.Errorf("global store limit is %d records", MaxRecords)
		}
		if plan.Compact {
			backupRoot := filepath.Join(s.root, "backups")
			if err := os.MkdirAll(backupRoot, 0700); err != nil {
				return err
			}
			backup, err = os.MkdirTemp(backupRoot, "compact-")
			if err != nil {
				return err
			}
			if err := copyRecords(ctx, global, backup); err != nil {
				os.RemoveAll(backup)
				backup = ""
				return err
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		previous := filepath.Join(s.root, ".previous")
		hadGlobal := true
		if err := s.rename(global, previous); errors.Is(err, os.ErrNotExist) {
			hadGlobal = false
		} else if err != nil {
			return err
		}
		// Do not honor cancellation halfway through a swap: finish or roll back.
		if err := s.rename(stage, global); err != nil {
			if hadGlobal {
				if rollbackErr := os.Rename(previous, global); rollbackErr != nil {
					return fmt.Errorf("commit failed: %v; rollback failed: %w; restart to retry recovery", err, rollbackErr)
				}
			}
			return err
		}
		if hadGlobal {
			if err := os.RemoveAll(previous); err != nil && s.warn != nil {
				s.warn("learning saved; old transaction directory will be cleaned on next access")
			}
		}
		return nil
	})
	return backup, err
}

func writeRecord(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}
func copyRecords(ctx context.Context, from, to string) error {
	entries, err := os.ReadDir(from)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("cannot copy non-regular learning entry %q", entry.Name())
		}
		if info.Size() > MaxRecordBytes {
			return fmt.Errorf("learning entry %q is too large to safely back up; repair it before modifying the store", entry.Name())
		}
		input, err := os.Open(filepath.Join(from, entry.Name()))
		if err != nil {
			return err
		}
		data, err := io.ReadAll(io.LimitReader(input, MaxRecordBytes+1))
		input.Close()
		if err != nil {
			return err
		}
		if len(data) > MaxRecordBytes {
			return fmt.Errorf("learning entry grew during backup")
		}
		if err := writeRecord(filepath.Join(to, entry.Name()), data); err != nil {
			return err
		}
	}
	return nil
}
