package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"qcode/internal/redaction"
)

// Metadata uses separate files so edits to one field, and conversation saves,
// cannot revert another field. Missing files are compatible with old sessions.
func (s *Store) LoadMetadata(id string) (Metadata, error) {
	var metadata Metadata
	if !validID(id) {
		return metadata, fmt.Errorf("invalid session ID")
	}
	var name struct {
		Name *string `json:"name"`
	}
	var pin struct {
		Pinned *bool `json:"pinned"`
	}
	nameErr := s.readMetadata(id, "name", &name)
	if nameErr == nil && name.Name != nil {
		metadata.Name, nameErr = s.sanitizeName(*name.Name)
	}
	pinErr := s.readMetadata(id, "pin", &pin)
	if pinErr == nil && pin.Pinned != nil {
		metadata.Pinned = *pin.Pinned
	}
	return metadata, errors.Join(nameErr, pinErr)
}

func (s *Store) readMetadata(id, field string, value any) error {
	data, err := os.ReadFile(filepath.Join(s.dir, "metadata", id+"."+field+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err == nil {
		var fields map[string]json.RawMessage
		if !utf8.Valid(data) {
			err = fmt.Errorf("invalid UTF-8")
		} else if err = json.Unmarshal(data, &fields); err == nil {
			key := field
			if field == "pin" {
				key = "pinned"
			}
			if raw, ok := fields[key]; !ok || string(raw) == "null" {
				err = fmt.Errorf("missing %s value", key)
			} else {
				err = json.Unmarshal(data, value)
			}
		}
	}
	if err != nil {
		return fmt.Errorf("cannot read session %s: %w", field, err)
	}
	return nil
}

func (s *Store) sanitizeName(name string) (string, error) {
	if !utf8.ValidString(name) {
		return "", fmt.Errorf("session name contains invalid UTF-8")
	}
	name = strings.TrimSpace(name)
	if s.nameFilter != nil {
		return s.nameFilter(name), nil
	}
	return (*redaction.Policy)(nil).Text(redaction.Persistence, name), nil
}

// Rename trims the custom name; an empty name restores the automatic label.
func (s *Store) Rename(id, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	name, err := s.sanitizeName(name)
	if err != nil {
		return err
	}
	return s.writeMetadata(id, "name", struct {
		Name string `json:"name"`
	}{name})
}

func (s *Store) SetPinned(id string, pinned bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeMetadata(id, "pin", struct {
		Pinned bool `json:"pinned"`
	}{pinned})
}

func (s *Store) writeMetadata(id, field string, value any) error {
	if _, err := s.Load(id); err != nil {
		return err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	dir := filepath.Join(s.dir, "metadata")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	return atomicWrite(dir, id+"."+field+".json", data)
}
