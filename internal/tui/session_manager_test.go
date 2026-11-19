package tui

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"qcode/internal/session"
)

func TestSessionManagerMarksAcrossWorkspaceFilters(t *testing.T) {
	entries := []session.Entry{
		{Snapshot: session.Snapshot{ID: strings.Repeat("a", 32), Workspace: filepath.Join(t.TempDir(), "project-one")}, WorkspaceKey: strings.Repeat("1", 64)},
		{Snapshot: session.Snapshot{ID: strings.Repeat("a", 32), Workspace: filepath.Join(t.TempDir(), "project-two")}, WorkspaceKey: strings.Repeat("2", 64)},
	}
	selected := []string{sessionEntryKey(entries[0]), sessionEntryKey(entries[1])}
	calls := 0
	options := sessionPickerOptions{
		visible: 8, width: 120, height: 20,
		refresh: func() ([]session.Entry, error) { return entries, nil },
		deleteSelected: func(keys []string) error {
			calls++
			if !reflect.DeepEqual(keys, selected) {
				t.Fatalf("deleted keys = %v, want %v", keys, selected)
			}
			entries = nil
			return nil
		},
	}
	// Mark one session under each filter, cancel confirmation once, then confirm.
	keys := []string{
		"project-one", "\x18", "\x15", "project-two", "\x18",
		"\x1b[3~", "\r", "\x1b[3~", arrowDownSequence, "\r", "\x03",
	}
	var out strings.Builder
	_, accepted, err := runSessionPicker(&sessionPickerKeys{keys: keys}, &out, entries, options)
	if err != nil || accepted || calls != 1 {
		t.Fatalf("accepted = %v, err = %v, delete calls = %d", accepted, err, calls)
	}
	for _, text := range []string{"[x] project-one", "[x] project-two", "Delete 2 marked sessions?", "No matching sessions"} {
		if !strings.Contains(out.String(), text) {
			t.Errorf("missing picker output %q", text)
		}
	}
}
