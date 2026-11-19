package session

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Catalog manages session files across the hashed workspace directories.
type Catalog struct {
	directory string
	filter    SnapshotFilter
	name      func(string) string
}

func NewCatalog(directory string, filter SnapshotFilter, nameFilter func(string) string) *Catalog {
	return &Catalog{directory: directory, filter: filter, name: nameFilter}
}

func validWorkspaceKey(key string) bool {
	decoded, err := hex.DecodeString(key)
	return err == nil && len(decoded) == 32
}

func (c *Catalog) store(key string) (*Store, error) {
	if !validWorkspaceKey(key) {
		return nil, fmt.Errorf("invalid session workspace")
	}
	dir := filepath.Join(c.directory, key)
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("invalid session workspace directory")
	}
	return &Store{dir: dir, workspaceKey: key, filter: c.filter, nameFilter: c.name}, nil
}

func (c *Catalog) workspaceKeys() ([]string, error) {
	dirs, err := os.ReadDir(c.directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var keys []string
	for _, dir := range dirs {
		if dir.IsDir() && validWorkspaceKey(dir.Name()) {
			keys = append(keys, dir.Name())
		}
	}
	return keys, nil
}

func (c *Catalog) List() ([]Entry, error) {
	keys, err := c.workspaceKeys()
	if err != nil {
		return nil, err
	}
	var entries []Entry
	for _, key := range keys {
		store, err := c.store(key)
		if err != nil {
			return nil, err
		}
		batch, err := store.List()
		if err != nil {
			return nil, err
		}
		entries = append(entries, batch...)
	}
	sortEntries(entries)
	return entries, nil
}

func (c *Catalog) Rename(entry Entry, name string) error {
	store, err := c.store(entry.WorkspaceKey)
	if err != nil {
		return err
	}
	return store.Rename(entry.ID, name)
}

func (c *Catalog) SetPinned(entry Entry, pinned bool) error {
	store, err := c.store(entry.WorkspaceKey)
	if err != nil {
		return err
	}
	return store.SetPinned(entry.ID, pinned)
}

func (c *Catalog) Delete(entries ...Entry) error {
	byWorkspace := make(map[string][]string)
	var keys []string
	for _, entry := range entries {
		if !validWorkspaceKey(entry.WorkspaceKey) || !validID(entry.ID) {
			return fmt.Errorf("invalid session reference")
		}
		if _, found := byWorkspace[entry.WorkspaceKey]; !found {
			keys = append(keys, entry.WorkspaceKey)
		}
		byWorkspace[entry.WorkspaceKey] = append(byWorkspace[entry.WorkspaceKey], entry.ID)
	}
	sort.Strings(keys)
	var failures []error
	for _, key := range keys {
		store, err := c.store(key)
		if err == nil {
			err = store.deleteSelected(byWorkspace[key])
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			failures = append(failures, fmt.Errorf("workspace %.12s: %w", key, err))
		}
	}
	return errors.Join(failures...)
}

func (s *Store) deleteSelected(ids []string) error {
	lock, err := s.lockSnapshots()
	if err != nil {
		return err
	}
	defer lock.Close()
	var failures []error
	for _, id := range ids {
		if err := s.delete(id); err != nil {
			failures = append(failures, fmt.Errorf("session %s: %w", id, err))
		}
	}
	return errors.Join(failures...)
}

// DeleteAll also removes orphaned name and pin files in each workspace.
func (c *Catalog) DeleteAll() error {
	keys, err := c.workspaceKeys()
	if err != nil {
		return err
	}
	var failures []error
	for _, key := range keys {
		store, err := c.store(key)
		if err == nil {
			err = store.DeleteAll()
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			failures = append(failures, fmt.Errorf("workspace %.12s: %w", key, err))
		}
	}
	return errors.Join(failures...)
}
