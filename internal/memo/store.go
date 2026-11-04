package memo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// MaxRecordBytes caps one memo file so a corrupt entry cannot exhaust memory.
const MaxRecordBytes = 16 * 1024

func (s *FileStore) warnf(format string, args ...any) {
	if s.warn != nil {
		s.warn(fmt.Sprintf(format, args...))
	}
}

func (s *FileStore) locked(ctx context.Context, create bool, fn func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if create {
		if err := os.MkdirAll(s.root, 0700); err != nil {
			return err
		}
	} else if _, err := os.Stat(s.root); errors.Is(err, os.ErrNotExist) {
		return fn()
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
	return fn()
}

// Snapshot returns memos newest-first (CreatedAt desc, ID tie-break).
func (s *FileStore) Snapshot(ctx context.Context) ([]Memo, error) {
	var out []Memo
	err := s.locked(ctx, false, func() error {
		var err error
		out, err = s.snapshot(ctx)
		return err
	})
	return out, err
}

func (s *FileStore) snapshot(ctx context.Context) ([]Memo, error) {
	var out []Memo
	dir := filepath.Join(s.root, "global")
	entries, err := os.ReadDir(dir)
	if err != nil && errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name := entry.Name()
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() || info.Size() > MaxRecordBytes {
			s.warnf("skipping invalid memo %q: non-regular or oversized entry", name)
			continue
		}
		f, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		data, readErr := io.ReadAll(io.LimitReader(f, MaxRecordBytes+1))
		f.Close()
		if readErr != nil {
			return nil, readErr
		}
		if len(data) > MaxRecordBytes {
			return nil, fmt.Errorf("memo entry %q exceeds size limit", name)
		}
		var m Memo
		if err := decode(data, &m); err != nil {
			s.warnf("skipping invalid memo %q: %v", name, err)
			continue
		}
		if err := validate(m); err != nil {
			s.warnf("skipping invalid memo %q: %v", name, err)
			continue
		}
		if name != m.ID+".json" {
			s.warnf("skipping invalid memo %q: filename does not match memo ID", name)
			continue
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID > out[j].ID
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}

func decode(data []byte, target any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return err
	}
	if err := d.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("expected one JSON value")
	}
	return nil
}

// Count returns the number of valid memos.
func (s *FileStore) Count(ctx context.Context) (int, error) {
	memos, err := s.Snapshot(ctx)
	if err != nil {
		return 0, err
	}
	return len(memos), nil
}

// Add stores trimmed text as a new memo and returns it with the new total.
func (s *FileStore) Add(ctx context.Context, text string) (Memo, int, error) {
	text = normalize(text)
	if !validText(text, MaxMemoBytes, true) {
		return Memo{}, 0, fmt.Errorf("memo text must be 1-%d bytes", MaxMemoBytes)
	}
	id, err := NewID()
	if err != nil {
		return Memo{}, 0, err
	}
	now := time.Now().UTC()
	m := Memo{Version: Version, ID: id, Text: text, CreatedAt: now, UpdatedAt: now}
	if err := validate(m); err != nil {
		return Memo{}, 0, err
	}
	var total int
	err = s.locked(ctx, true, func() error {
		existing, err := s.snapshot(ctx)
		if err != nil {
			return err
		}
		if len(existing) >= MaxMemos {
			return fmt.Errorf("memo store limit is %d memos", MaxMemos)
		}
		for _, e := range existing {
			if e.ID == m.ID {
				return fmt.Errorf("memo ID collision; retry")
			}
		}
		data, err := json.MarshalIndent(m, "", "  ")
		if err != nil {
			return err
		}
		data = append(data, '\n')
		dir := filepath.Join(s.root, "global")
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
		if err := writeRecord(filepath.Join(dir, m.ID+".json"), data); err != nil {
			return err
		}
		total = len(existing) + 1
		return nil
	})
	if err != nil {
		return Memo{}, 0, err
	}
	return m, total, nil
}

// Update replaces the text of one memo, preserving its creation time.
func (s *FileStore) Update(ctx context.Context, id, text string) (Memo, error) {
	text = normalize(text)
	if !validID.MatchString(id) {
		return Memo{}, fmt.Errorf("unknown memo ID %q", id)
	}
	if !validText(text, MaxMemoBytes, true) {
		return Memo{}, fmt.Errorf("memo text must be 1-%d bytes", MaxMemoBytes)
	}
	var updated Memo
	err := s.locked(ctx, true, func() error {
		existing, err := s.snapshot(ctx)
		if err != nil {
			return err
		}
		found := false
		for _, e := range existing {
			if e.ID == id {
				updated = e
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("unknown memo ID %q", id)
		}
		updated.Text = text
		updated.UpdatedAt = time.Now().UTC()
		if err := validate(updated); err != nil {
			return err
		}
		data, err := json.MarshalIndent(updated, "", "  ")
		if err != nil {
			return err
		}
		if err := writeRecord(filepath.Join(s.root, "global", id+".json"), append(data, '\n')); err != nil {
			return err
		}
		return nil
	})
	return updated, err
}

// Delete removes one memo and returns the remaining total.
func (s *FileStore) Delete(ctx context.Context, id string) (int, error) {
	if !validID.MatchString(id) {
		return 0, fmt.Errorf("unknown memo ID %q", id)
	}
	var total int
	err := s.locked(ctx, false, func() error {
		existing, err := s.snapshot(ctx)
		if err != nil {
			return err
		}
		found := false
		for _, e := range existing {
			if e.ID == id {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("unknown memo ID %q", id)
		}
		if err := os.Remove(filepath.Join(s.root, "global", id+".json")); err != nil {
			return err
		}
		total = len(existing) - 1
		return nil
	})
	return total, err
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
