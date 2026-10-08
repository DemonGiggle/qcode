package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"qcode/internal/redaction"
	"qcode/internal/session"
)

// Separate key events let tests distinguish Esc from an immediately following
// key, as interruptReader does for real terminals with its short timeout.
type sessionPickerKeys struct {
	keys    []string
	pending string
}

func (r *sessionPickerKeys) Read(data []byte) (int, error) {
	if r.pending == "" {
		if len(r.keys) == 0 {
			return 0, io.EOF
		}
		r.pending, r.keys = r.keys[0], r.keys[1:]
	}
	n := copy(data, r.pending)
	r.pending = r.pending[n:]
	return n, nil
}
func (r *sessionPickerKeys) readSelectorEscapeTail() []byte {
	tail := []byte(r.pending)
	r.pending = ""
	return tail
}

func sessionPickerFixture(t *testing.T) (*session.Store, []session.Entry, sessionPickerOptions) {
	t.Helper()
	s, err := session.Open(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		snap, err := s.New()
		if err != nil {
			t.Fatal(err)
		}
		snap.Recency = time.Now().UTC().Add(-time.Duration(i) * time.Hour)
		snap.Preview = fmt.Sprintf("automatic preview %d", i)
		if err := s.Save(snap); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	return s, entries, sessionPickerOptions{visible: 12, width: 120, height: 18, unicode: true,
		rename: s.Rename, setPinned: s.SetPinned, refresh: s.List}
}

func TestSessionPickerRenameClearAndUTF8Editing(t *testing.T) {
	for _, test := range []struct {
		name, initial, want string
		keys                []string
	}{
		{"trim", "", "設計 😀", []string{"設計 😀  "}},
		{"prefill and insert", "設計😀", "設稿😀", []string{"\x1b[D", "\x7f", "稿"}},
		{"delete", "設計😀", "新😀", []string{"\x01", "\x1b[3~", "\x1b[3~", "新", "\x05"}},
		{"clear", "custom name", "", []string{"\x15", "  "}},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, entries, options := sessionPickerFixture(t)
			id := entries[0].ID
			if err := s.Rename(id, test.initial); err != nil {
				t.Fatal(err)
			}
			entries, _ = s.List()
			keys := append([]string{"\t", "\r"}, test.keys...)
			keys = append(keys, "\r", "\r")
			var output strings.Builder
			got, accepted, err := runSessionPicker(&sessionPickerKeys{keys: keys}, &output, entries, options)
			if err != nil || !accepted || got != id {
				t.Fatalf("result: %q %v %v", got, accepted, err)
			}
			metadata, err := s.LoadMetadata(id)
			if err != nil || metadata.Name != test.want {
				t.Fatalf("name: %+v %v, want %q", metadata, err, test.want)
			}
			loaded, _ := s.Load(id)
			if !loaded.Recency.Equal(entries[0].Recency) {
				t.Fatal("rename changed prompt recency")
			}
			if test.want == "" && !strings.Contains(output.String(), entries[0].Preview) {
				t.Fatal("clearing name did not restore automatic label")
			}
		})
	}
}

func TestSessionPickerFilteringNamesAndAutomaticPreviews(t *testing.T) {
	s, entries, options := sessionPickerFixture(t)
	if err := s.Rename(entries[1].ID, "設計專案"); err != nil {
		t.Fatal(err)
	}
	entries, _ = s.List()
	for _, query := range []string{"設計", "preview 1"} {
		var output strings.Builder
		got, accepted, err := runSessionPicker(&sessionPickerKeys{keys: []string{query, "\r"}}, &output, entries, options)
		if err != nil || !accepted || got != entries[1].ID {
			t.Fatalf("filter %q: %q %v %v", query, got, accepted, err)
		}
	}
	// Backspace removes a complete rune and Ctrl+U clears the entire query.
	got, accepted, err := runSessionPicker(&sessionPickerKeys{keys: []string{"設計錯", "\x7f", "\r"}}, io.Discard, entries, options)
	if err != nil || !accepted || got != entries[1].ID {
		t.Fatal("UTF-8 backspace filtering", got, accepted, err)
	}
	got, accepted, err = runSessionPicker(&sessionPickerKeys{keys: []string{"absent", "\x15", "\r"}}, io.Discard, entries, options)
	if err != nil || !accepted || got != entries[0].ID {
		t.Fatal("filter reset", got, accepted, err)
	}
}

func TestSessionPickerPinReorderingPreservesSelection(t *testing.T) {
	s, entries, options := sessionPickerFixture(t)
	id := entries[1].ID
	got, accepted, err := runSessionPicker(&sessionPickerKeys{keys: []string{
		arrowDownSequence, "\t", arrowDownSequence, "\r", "\r",
	}}, io.Discard, entries, options)
	if err != nil || !accepted || got != id {
		t.Fatal("pin changed selection", got, accepted, err)
	}
	entries, _ = s.List()
	if !entries[0].Pinned || entries[0].ID != id {
		t.Fatal("pinned session not first", entries)
	}
	var output strings.Builder
	got, accepted, err = runSessionPicker(&sessionPickerKeys{keys: []string{
		"\t", arrowDownSequence, "\r", "\r",
	}}, &output, entries, options)
	if err != nil || !accepted || got != id {
		t.Fatal("unpin changed selection", got, accepted, err)
	}
	if !strings.Contains(output.String(), "Unpin") {
		t.Fatal("pin menu did not update")
	}
	metadata, _ := s.LoadMetadata(id)
	if metadata.Pinned {
		t.Fatal("unpin not persisted")
	}
}

func TestSessionPickerRenameExcludedByFilterSelectsFirstRemainingMatch(t *testing.T) {
	s, entries, options := sessionPickerFixture(t)
	for i, entry := range entries[:2] {
		if err := s.Rename(entry.ID, fmt.Sprintf("match %d", i)); err != nil {
			t.Fatal(err)
		}
	}
	entries, _ = s.List()
	got, accepted, err := runSessionPicker(&sessionPickerKeys{keys: []string{
		"match", "\t", "\r", "\x15", "excluded", "\r", "\r",
	}}, io.Discard, entries, options)
	if err != nil || !accepted || got != entries[1].ID {
		t.Fatal("filtered rename did not select first match", got, accepted, err)
	}
}

func TestSessionPickerMenuAndEditorCancellation(t *testing.T) {
	for _, keys := range [][]string{
		{"\t", "\x1b", "\r"},
		{"\t", "\r", "discarded", "\x1b", "\r"},
		{"\t", "\x03"},
		{"\t", "\r", "discarded", "\x03"},
	} {
		s, entries, options := sessionPickerFixture(t)
		var output strings.Builder
		_, _, err := runSessionPicker(&sessionPickerKeys{keys: keys}, &output, entries, options)
		if err != nil {
			t.Fatal(err)
		}
		metadata, err := s.LoadMetadata(entries[0].ID)
		if err != nil || metadata != (session.Metadata{}) {
			t.Fatal("cancel wrote metadata", metadata, err)
		}
		lastFrame := output.String()[strings.LastIndex(output.String(), "\x1b[2;1H"):]
		if strings.Contains(lastFrame, "Resume") || strings.Contains(lastFrame, "discarded") {
			t.Fatal("exit left stale picker text")
		}
	}
}

func TestSessionPickerWriteErrorsStayOpenWithoutOptimisticUpdates(t *testing.T) {
	for _, action := range []string{"rename", "pin", "resume"} {
		t.Run(action, func(t *testing.T) {
			s, entries, options := sessionPickerFixture(t)
			if err := s.Rename(entries[0].ID, "original"); err != nil {
				t.Fatal(err)
			}
			entries, _ = s.List()
			failure := errors.New("disk full")
			options.rename = func(string, string) error { return failure }
			options.setPinned = func(string, bool) error { return failure }
			options.accept = func(string) error { return failure }
			options.refresh = func() ([]session.Entry, error) { t.Fatal("failed edit was refreshed as saved"); return nil, nil }
			keys := []string{"\t", "\r", "\x15", "unsaved", "\r", "\x03"}
			if action == "pin" {
				keys = []string{"\t", arrowDownSequence, "\r", "\x03"}
			}
			if action == "resume" {
				keys = []string{"\r", "\x03"}
			}
			var output strings.Builder
			_, accepted, err := runSessionPicker(&sessionPickerKeys{keys: keys}, &output, entries, options)
			if err != nil || accepted || !strings.Contains(output.String(), "disk full") {
				t.Fatal("error closed picker", accepted, err, output.String())
			}
			metadata, _ := s.LoadMetadata(entries[0].ID)
			if metadata.Name != "original" || metadata.Pinned {
				t.Fatal("failed edit changed metadata", metadata)
			}
		})
	}
}

func TestSessionPreviewActiveTabArchiveCurrentAndFallbacks(t *testing.T) {
	snap := session.Snapshot{Preview: "fallback\n  content"}
	presentation := savedPresentation{Active: "agent-1", Views: []savedView{
		{ID: "main", History: savedHistory{Lines: []string{"main transcript"}}},
		{ID: "agent-1", History: savedHistory{Archive: []string{"one", "two", "", "  three", "four", "five", "six"}, Lines: []string{"wrong retained line"}, Current: []savedCell{{Char: '七'}, {Char: '😀'}}}},
	}}
	snap.Presentation, _ = json.Marshal(presentation)
	if got := sessionPreview(snap, nil, 80, true, false); !reflect.DeepEqual(got, []string{"one", "two", "  three", "four", "five", "six", "七😀"}) {
		t.Fatal("active archive/current tail", got)
	}
	presentation.Views[1].History.Archive = nil
	snap.Presentation, _ = json.Marshal(presentation)
	if got := sessionPreview(snap, nil, 80, true, false); !reflect.DeepEqual(got, []string{"wrong retained line", "七😀"}) {
		t.Fatal("retained fallback", got)
	}
	presentation.Active = "missing"
	snap.Presentation, _ = json.Marshal(presentation)
	if got := sessionPreview(snap, nil, 80, true, false); !reflect.DeepEqual(got, []string{"main transcript"}) {
		t.Fatal("main fallback", got)
	}
	presentation.Views = nil
	snap.Presentation, _ = json.Marshal(presentation)
	if got := sessionPreview(snap, nil, 80, true, false); !reflect.DeepEqual(got, []string{"fallback", "  content"}) {
		t.Fatal("preview fallback", got)
	}
	snap.Presentation = json.RawMessage(`broken`)
	if got := sessionPreview(snap, nil, 80, true, false); len(got) != 2 {
		t.Fatal("invalid presentation fallback", got)
	}
}

func TestSessionPreviewRedactsBeforeTailAndStripsTerminalEscapes(t *testing.T) {
	policy, _ := redaction.New(redaction.Config{}, []string{"exact-secret"})
	presentation := savedPresentation{Active: "main", Views: []savedView{{ID: "main", History: savedHistory{Archive: []string{
		"old line",
		"-----BEGIN PRIVATE KEY-----",
		"private1", "private2", "private3", "private4", "private5",
		"-----END PRIVATE KEY-----",
		"  \x1b[31mexact-\x1b[0msecret\x1b[2J",
		"\x1b]8;;https://secret.test\x1b\\link\x1b]8;;\a",
		"\x1bPignored payload\x1b\\plain\u009b31m text\u009b0m",
	}, Current: []savedCell{{Char: '尾'}}}}}}
	snap := session.Snapshot{}
	snap.Presentation, _ = json.Marshal(presentation)
	got := sessionPreview(snap, policy, 80, true, false)
	text := strings.Join(got, "\n")
	if strings.Contains(text, "private1") || strings.Contains(text, "private5") || strings.Contains(text, "exact-secret") || strings.Contains(text, "https") || strings.Contains(text, "ignored") || strings.ContainsAny(text, "\x1b\u009b") {
		t.Fatal("unsafe preview", got)
	}
	if !strings.Contains(text, "  "+redaction.Marker) || !strings.Contains(text, "link") || !strings.Contains(text, "plain text") || got[len(got)-1] != "尾" {
		t.Fatal("lost safe content or indentation", got)
	}
	for _, line := range sessionPreview(snap, policy, 7, true, false) {
		if visibleWidth(line) > 7 {
			t.Fatal("preview too wide", line)
		}
	}
}

func TestSessionPreviewPreservesColorsWithoutBypassingRedaction(t *testing.T) {
	policy, _ := redaction.New(redaction.Config{}, []string{"exact-secret"})
	presentation := savedPresentation{Active: "main", Views: []savedView{{ID: "main", History: savedHistory{Archive: []string{
		green + "+ added line" + reset,
		red + "- removed line" + reset,
		"\x1b[38;2;123;45;67mRGB line" + reset,
		cyan + "carried color",
		"still colored" + reset,
		green + "before " + red + "exact-" + bold + "secret" + reset + cyan + " after" + reset,
		"\x1b]8;;https://hidden.test\x1b\\" + yellow + "link\x1b]8;;\a" + reset + "\x1b[2J\x1b[4;1H",
		"\u009b35mC1 style\u009b0m\x1b[?25l\x1b[3 q",
	}}}}}
	for _, char := range "unfinished" {
		presentation.Views[0].History.Current = append(presentation.Views[0].History.Current, savedCell{Char: char, Style: magenta})
	}
	snap := session.Snapshot{}
	snap.Presentation, _ = json.Marshal(presentation)
	preview := sessionPreview(snap, policy, 80, true, true)
	for i, want := range []string{
		green + "+ added line" + reset,
		red + "- removed line" + reset,
		"\x1b[38;2;123;45;67mRGB line" + reset,
		cyan + "carried color" + reset,
		cyan + "still colored" + reset,
	} {
		if preview[i] != want {
			t.Fatalf("line %d lost styling: %q, want %q", i, preview[i], want)
		}
	}
	if !strings.Contains(preview[5], red+redaction.Marker) || !strings.Contains(preview[5], cyan+" after") {
		t.Fatal("redacted text lost surrounding colors", preview[5])
	}
	if preview[len(preview)-1] != magenta+"unfinished"+reset {
		t.Fatal("unfinished saved cells lost their styles", preview)
	}
	text := strings.Join(preview, "\n")
	if strings.Contains(stripSessionEscapes(text), "exact-secret") || !strings.Contains(text, redaction.Marker) || strings.Contains(text, "hidden.test") {
		t.Fatal("colored preview leaked saved content", preview)
	}
	for _, unit := range displayUnits(text) {
		if strings.HasPrefix(unit.raw, "\x1b") && !strings.HasSuffix(unit.raw, "m") {
			t.Fatal("preview retained a terminal control", unit.raw)
		}
	}
	for _, line := range sessionPreview(snap, policy, 9, true, true) {
		if visibleWidth(line) > 9 {
			t.Fatal("colored truncation exceeded width", line)
		}
	}
	plain := sessionPreview(snap, policy, 80, true, false)
	if strings.ContainsAny(strings.Join(plain, "\n"), "\x1b\u009b") {
		t.Fatal("color-disabled preview retained styling", plain)
	}
}

func TestSessionPickerPreviewReservesHintSeparator(t *testing.T) {
	_, entries, options := sessionPickerFixture(t)
	for len(entries) < 20 {
		entries = append(entries, entries[0])
	}
	presentation := savedPresentation{Active: "main", Views: []savedView{{ID: "main"}}}
	for i := 1; i <= 80; i++ {
		presentation.Views[0].History.Archive = append(presentation.Views[0].History.Archive, fmt.Sprintf("%sline %02d%s", green, i, reset))
	}
	entries[0].Presentation, _ = json.Marshal(presentation)
	p := sessionPicker{entries: entries, matches: matchingSessionIndices(entries, "")}
	options.color = true
	for _, test := range []struct{ height, wantLines int }{{20, 6}, {32, 18}, {60, 46}} {
		lines, visible := p.render(options, 80, test.height)
		if visible != 8 || lines[1+visible] != "" {
			t.Fatal("preview lacks space after the session list", lines)
		}
		divider := lines[2+visible]
		if !strings.Contains(divider, "── Preview ─") || visibleWidth(divider) != 80 {
			t.Fatal("preview divider is not a full-width labeled rule", divider)
		}
		count := 0
		for _, line := range lines[3+visible : len(lines)-3] {
			if strings.Contains(line, "line ") {
				count++
				if !strings.Contains(line, green) {
					t.Fatal("picker dropped preview colors", line)
				}
			}
		}
		if count != test.wantLines {
			t.Fatalf("height %d shows %d preview lines, want %d", test.height, count, test.wantLines)
		}
		first := fmt.Sprintf("line %02d", 81-test.wantLines)
		if stripSessionEscapes(lines[3+visible]) != first || stripSessionEscapes(lines[len(lines)-4]) != "line 80" {
			t.Fatal("preview did not fit the transcript tail above its bottom border", lines)
		}
		if stripSessionEscapes(lines[len(lines)-3]) != strings.Repeat("─", 80) || lines[len(lines)-2] != "" || !strings.Contains(lines[len(lines)-1], "Ctrl+C cancel") {
			t.Fatal("preview lacks a bottom border and blank line before keyboard hints", lines)
		}
	}
	// Filtering leaves a shorter list and gives the preview more space. An
	// inline error must reserve its own row without displacing the footer.
	p.matches = p.matches[:1]
	p.message = "failed edit"
	lines, visible := p.render(options, 80, 32)
	if visible != 1 || stripSessionEscapes(lines[4]) != "line 57" || stripSessionEscapes(lines[27]) != "line 80" || stripSessionEscapes(lines[28]) != strings.Repeat("─", 80) || lines[29] != "" || lines[30] != "Error: failed edit" || !strings.Contains(lines[31], "Ctrl+C cancel") {
		t.Fatal("filtered preview did not use the space remaining above the error and footer", lines)
	}
	if got := sessionPreviewRule("Preview", 25, false, false); !strings.HasPrefix(got, "-- Preview --") || visibleWidth(got) != 25 {
		t.Fatal("ASCII preview divider", got)
	}
	if got := sessionPreviewRule("", 25, false, false); got != strings.Repeat("-", 25) {
		t.Fatal("ASCII preview bottom border", got)
	}
}

func TestSessionPickerFitsShortAndNarrowViewports(t *testing.T) {
	_, entries, options := sessionPickerFixture(t)
	p := sessionPicker{entries: entries, matches: []int{0, 1, 2}}
	for height := 1; height <= 18; height++ {
		for _, mode := range []sessionPickerMode{sessionPickerList, sessionPickerActions, sessionPickerRename} {
			p.mode = mode
			for _, message := range []string{"", "failed edit"} {
				p.message = message
				lines, _ := p.render(options, 20, height)
				if len(lines) != height {
					t.Fatalf("height %d rendered %d", height, len(lines))
				}
				var output strings.Builder
				paintSessionRows(&output, lines, 20, true)
				if strings.Contains(output.String(), "\n") {
					t.Fatal("render scrolls bottom row")
				}
				for _, line := range lines {
					if visibleWidth(truncateDiffLine(line, 20, true)) > 20 {
						t.Fatal("row too wide", line)
					}
				}
			}
		}
	}
	p.mode, p.message = sessionPickerList, ""
	full, _ := p.render(options, 120, 12)
	short, _ := p.render(options, 120, 5)
	if !strings.Contains(strings.Join(full, "\n"), "Preview") || strings.Contains(strings.Join(short, "\n"), "Preview") {
		t.Fatal("preview did not shrink first")
	}
	_, visible := p.render(options, 120, 4)
	if visible != 2 {
		t.Fatal("list rows did not shrink after preview", visible)
	}
}

func TestSessionPickerNavigationRepaintsPreviewAndClearsEntireArea(t *testing.T) {
	_, entries, options := sessionPickerFixture(t)
	options.visible = 1
	var output strings.Builder
	id, accepted, err := runSessionPicker(&sessionPickerKeys{keys: []string{selectorPageDown, arrowDownSequence, selectorPageUp, "\r"}}, &output, entries, options)
	if err != nil || !accepted || id != entries[1].ID {
		t.Fatal("paging", id, accepted, err)
	}
	frames := strings.Split(output.String(), "\x1b[2;1H")
	if !strings.Contains(frames[1], entries[0].Preview) || !strings.Contains(frames[2], entries[1].Preview) || !strings.Contains(frames[3], entries[2].Preview) {
		t.Fatal("preview did not follow selection", frames)
	}
	last := frames[len(frames)-1]
	if strings.Contains(last, "Preview") || strings.Count(last, "\x1b[2K") != options.height {
		t.Fatal("exit did not clear entire rendered area", last)
	}
}

func TestSessionPickerTerminalRedactionIncludesNamesAndEditor(t *testing.T) {
	s, entries, options := sessionPickerFixture(t)
	policy, _ := redaction.New(redaction.Config{}, []string{"sensitive-name"})
	options.policy = policy
	entries[0].Name = "sensitive-name"
	if err := s.Rename(entries[0].ID, "sensitive-name"); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	_, _, err := runSessionPicker(&sessionPickerKeys{keys: []string{"\t", "\r", "\x15", "sensitive-name", "\x03"}}, &output, entries, options)
	if err != nil || strings.Contains(output.String(), "sensitive-name") || !strings.Contains(output.String(), redaction.Marker) {
		t.Fatal("name/editor redaction", err, output.String())
	}
	// Masking for display must not change the editor's underlying custom name.
	_, _, err = runSessionPicker(&sessionPickerKeys{keys: []string{"\t", "\r", "\r", "\x03"}}, io.Discard, entries, options)
	metadata, loadErr := s.LoadMetadata(entries[0].ID)
	if err != nil || loadErr != nil || metadata.Name != "sensitive-name" {
		t.Fatal("display redaction changed stored name", metadata, err, loadErr)
	}
}

func TestResumeAllowsConcurrentSessionAndReportsMetadataProblems(t *testing.T) {
	u, _ := persistenceUI(t)
	dir := t.TempDir()
	store, err := session.Open(dir, u.root)
	if err != nil {
		t.Fatal(err)
	}
	other, err := session.Open(dir, u.root)
	if err != nil {
		t.Fatal(err)
	}
	snap, _ := other.New()
	target, _ := persistenceUI(t)
	snap.Agents, _ = target.manager.(savedAgentController).SaveAgents()
	snap.Presentation, _ = json.Marshal(target.snapshotPresentation())
	if err := other.Save(snap); err != nil {
		t.Fatal(err)
	}
	if err := other.Rename(snap.ID, "session name"); err != nil {
		t.Fatal(err)
	}
	if err := other.SetPinned(snap.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := other.LoadForResume(snap.ID); err != nil {
		t.Fatal(err)
	}
	if err := u.EnableSessions(store, func(s session.Snapshot) (*UI, error) {
		v, _ := persistenceUI(t)
		return v, v.RestorePresentation(s.Presentation)
	}); err != nil {
		t.Fatal(err)
	}
	defer func() { u.shutdownAgentManager(); u.closeSession() }()
	u.resumeSessionID(snap.ID)
	if u.persistence.current.ID != snap.ID {
		t.Fatal("concurrent resume blocked")
	}
	if err := u.saveSession(false); err != nil {
		t.Fatal(err)
	}
	if metadata, err := other.LoadMetadata(snap.ID); err != nil || metadata != (session.Metadata{Name: "session name", Pinned: true}) {
		t.Fatal("resume/autosave reverted metadata", metadata, err)
	}
	// Corrupt names still permit switching away and back to the valid snapshot.
	paths, _ := filepath.Glob(filepath.Join(dir, "*", "metadata", snap.ID+".name.json"))
	if len(paths) != 1 {
		t.Fatal("name path", paths)
	}
	if err := os.WriteFile(paths[0], []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	second, _ := other.New()
	second.Agents, second.Presentation = snap.Agents, snap.Presentation
	if err := other.Save(second); err != nil {
		t.Fatal(err)
	}
	u.resumeSessionID(second.ID)
	u.resumeSessionID(snap.ID)
	if u.persistence.current.ID != snap.ID {
		t.Fatal("metadata problem blocked restore")
	}
	if !strings.Contains(strings.Join(u.display.Lines(), "\n"), "Session metadata:") {
		t.Fatal("metadata error not reported")
	}
}

func TestResumeRunningAgentGuardPreservesCurrentSession(t *testing.T) {
	u, m := persistenceUI(t)
	store, _ := session.Open(t.TempDir(), u.root)
	if err := u.EnableSessions(store, nil); err != nil {
		t.Fatal(err)
	}
	defer u.closeSession()
	id := u.persistence.current.ID
	m.agents[0].Summary.Status = session.StatusRunning
	if err := u.restoreSession(strings.Repeat("a", 32)); err == nil || !strings.Contains(err.Error(), "busy agent main") {
		t.Fatal("running guard", err)
	}
	if u.persistence.current.ID != id {
		t.Fatal("running session switched")
	}
}

func TestSessionNameUsesConfiguredPersistenceRedaction(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(fmt.Sprintf("persistence=%v", enabled), func(t *testing.T) {
			u, _ := persistenceUI(t)
			policy, _ := redaction.New(redaction.Config{Persistence: &enabled}, []string{"name-secret"})
			u.SetRedaction(policy)
			store, _ := session.Open(t.TempDir(), u.root)
			if err := u.EnableSessions(store, nil); err != nil {
				t.Fatal(err)
			}
			defer u.closeSession()
			if err := u.saveSession(false); err != nil {
				t.Fatal(err)
			}
			id := u.persistence.current.ID
			if err := store.Rename(id, "name-secret"); err != nil {
				t.Fatal(err)
			}
			metadata, err := store.LoadMetadata(id)
			want := "name-secret"
			if enabled {
				want = redaction.Marker
			}
			if err != nil || metadata.Name != want {
				t.Fatal("configured name redaction", metadata, err)
			}
		})
	}
}

func TestSessionPickerCleanupUsesResizedCommandView(t *testing.T) {
	_, entries, options := sessionPickerFixture(t)
	calls := 0
	options.size = func() (int, int) {
		calls++
		if calls == 1 {
			return 120, 18
		}
		return 20, 3
	}
	var output strings.Builder
	_, _, err := runSessionPicker(&sessionPickerKeys{keys: []string{"\x03"}}, &output, entries, options)
	if err != nil {
		t.Fatal(err)
	}
	final := output.String()[strings.LastIndex(output.String(), "\x1b[2;1H"):]
	if strings.Count(final, "\x1b[2K") != 3 || strings.Contains(final, "\x1b[5;1H") {
		t.Fatal("cleanup wrote outside resized command view", final)
	}
}
