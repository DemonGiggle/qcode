package tui

import (
	"strings"
	"testing"
	"time"

	"qcode/internal/session"
)

func managerLayoutEntries() []session.Entry {
	recency := time.Date(2027, time.January, 23, 15, 4, 0, 0, time.Local)
	mk := func(workspace, preview, problem string) session.Entry {
		return session.Entry{
			Snapshot: session.Snapshot{
				Recency:   recency,
				Preview:   preview,
				Workspace: workspace,
			},
			WorkspaceKey: strings.Repeat("a", 64),
			Problem:      problem,
		}
	}
	return []session.Entry{
		mk("/tmp/a", "short", ""),
		mk("/tmp/very-long-directory-name-xyz", "short", "missing directory"),
	}
}

func TestSessionManagerFolderWidthIsFixed(t *testing.T) {
	options := sessionPickerOptions{visible: 8, manager: true}
	if got := options.folderWidth(120); got != sessionManagerFolderWidth {
		t.Fatalf("wide folder width = %d, want %d", got, sessionManagerFolderWidth)
	}
	if got := options.folderWidth(80); got != 20 {
		t.Fatalf("narrow folder width = %d, want 20", got)
	}
	// Tiny terminals must keep room for the session label after "[ ] " + folder + " | ".
	if got := options.folderWidth(10); got != 2 {
		t.Fatalf("tiny folder width = %d, want 2", got)
	}
	if got := 4 + options.folderWidth(10) + 3; got >= 10 {
		t.Fatalf("tiny prefix width = %d leaves no room for the session label", got)
	}

	entries := managerLayoutEntries()
	p := sessionPicker{entries: entries, matches: options.matchingIndices(entries, ""), marked: map[string]bool{}}
	lines, _ := p.render(options, 120, 20)
	sep := strings.Index(lines[1], "|")
	if sep < 0 {
		t.Fatalf("manager row lacks a folder separator: %q", lines[1])
	}
	if other := strings.Index(lines[2], "|"); other != sep {
		t.Fatalf("folder separators do not align: %d vs %d: %q vs %q", sep, other, lines[1], lines[2])
	}
}

func TestSessionManagerUsesShortDate(t *testing.T) {
	entries := managerLayoutEntries()
	manager := sessionPickerOptions{visible: 8, manager: true}
	resume := sessionPickerOptions{visible: 8}
	if got := manager.entryLabel(entries[0]); !strings.Contains(got, "2027/01/23") || strings.Contains(got, "Jan") {
		t.Fatalf("manager date = %q, want 2027/01/23", got)
	}
	if got := resume.entryLabel(entries[0]); !strings.Contains(got, "Jan 23 15:04") || strings.Contains(got, "2027/01/23") {
		t.Fatalf("resume date = %q, want Jan 23 15:04", got)
	}
}

func TestSessionManagerErrorUsesDistinctColor(t *testing.T) {
	entries := managerLayoutEntries()
	manager := sessionPickerOptions{visible: 8, color: true, manager: true, deleteSelected: func([]string) error { return nil }}
	resume := sessionPickerOptions{visible: 8, color: true}

	if line := manager.renderEntry(entries[1], false, 120); !strings.Contains(line, "\x1b[31m") {
		t.Fatalf("manager problem row lacks red: %q", line)
	}
	if line := resume.renderEntry(entries[1], false, 120); !strings.Contains(line, "\x1b[33m") || strings.Contains(line, "\x1b[31m") {
		t.Fatalf("resume problem row should stay yellow: %q", line)
	}

	p := sessionPicker{entries: entries, matches: manager.matchingIndices(entries, ""), marked: map[string]bool{}, message: "missing directory"}
	lines, _ := p.render(manager, 120, 20)
	found := false
	for _, line := range lines {
		if strings.Contains(line, "missing directory") {
			found = true
			if !strings.Contains(line, "\x1b[31m") {
				t.Fatalf("manager error line lacks red: %q", line)
			}
		}
	}
	if !found {
		t.Fatalf("manager error line missing: %q", lines)
	}
}

func TestSessionManagerSearchMatchesBothDateFormats(t *testing.T) {
	entries := managerLayoutEntries()
	manager := sessionPickerOptions{visible: 8, manager: true}
	resume := sessionPickerOptions{visible: 8}
	for _, query := range []string{"2027/01/23", "Jan 23"} {
		if got := manager.matchingIndices(entries, query); len(got) == 0 {
			t.Fatalf("manager failed to match %q", query)
		}
		if got := resume.matchingIndices(entries, query); len(got) == 0 {
			t.Fatalf("resume failed to match %q", query)
		}
	}
}

func TestSessionManagerExplicitFlagSurvivesNilDeleteSelected(t *testing.T) {
	options := sessionPickerOptions{visible: 8, manager: true}
	if !options.global() {
		t.Fatalf("explicit manager flag should imply global layout")
	}
	if got := (sessionPickerOptions{}).global(); got {
		t.Fatalf("empty options should remain resume layout")
	}
}
