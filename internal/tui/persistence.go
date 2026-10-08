package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"qcode/internal/redaction"
	"qcode/internal/session"
)

type savedCell struct {
	Char  rune
	Style string
}
type savedHistory struct {
	Lines   []string
	Archive []string
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
	Active             string
	Drafts             map[string]string
	Verbose            bool
	Views              []savedView
	ConsultationCursor uint64
	SteeringCursor     uint64
}
type sessionPersistence struct {
	mu          sync.Mutex
	store       *session.Store
	current     session.Snapshot
	lastContent string
	lastError   string
	build       func(session.Snapshot) (*UI, error)
	buildFresh  func(session.SavedAgent) (*UI, error)
	requests    chan struct{}
}
type savedAgentController interface {
	SaveAgents() ([]session.SavedAgent, int)
}

func (u *UI) SetDetachedAgentManager(manager agentController) { u.manager = manager }

// EnableSessions installs persistence only for interactive, non-demo runs.
// The optional builder prepares a detached fresh UI from the main checkpoint.
func (u *UI) EnableSessions(store *session.Store, build func(session.Snapshot) (*UI, error), fresh ...func(session.SavedAgent) (*UI, error)) error {
	store.SetDefaultSnapshotFilter(func(snap session.Snapshot) (session.Snapshot, error) { return FilterSnapshot(u.redaction, snap) })
	store.SetDefaultNameFilter(func(name string) string { return u.redaction.Text(redaction.Persistence, name) })
	snap, err := store.New()
	if err != nil {
		return err
	}
	u.persistence = &sessionPersistence{store: store, current: snap, build: build, requests: make(chan struct{}, 1)}
	if len(fresh) > 0 {
		u.persistence.buildFresh = fresh[0]
	}
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
		if v.id == "main" {
			s.ConsultationCursor = u.consultationCursor
			s.SteeringCursor = u.steeringCursor
		}
		sv := savedView{ID: v.id, Provider: v.provider, Model: v.model, Unseen: v.unseen,
			Browsing: v.viewport.browsing, AnchorLine: v.viewport.anchor.line, AnchorColumn: v.viewport.anchor.column,
			Diffs: append([]string(nil), v.response.diffList...), Buffer: "", InFence: v.response.inFence, Thinking: v.response.thinking,
			History: savedHistory{Lines: append([]string(nil), h.lines...), Archive: append([]string(nil), h.archive...), Cursor: h.cursor, Pending: append([]byte(nil), h.pending...), Style: h.style, BaseID: h.baseID}}
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
		h.archive = append([]string(nil), sv.History.Archive...)
		if len(h.archive) == 0 {
			// Snapshots written before full-history export was introduced only have
			// the bounded repaint window available.
			h.archive = append([]string(nil), h.lines...)
		}
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
	u.consultationCursor = s.ConsultationCursor
	u.steeringCursor = s.SteeringCursor
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
	// Capture presentation first so its event cursor cannot be newer than the
	// journal. Manager identities and work must then be captured atomically.
	presentation := u.snapshotPresentation()
	var agents []session.SavedAgent
	var next int
	var work *session.WorkHistory
	if combined, ok := u.manager.(interface {
		SaveSessionState() ([]session.SavedAgent, int, *session.WorkHistory)
	}); ok {
		agents, next, work = combined.SaveSessionState()
	} else {
		agents, next = manager.SaveAgents()
		if history, ok := u.manager.(interface{ SaveWorkHistory() *session.WorkHistory }); ok {
			work = history.SaveWorkHistory()
		}
	}
	// A launch containing only banners and commands is not a conversation.
	hasContent := work != nil && (len(work.Records) > 0 || len(work.Events) > 0)
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
	if !hasContent {
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
	snap.Work = work
	snap.Preview = preview
	snap.Recency = session.ResumeTime(snap)
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

func (u *UI) recordSessionPrompt() {
	p := u.persistence
	if p == nil {
		return
	}
	p.mu.Lock()
	p.current.Recency = time.Now().UTC()
	p.mu.Unlock()
	u.requestSessionSave()
}

func (u *UI) closeSession() {
	if u.persistence == nil {
		return
	}
	u.reportSave(u.saveSession(true))
}

func (u *UI) resumeSession() {
	u.resumeSessionID("")
}

// listSessionsLocked keeps every saved session in the store's order and marks
// the session currently displayed in this process.
func (p *sessionPersistence) listSessionsLocked() ([]session.Entry, error) {
	entries, err := p.store.List()
	if err != nil {
		return nil, err
	}
	for i := range entries {
		if entries[i].ID == p.current.ID {
			entries[i].Current = true
		}
	}
	return entries, nil
}

func (u *UI) resumeSessionID(requestedID string) {
	p := u.persistence
	if p == nil {
		u.printSystemMessage("Session resume is unavailable.")
		return
	}
	p.mu.Lock()
	currentID := p.current.ID
	p.mu.Unlock()
	if requestedID == currentID {
		return
	}
	p.mu.Lock()
	choices, err := p.listSessionsLocked()
	p.mu.Unlock()
	if err != nil {
		u.printSystemMessage("Cannot list sessions: " + err.Error())
		return
	}
	if len(choices) == 0 {
		u.printSystemMessage("No saved sessions for this workspace.")
		return
	}
	id := requestedID
	if id == "" {
		u.input.setRaw(true)
		u.beginRawSelector()
		var accepted bool
		id, accepted, err = u.pickSession(choices, u.restoreSession)
		u.input.setRaw(false)
		u.endRawSelector()
		u.repaintActive()
		if err != nil {
			u.printSystemMessage(err.Error())
			return
		}
		if !accepted {
			return
		}
		p.mu.Lock()
		id = p.current.ID
		p.mu.Unlock()
		if id == currentID {
			return
		}
	} else {
		if err := u.restoreSession(id); err != nil {
			u.printSystemMessage("Cannot resume: " + err.Error())
			return
		}
	}
	u.activateRestoredTab()
	if _, err := p.store.LoadMetadata(id); err != nil {
		u.printSystemMessage("Session metadata: " + err.Error())
	}
}

// restoreSession stages and validates before replacing the current interface.
// Picker callers can report any failure inline and leave the selection open.
func (u *UI) restoreSession(id string) error {
	p := u.persistence
	p.mu.Lock()
	defer p.mu.Unlock()
	if id == p.current.ID {
		return nil
	}
	for _, a := range u.manager.List() {
		if a.Status == session.StatusRunning || a.Status == session.StatusWaitingForApproval {
			return fmt.Errorf("finish or cancel busy agent %s before resuming", a.ID)
		}
	}
	snap, err := p.store.LoadForResume(id)
	if err != nil {
		return err
	}
	if p.build == nil {
		return fmt.Errorf("session restore is unavailable")
	}
	staged, err := p.build(snap)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			staged.manager.Shutdown()
		}
	}()
	if flusher, ok := u.manager.(interface{ FlushEvents() }); ok && u.agentEventsDone != nil {
		flusher.FlushEvents()
	}
	if err = u.saveSessionLocked(true); err != nil {
		return fmt.Errorf("cannot save current session: %w", err)
	}
	u.installSessionLocked(staged, snap)
	committed = true
	return nil
}

// deleteSession serializes autosaves with deletion and the current UI handoff.
// It reports whether the picker should close on a new, empty conversation.
func (u *UI) deleteSession(id string) (bool, error) {
	p := u.persistence
	p.mu.Lock()
	defer p.mu.Unlock()
	if id != p.current.ID {
		return false, p.store.Delete(id)
	}
	return u.deleteCurrentSessionLocked(func() error { return p.store.Delete(id) })
}

// deleteAllSessions ignores the picker filter and keeps the current snapshot
// until every other deletion succeeds, so a partial failure retains its UI.
func (u *UI) deleteAllSessions() (bool, error) {
	p := u.persistence
	p.mu.Lock()
	defer p.mu.Unlock()
	entries, err := p.listSessionsLocked()
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if entry.Current {
			return u.deleteCurrentSessionLocked(func() error {
				if err := p.store.DeleteAll(p.current.ID); err != nil {
					return err
				}
				return p.store.Delete(p.current.ID)
			})
		}
	}
	return false, p.store.DeleteAll()
}

// deleteCurrentSessionLocked prepares a fresh conversation before deleting
// saved files. Callers hold the persistence mutex throughout the transaction.
func (u *UI) deleteCurrentSessionLocked(remove func() error) (bool, error) {
	p := u.persistence
	for _, a := range u.manager.List() {
		if a.Status == session.StatusRunning || a.Status == session.StatusWaitingForApproval {
			return false, fmt.Errorf("finish or cancel busy agent %s before deleting the current session", a.ID)
		}
	}
	if p.buildFresh == nil {
		return false, fmt.Errorf("fresh session creation is unavailable")
	}
	manager, ok := u.manager.(savedAgentController)
	if !ok {
		return false, fmt.Errorf("main agent checkpoint is unavailable")
	}
	agents, _ := manager.SaveAgents()
	var main session.SavedAgent
	for _, a := range agents {
		if a.Summary.ID == "main" {
			main = a
			break
		}
	}
	if len(main.State) == 0 {
		return false, fmt.Errorf("main agent checkpoint is unavailable")
	}
	snap, err := p.store.New()
	if err != nil {
		return false, err
	}
	staged, err := p.buildFresh(main)
	committed := false
	defer func() {
		if !committed && staged != nil && staged.manager != nil {
			staged.manager.Shutdown()
		}
	}()
	if err != nil {
		return false, fmt.Errorf("cannot prepare fresh session: %w", err)
	}
	if staged == nil || staged.manager == nil || len(staged.views) != 1 || staged.views["main"] == nil || staged.activeAgent != "main" {
		return false, fmt.Errorf("invalid fresh session interface")
	}
	if err := remove(); err != nil {
		return false, err
	}
	staged.verbose = u.VerboseEnabled()
	u.installSessionLocked(staged, snap)
	u.screenMu.Lock()
	u.queue = queuePanel{}
	u.deferredInteractions = make(map[string]bool)
	u.observedTaskID = ""
	u.screenMu.Unlock()
	u.tabMu.Lock()
	u.pendingTab = 0
	u.tabMu.Unlock()
	committed = true
	return true, nil
}

// installSessionLocked shares the validated detached-manager handoff. Callers
// hold the persistence mutex and decide whether to save the departing session.
func (u *UI) installSessionLocked(staged *UI, snap session.Snapshot) {
	p := u.persistence
	u.shutdownAgentManager()
	u.screenMu.Lock()
	staged.sessionHost = u
	u.views = staged.views
	u.activeAgent = staged.activeAgent
	u.drafts = staged.drafts
	u.verbose = staged.verbose
	u.consultationCursor = staged.consultationCursor
	u.steeringCursor = staged.steeringCursor
	for _, v := range u.views {
		v.display.ui = u
		v.display.history.onChange = u.signalPresentation
	}
	u.screenMu.Unlock()
	u.SetAgentManager(staged.manager)
	u.signalPresentation()
	p.current = snap
	p.current.Recency = session.ResumeTime(snap)
	p.current.Left = time.Time{}
	p.lastContent = ""
	p.lastError = ""
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
	u.screenMu.Unlock()
	u.SetRunner(runner.(Runner))
	u.updateActiveCancellation()
	u.repaintActive()
	if draft := u.drafts[u.activeAgent]; draft != "" {
		u.input.inject([]byte(draft))
	}
}
