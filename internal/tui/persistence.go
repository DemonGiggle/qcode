package tui

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"qcode/internal/session"
)

type savedCell struct {
	Char  rune
	Style string
}
type savedHistory struct {
	Lines   []string
	Current []savedCell
	Cursor  int
	Pending []byte
	Style   string
	BaseID  uint64
}
type savedView struct {
	ID, Provider, Model string
	History             savedHistory
	Diffs               []string
	Buffer              string
	InFence, Thinking   bool
	Active              bool
	Table               []savedTableLine
	Browsing            bool
	AnchorLine          uint64
	AnchorColumn        int
	Unseen              bool
}
type savedTableLine struct {
	Text    string
	Newline bool
}
type savedPresentation struct {
	Active  string
	Drafts  map[string]string
	Verbose bool
	Views   []savedView
}
type sessionPersistence struct {
	mu          sync.Mutex
	store       *session.Store
	current     session.Snapshot
	lock        *os.File
	lastContent string
	lastError   string
	build       func(session.Snapshot) (*UI, error)
	requests    chan struct{}
}
type savedAgentController interface {
	SaveAgents() ([]session.SavedAgent, int)
}

func (u *UI) SetDetachedAgentManager(manager agentController) { u.manager = manager }

// EnableSessions installs persistence only for interactive, non-demo runs.
func (u *UI) EnableSessions(store *session.Store, build func(session.Snapshot) (*UI, error)) error {
	snap, lock, err := store.New()
	if err != nil {
		return err
	}
	u.persistence = &sessionPersistence{store: store, current: snap, lock: lock, build: build, requests: make(chan struct{}, 1)}
	return nil
}

func (u *UI) snapshotPresentation() savedPresentation {
	u.screenMu.Lock()
	s := savedPresentation{Active: u.activeAgent, Verbose: u.verbose, Drafts: map[string]string{}}
	for id, draft := range u.drafts {
		s.Drafts[id] = draft
	}
	var views []*agentView
	for _, v := range u.views {
		views = append(views, v)
	}
	u.screenMu.Unlock()
	for _, v := range views {
		// The renderer writes through screenMu, so acquire its lock first.
		v.response.stateMu.Lock()
		v.response.diffMu.Lock()
		u.screenMu.Lock()
		h := v.display.history
		h.mu.Lock()
		sv := savedView{ID: v.id, Provider: v.provider, Model: v.model, Unseen: v.unseen,
			Browsing: v.viewport.browsing, AnchorLine: v.viewport.anchor.line, AnchorColumn: v.viewport.anchor.column,
			Diffs: append([]string(nil), v.response.diffList...), Buffer: v.response.buffer.String(), InFence: v.response.inFence, Thinking: v.response.thinking,
			History: savedHistory{Lines: append([]string(nil), h.lines...), Cursor: h.cursor, Pending: append([]byte(nil), h.pending...), Style: h.style, BaseID: h.baseID}}
		for _, c := range h.current {
			sv.History.Current = append(sv.History.Current, savedCell{c.char, c.style})
		}
		sv.Active = v.response.active
		for _, line := range v.response.table {
			sv.Table = append(sv.Table, savedTableLine{line.text, line.newline})
		}
		h.mu.Unlock()
		u.screenMu.Unlock()
		v.response.diffMu.Unlock()
		v.response.stateMu.Unlock()
		s.Views = append(s.Views, sv)
	}
	return s
}

func (u *UI) RestorePresentation(data json.RawMessage) error {
	var s savedPresentation
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	if u.views[s.Active] == nil || len(s.Views) != len(u.views) {
		return fmt.Errorf("invalid saved tabs")
	}
	seen := map[string]bool{}
	for _, sv := range s.Views {
		v := u.views[sv.ID]
		if v == nil || seen[sv.ID] {
			return fmt.Errorf("invalid saved tab %q", sv.ID)
		}
		seen[sv.ID] = true
		if sv.History.Cursor < 0 || sv.History.Cursor > len(sv.History.Current) {
			return fmt.Errorf("invalid history cursor")
		}
		h := v.display.history
		h.lines = sv.History.Lines
		h.cursor = sv.History.Cursor
		h.pending = sv.History.Pending
		h.style = sv.History.Style
		h.baseID = sv.History.BaseID
		for _, c := range sv.History.Current {
			h.current = append(h.current, historyCell{c.Char, c.Style})
		}
		v.response.diffList = sv.Diffs
		v.response.buffer.WriteString(sv.Buffer)
		v.response.inFence, v.response.thinking, v.response.active = sv.InFence, sv.Thinking, sv.Active
		for _, line := range sv.Table {
			v.response.table = append(v.response.table, markdownTableLine{text: line.Text, newline: line.Newline})
		}
		v.unseen = sv.Unseen
		v.viewport = viewport{browsing: sv.Browsing, anchor: historyPosition{line: sv.AnchorLine, column: sv.AnchorColumn}}
	}
	u.activeAgent = s.Active
	u.drafts = s.Drafts
	if u.drafts == nil {
		u.drafts = map[string]string{}
	}
	u.verbose = s.Verbose
	for id, v := range u.views {
		if runner, ok := u.manager.Runner(id); ok {
			if r, ok := runner.(verboseRunner); ok {
				r.SetVerbose(s.Verbose)
			}
			if r, ok := runner.(interface{ RestoreWarnings() []string }); ok {
				for _, warning := range r.RestoreWarnings() {
					v.display.AddLine("Session restore: " + warning)
				}
			}
		}
	}
	return nil
}

func (u *UI) saveSession(left bool) error {
	p := u.persistence
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return u.saveSessionLocked(left)
}
func (u *UI) saveSessionLocked(left bool) error {
	p := u.persistence
	manager, ok := u.manager.(savedAgentController)
	if !ok {
		return nil
	}
	agents, next := manager.SaveAgents()
	// A launch containing only banners and commands is not a conversation.
	hasContent := false
	preview := p.current.Preview
	for _, a := range agents {
		var state struct {
			Messages []struct{ Role, Content string }
		}
		if err := json.Unmarshal(a.State, &state); err != nil {
			return err
		}
		for _, m := range state.Messages {
			if m.Role != "system" {
				hasContent = true
			}
			if a.Summary.ID == "main" && (m.Role == "user" || m.Role == "assistant") && strings.TrimSpace(m.Content) != "" {
				preview = m.Content
			}
		}
	}
	presentation := u.snapshotPresentation()
	if len(agents) > 1 {
		hasContent = true
	}
	for _, draft := range presentation.Drafts {
		if strings.TrimSpace(draft) != "" {
			hasContent = true
		}
	}
	for _, view := range presentation.Views {
		for _, line := range view.History.Lines {
			if strings.HasPrefix(line, "> ") && line != "> /quit" && line != "> /exit" {
				hasContent = true
			}
		}
	}
	if !hasContent && p.current.Saved.IsZero() {
		return nil
	}
	// Stable tab ordering also prevents spurious saves from map iteration order.
	var ordered []savedView
	for _, a := range agents {
		for _, v := range presentation.Views {
			if v.ID == a.Summary.ID {
				ordered = append(ordered, v)
				break
			}
		}
	}
	presentation.Views = ordered
	if len(ordered) != len(agents) {
		return nil
	} // A tab was removed during capture; retry next checkpoint.
	data, err := json.Marshal(presentation)
	if err != nil {
		return err
	}
	snap := p.current
	snap.Agents = agents
	snap.NextID = next
	snap.Presentation = data
	snap.Preview = preview
	snap.Saved = time.Time{}
	snap.Left = time.Time{}
	content, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	if !left && string(content) == p.lastContent {
		return nil
	}
	snap.Saved = time.Now().UTC()
	if left {
		snap.Left = snap.Saved
	}
	if err := p.store.Save(snap); err != nil {
		return err
	}
	p.current = snap
	p.lastContent = string(content)
	return nil
}

func (u *UI) reportSave(err error) {
	p := u.persistence
	if p == nil {
		return
	}
	p.mu.Lock()
	message := ""
	if err != nil {
		message = err.Error()
	}
	changed := message != p.lastError
	p.lastError = message
	p.mu.Unlock()
	if changed && message != "" {
		u.printSystemMessage(yellow + "Session save failed: " + message + reset)
	}
}

func (u *UI) watchSessions() func() {
	if u.persistence == nil {
		return func() {}
	}
	done, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				u.reportSave(u.saveSession(false))
			case <-u.persistence.requests:
				u.reportSave(u.saveSession(false))
			case <-done:
				return
			}
		}
	}()
	return func() { close(done); <-stopped }
}

func (u *UI) requestSessionSave() {
	if u.persistence != nil {
		select {
		case u.persistence.requests <- struct{}{}:
		default:
		}
	}
}

func (u *UI) closeSession() {
	if u.persistence == nil {
		return
	}
	u.reportSave(u.saveSession(true))
	session.Release(u.persistence.lock)
}

func relativeDeparture(t, now time.Time) string {
	d := now.Sub(t)
	if d < time.Minute {
		return "just now"
	}
	if d < time.Hour {
		return fmt.Sprintf("%d minutes ago", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%d hours ago", int(d.Hours()))
	}
	return fmt.Sprintf("%d days ago", int(d.Hours()/24))
}

func (u *UI) resumeSession() {
	p := u.persistence
	if p == nil {
		u.printSystemMessage("Session resume is unavailable.")
		return
	}
	for _, a := range u.manager.List() {
		if a.Status == session.StatusRunning || a.Status == session.StatusWaitingForApproval {
			u.printSystemMessage("Finish or cancel busy agent " + a.ID + " before resuming.")
			return
		}
	}
	entries, err := p.store.List()
	if err != nil {
		u.printSystemMessage("Cannot list sessions: " + err.Error())
		return
	}
	var choices []session.Entry
	p.mu.Lock()
	currentID := p.current.ID
	p.mu.Unlock()
	for _, e := range entries {
		if e.ID != currentID {
			choices = append(choices, e)
		}
	}
	if len(choices) == 0 {
		u.printSystemMessage("No saved sessions for this workspace.")
		return
	}
	u.input.setRaw(true)
	id, accepted, err := u.selectSession(choices)
	u.input.setRaw(false)
	u.repaintActive()
	if err != nil {
		u.printSystemMessage(err.Error())
		return
	}
	if !accepted {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	lock, err := p.store.Lock(id)
	if err != nil {
		u.printSystemMessage(err.Error())
		return
	}
	committed := false
	defer func() {
		if !committed {
			session.Release(lock)
		}
	}()
	snap, err := p.store.Load(id)
	if err != nil {
		u.printSystemMessage(err.Error())
		return
	}
	staged, err := p.build(snap)
	if err != nil {
		u.printSystemMessage("Cannot resume: " + err.Error())
		return
	}
	defer func() {
		if !committed {
			staged.manager.Shutdown()
		}
	}()
	if flusher, ok := u.manager.(interface{ FlushEvents() }); ok && u.agentEventsDone != nil {
		flusher.FlushEvents()
	}
	if err = u.saveSessionLocked(true); err != nil {
		u.printSystemMessage("Cannot save current session: " + err.Error())
		return
	}
	u.shutdownAgentManager()
	u.screenMu.Lock()
	staged.sessionHost = u
	u.views = staged.views
	u.activeAgent = staged.activeAgent
	u.drafts = staged.drafts
	u.verbose = staged.verbose
	for _, v := range u.views {
		v.display.ui = u
	}
	u.screenMu.Unlock()
	u.SetAgentManager(staged.manager)
	session.Release(p.lock)
	p.lock = lock
	p.current = snap
	p.current.Left = time.Time{}
	p.lastContent = ""
	committed = true
	u.activateRestoredTab()
}

func (u *UI) activateRestoredTab() {
	u.screenMu.Lock()
	v := u.views[u.activeAgent]
	u.display = v.display
	u.responseWriter = v.response
	u.provider = v.provider
	u.model = v.model
	u.onSkills = v.onSkills
	runner, _ := u.manager.Runner(u.activeAgent)
	u.runner = runner.(Runner)
	u.screenMu.Unlock()
	u.updateActiveCancellation()
	u.repaintActive()
	if draft := u.drafts[u.activeAgent]; draft != "" {
		u.input.inject([]byte(draft))
	}
}

func (u *UI) selectSession(entries []session.Entry) (string, bool, error) {
	selected := 0
	for {
		u.screenMu.Lock()
		if u.height < 7 || u.width < 20 {
			u.screenMu.Unlock()
			return "", false, fmt.Errorf("enlarge the terminal to at least 20 columns and 7 rows to select a session")
		}
		fmt.Fprint(u.out, "\x1b[s\x1b[2;1H")
		rows := max(1, (u.height-4)/3)
		start := selected / rows * rows
		fmt.Fprintf(u.out, "\x1b[2K%s\r\n", truncateDiffLine("Resume session | Up/Down, Enter, Esc", u.width, u.unicode))
		for row := 0; row < rows; row++ {
			i := start + row
			if i >= len(entries) {
				fmt.Fprint(u.out, "\x1b[2K\r\n\x1b[2K\r\n\x1b[2K\r\n")
				continue
			}
			e := entries[i]
			marker := "  "
			if i == selected {
				marker = "> "
			}
			busy := ""
			if e.Busy {
				busy = " (open elsewhere)"
			}
			if e.Problem != "" {
				busy = " (unavailable)"
			}
			fmt.Fprintf(u.out, "\x1b[2K%s\r\n", truncateDiffLine(marker+e.ID[:8]+"  "+relativeDeparture(session.Departure(e.Snapshot), time.Now())+busy, u.width, u.unicode))
			preview := strings.Join(strings.Fields(plainHistoryText(e.Preview)), " ")
			if e.Problem != "" {
				preview = e.Problem
			}
			preview = sanitizeDiffLine(preview, "<ESC>")
			lines := strings.Split(wrapANSI(preview, max(1, u.width-2), ""), "\n")
			for j := 0; j < 2; j++ {
				line := ""
				if j < len(lines) {
					line = lines[j]
				}
				fmt.Fprintf(u.out, "\x1b[2K  %s\r\n", line)
			}
		}
		fmt.Fprint(u.out, "\x1b[u")
		u.screenMu.Unlock()
		var b [1]byte
		if _, err := io.ReadFull(u.input, b[:]); err != nil {
			return "", false, err
		}
		switch b[0] {
		case '\r', '\n':
			if !entries[selected].Busy && entries[selected].Problem == "" {
				return entries[selected].ID, true, nil
			}
		case ctrlC:
			return "", false, nil
		case 27:
			// interruptReader already owns the byte stream. A short timeout makes
			// a lone Escape cancel without leaving a blocked reader behind.
			select {
			case next := <-u.input.data:
				if next != '[' {
					return "", false, nil
				}
				select {
				case key := <-u.input.data:
					if key == 'A' {
						selected = (selected + len(entries) - 1) % len(entries)
					} else if key == 'B' {
						selected = (selected + 1) % len(entries)
					}
				case <-time.After(80 * time.Millisecond):
					return "", false, nil
				}
			case <-time.After(80 * time.Millisecond):
				return "", false, nil
			}
		}
	}
}
