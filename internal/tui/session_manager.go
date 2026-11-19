package tui

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/term"

	"qcode/internal/redaction"
	"qcode/internal/session"
)

// SavedSessionSelection retains the terminal input reader so that the picker
// and the resumed UI never compete for keystrokes.
type SavedSessionSelection struct {
	Workspace string
	id        string
	input     *interruptReader
}

func (s *SavedSessionSelection) Input() io.Reader { return s.input }

func (s *SavedSessionSelection) NewUI(in, out *os.File, provider, model string) *UI {
	u := newUI(in, out, s.input, nil, provider, model, s.Workspace)
	u.startupSessionID = s.id
	return u
}

func ManageSavedSessions(in, out *os.File, catalog *session.Catalog, policy *redaction.Policy) (*SavedSessionSelection, error) {
	if !term.IsTerminal(int(in.Fd())) || !term.IsTerminal(int(out.Fd())) {
		return nil, fmt.Errorf("qcode sessions requires an interactive terminal")
	}
	var byKey map[string]session.Entry
	refresh := func() ([]session.Entry, error) {
		entries, err := catalog.List()
		if err != nil {
			return nil, err
		}
		byKey = make(map[string]session.Entry, len(entries))
		for _, entry := range entries {
			byKey[sessionEntryKey(entry)] = entry
		}
		return entries, nil
	}
	entries, err := refresh()
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		fmt.Fprintln(out, "No saved sessions.")
		return nil, nil
	}
	state, err := term.MakeRaw(int(in.Fd()))
	if err != nil {
		return nil, fmt.Errorf("enable terminal mode: %w", err)
	}
	defer term.Restore(int(in.Fd()), state)
	fmt.Fprint(out, "\x1b[?1049h\x1b[?25l")
	defer fmt.Fprint(out, "\x1b[?25h\x1b[?1049l")
	input := newInterruptReader(in)
	input.setRaw(true)
	input.start()
	defer input.setRaw(false)
	var selection *SavedSessionSelection
	options := sessionPickerOptions{
		visible: maxSessionListRows, color: ColorEnabled(out), unicode: UnicodeEnabled(), manager: true,
		policy: policy, size: func() (int, int) { return terminalSize(out) }, refresh: refresh,
		rename:    func(key, name string) error { return catalog.Rename(byKey[key], name) },
		setPinned: func(key string, pinned bool) error { return catalog.SetPinned(byKey[key], pinned) },
		deleteSession: func(key string) (bool, error) {
			return false, catalog.Delete(byKey[key])
		},
		deleteAllSessions: func() (bool, error) { return false, catalog.DeleteAll() },
		deleteSelected: func(keys []string) error {
			selected := make([]session.Entry, 0, len(keys))
			for _, key := range keys {
				selected = append(selected, byKey[key])
			}
			return catalog.Delete(selected...)
		},
		accept: func(key string) error {
			entry := byKey[key]
			if entry.Problem != "" {
				return fmt.Errorf("%s", entry.Problem)
			}
			resolved, err := filepath.EvalSymlinks(entry.Workspace)
			if err != nil {
				return fmt.Errorf("open workspace: %w", err)
			}
			info, err := os.Stat(resolved)
			if err != nil {
				return fmt.Errorf("open workspace: %w", err)
			}
			if resolved != entry.Workspace || !info.IsDir() {
				return fmt.Errorf("saved workspace no longer points to the original directory")
			}
			selection = &SavedSessionSelection{Workspace: entry.Workspace, id: entry.ID, input: input}
			return nil
		},
	}
	if _, _, err := runSessionPicker(input, out, entries, options); err != nil && err != io.EOF {
		return nil, err
	}
	return selection, nil
}
