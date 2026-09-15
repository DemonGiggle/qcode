package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"qcode/internal/prompt"
	"qcode/internal/question"
	"qcode/internal/session"
)

const (
	webStatusModelColor     = "\x1b[38;2;224;156;255m"
	webStatusWorkspaceColor = "\x1b[38;2;128;184;255m"
)

// RemoteAgentView is a transport-safe snapshot of one agent tab.
type RemoteAgentView struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Provider string   `json:"provider,omitempty"`
	Model    string   `json:"model,omitempty"`
	Status   string   `json:"status"`
	Lines    []string `json:"lines"`
}

type RemotePresentation struct {
	Sequence     uint64                `json:"sequence"`
	Active       string                `json:"active"`
	StatusBar    string                `json:"status_bar"`
	Views        []RemoteAgentView     `json:"views"`
	Agents       []session.Summary     `json:"agents"`
	Interactions []session.Interaction `json:"interactions,omitempty"`
}

// RemoteCatalog contains the read-only selector data needed by the browser UI.
// It deliberately mirrors existing TUI runtime state without changing it.
type RemoteCatalog struct {
	Models   []string                       `json:"models"`
	Thinking map[string]RemoteThinkingState `json:"thinking"`
	Tools    []RemoteToolState              `json:"tools"`
	Skills   []RemoteSkillState             `json:"skills"`
	Sessions []RemoteSessionState           `json:"sessions"`
}

type RemoteToolState struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

// RemoteThinkingState contains the selectable thinking levels for one model.
// Models without an adjustable thinking capability are omitted.
type RemoteThinkingState struct {
	Levels  []string `json:"levels"`
	Current string   `json:"current,omitempty"`
}

type RemoteSkillState struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Selected    bool   `json:"selected"`
}

// RemoteSessionState is the resumable subset of a saved session entry. The
// current session, busy sessions, and unreadable snapshots are intentionally
// omitted by RemoteCatalog.
type RemoteSessionState struct {
	ID         string    `json:"id"`
	Preview    string    `json:"preview"`
	Created    time.Time `json:"created,omitempty"`
	Saved      time.Time `json:"saved,omitempty"`
	Left       time.Time `json:"left,omitempty"`
	AgentCount int       `json:"agent_count"`
}

type RemoteLogin struct {
	URL        string
	ExpiresAt  time.Time
	OpenAccess bool
}

// RemoteMode selects how the browser-facing listener is exposed.
type RemoteMode string

const (
	RemoteModePureWeb     RemoteMode = "pure-web"
	RemoteModePureWebOpen RemoteMode = "pure-web-open"
	RemoteModeTailscale   RemoteMode = "tailscale"
)

// RemoteStatus is the current browser remote-control lifecycle state.
type RemoteStatus struct {
	Running     bool
	Mode        RemoteMode
	URL         string
	Connections int
}

// RemoteNetwork is a LAN IPv4 address available for a Pure Web listener.
type RemoteNetwork struct {
	Name    string
	Address string
	Subnet  string
}

type RemoteService interface {
	Networks() ([]RemoteNetwork, error)
	Start(context.Context, RemoteMode, string) (RemoteStatus, error)
	IssueLogin(context.Context) (RemoteLogin, error)
	LoginState() string
	Stop() error
	Status() RemoteStatus
}

type interactionController interface {
	BeginInteraction(session.Interaction) (session.InteractionWaiter, error)
	ResolveInteraction(session.Resolution) error
}

func (u *UI) SetRemoteService(service RemoteService) { u.remoteService = service }

func (u *UI) handleRemoteCommand(ctx context.Context, fields []string) {
	if u.remoteService == nil {
		u.printSystemMessage(yellow + "Remote control is unavailable." + reset)
		return
	}
	if len(fields) != 1 {
		u.printSystemMessage(yellow + "Usage: /remote" + reset)
		return
	}
	status := u.remoteService.Status()
	if !status.Running {
		mode, accepted, err := u.selectRemoteMode()
		if err != nil {
			u.printSystemMessage(yellow + "Unable to choose remote mode: " + sanitizeDiffLine(err.Error(), "<ESC>") + reset)
			return
		}
		if !accepted {
			return
		}
		address := ""
		if mode != RemoteModeTailscale {
			networks, networkErr := u.remoteService.Networks()
			if networkErr != nil {
				u.printSystemMessage(yellow + "Unable to list LAN interfaces: " + sanitizeDiffLine(networkErr.Error(), "<ESC>") + reset)
				return
			}
			if len(networks) == 0 {
				u.printSystemMessage(yellow + "Pure Web needs an active LAN IPv4 address." + reset)
				return
			}
			if len(networks) == 1 {
				address = networks[0].Address
			} else {
				selected, selectedNetwork, selectErr := u.selectRemoteNetwork(networks)
				if selectErr != nil {
					u.printSystemMessage(yellow + "Unable to choose LAN interface: " + sanitizeDiffLine(selectErr.Error(), "<ESC>") + reset)
					return
				}
				if !selected {
					return
				}
				address = selectedNetwork.Address
			}
		}
		status, err = u.remoteService.Start(ctx, mode, address)
		if err != nil {
			u.printSystemMessage(yellow + "Unable to start remote control: " + sanitizeDiffLine(err.Error(), "<ESC>") + reset)
			return
		}
		u.drawStatusBar()
	}
	login, err := u.remoteService.IssueLogin(ctx)
	if err != nil {
		u.printSystemMessage(yellow + "Unable to issue remote login link." + reset)
		return
	}
	u.showRemoteLogin(login)
	if err := u.showRemoteActive(status); err != nil {
		u.printSystemMessage(yellow + "Remote control screen failed: " + sanitizeDiffLine(err.Error(), "<ESC>") + reset)
	}
}

func (u *UI) RemotePresentation() RemotePresentation {
	summaries := map[string]session.Summary{}
	var agents []session.Summary
	if u.manager != nil {
		agents = u.manager.List()
		for _, item := range agents {
			summaries[item.ID] = item
		}
	}
	u.presentationMu.Lock()
	sequence := u.presentationSequence
	u.presentationMu.Unlock()
	u.screenMu.Lock()
	active := u.activeAgent
	type viewSnapshot struct {
		id, provider, model string
		history             *historyWriter
	}
	views := make([]viewSnapshot, 0, len(u.views))
	for _, view := range u.views {
		views = append(views, viewSnapshot{id: view.id, provider: view.provider, model: view.model, history: view.display.history})
	}
	u.screenMu.Unlock()
	sort.Slice(views, func(i, j int) bool { return views[i].id < views[j].id })
	result := RemotePresentation{Sequence: sequence, Active: active, StatusBar: u.remoteStatusBar(), Agents: agents}
	if source, ok := u.manager.(interface{ PendingInteractions() []session.Interaction }); ok {
		result.Interactions = source.PendingInteractions()
	}
	for _, view := range views {
		summary := summaries[view.id]
		name := summary.Name
		if name == "" {
			name = view.id
		}
		snapshot := view.history.Snapshot()
		lines := make([]string, 0, len(snapshot.lines))
		for _, line := range snapshot.lines {
			lines = append(lines, line.text)
		}
		result.Views = append(result.Views, RemoteAgentView{
			ID: view.id, Name: name, Provider: view.provider, Model: view.model,
			Status: string(summary.Status), Lines: lines,
		})
	}
	return result
}

func (u *UI) RemoteCatalog(ctx context.Context) RemoteCatalog {
	result := RemoteCatalog{
		Models:   make([]string, 0),
		Thinking: make(map[string]RemoteThinkingState),
		Tools:    make([]RemoteToolState, 0),
		Skills:   make([]RemoteSkillState, 0),
		Sessions: make([]RemoteSessionState, 0),
	}
	u.screenMu.Lock()
	runner := u.runner
	catalogLoader := u.skillCatalogLoader
	skillSummaries := append([]prompt.SkillSummary(nil), u.skills...)
	currentModel := u.model
	u.screenMu.Unlock()
	if catalogLoader != nil {
		if loaded, err := catalogLoader(); err == nil {
			skillSummaries = loaded
		}
	}
	selectedSkills := map[string]bool{}
	if selectedRunner, ok := runner.(selectedSkillsRunner); ok {
		for _, skill := range selectedRunner.SelectedSkills() {
			selectedSkills[skill.Name] = true
		}
	}
	for _, skill := range skillSummaries {
		result.Skills = append(result.Skills, RemoteSkillState{
			Name: skill.Name, Description: skill.Description, Selected: selectedSkills[skill.Name],
		})
	}
	if modelRunner, ok := runner.(modelRunner); ok {
		if models, err := modelRunner.ListModels(ctx); err == nil {
			result.Models = append(result.Models, models...)
			if thinkingRunner, ok := runner.(thinkingRunner); ok {
				for _, model := range models {
					capability := thinkingRunner.ThinkingCapabilityFor(model)
					if !capability.Adjustable || len(capability.Levels) == 0 {
						continue
					}
					state := RemoteThinkingState{Levels: append([]string(nil), capability.Levels...)}
					if model == currentModel {
						state.Current = thinkingRunner.ThinkingLevel()
					}
					result.Thinking[model] = state
				}
			}
		}
	}
	if runner, ok := runner.(toolRunner); ok {
		for _, name := range runner.ToolNames() {
			result.Tools = append(result.Tools, RemoteToolState{Name: name, Enabled: runner.ToolEnabled(name)})
		}
	}
	result.Sessions = u.remoteSessions()
	return result
}

func (u *UI) remoteSessions() []RemoteSessionState {
	p := u.persistence
	if p == nil {
		return []RemoteSessionState{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.store == nil {
		return []RemoteSessionState{}
	}
	entries, err := p.store.List()
	if err != nil {
		return []RemoteSessionState{}
	}
	result := make([]RemoteSessionState, 0, len(entries))
	for _, entry := range entries {
		if entry.ID == p.current.ID || entry.Busy || entry.Problem != "" {
			continue
		}
		result = append(result, RemoteSessionState{
			ID: entry.ID, Preview: remoteSessionPreview(entry.Preview),
			Created: entry.Created, Saved: entry.Saved, Left: entry.Left,
			AgentCount: len(entry.Agents),
		})
	}
	return result
}

func remoteSessionPreview(preview string) string {
	preview = strings.Join(strings.Fields(plainHistoryText(preview)), " ")
	if preview == "" {
		return "Untitled session"
	}
	runes := []rune(preview)
	if len(runes) <= 256 {
		return preview
	}
	return string(runes[:253]) + "..."
}

// remoteStatusBar uses the same formatter as the terminal footer, but always
// includes ANSI styling so the browser can faithfully render TUI categories.
func (u *UI) remoteStatusBar() string {
	remote := false
	if u.remoteService != nil {
		remote = u.remoteService.Status().Running
	}
	return statusBarWithRemoteColors(u.provider, u.model, displayRoot(u.root), u.width, u.unicode, true, remote, webStatusModelColor, webStatusWorkspaceColor, u.contextLabel(), u.usageLabel(), u.stepsLabel(), u.modeLabel(), u.thinkingLabel())
}

func (u *UI) SubscribePresentation(ctx context.Context) <-chan struct{} {
	ch := make(chan struct{}, 1)
	u.presentationMu.Lock()
	u.nextPresentationSub++
	id := u.nextPresentationSub
	u.presentationSubs[id] = ch
	u.presentationMu.Unlock()
	go func() {
		<-ctx.Done()
		u.presentationMu.Lock()
		if current, ok := u.presentationSubs[id]; ok {
			delete(u.presentationSubs, id)
			close(current)
		}
		u.presentationMu.Unlock()
	}()
	return ch
}

func (u *UI) signalPresentation() {
	u.presentationMu.Lock()
	defer u.presentationMu.Unlock()
	u.presentationSequence++
	for _, ch := range u.presentationSubs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (u *UI) ResolveRemoteInteraction(actor, id string, value []byte) error {
	controller, ok := u.manager.(interactionController)
	if !ok {
		return fmt.Errorf("shared interactions are unavailable")
	}
	var target *session.Interaction
	if source, ok := u.manager.(interface{ PendingInteractions() []session.Interaction }); ok {
		for _, item := range source.PendingInteractions() {
			if item.ID == id {
				copy := item
				target = &copy
				break
			}
		}
	}
	if target == nil {
		return fmt.Errorf("interaction not found")
	}
	switch target.Kind {
	case session.InteractionQuestions:
		var questions []question.Question
		var answers []string
		if json.Unmarshal(target.Payload, &questions) != nil || json.Unmarshal(value, &answers) != nil || len(questions) != len(answers) {
			return fmt.Errorf("remote answer count does not match questions")
		}
		for i, item := range questions {
			normalized, valid := normalizeQuestionAnswerWithCustom(answers[i], item.Options, item.AllowCustom)
			if !valid {
				return fmt.Errorf("invalid answer for question %d", i+1)
			}
			answers[i] = normalized
		}
		value, _ = json.Marshal(answers)
	case session.InteractionDirectoryApproval:
		var request struct {
			Requested string `json:"requested"`
			Proposed  string `json:"proposed"`
		}
		var answer struct {
			Selected string `json:"selected"`
			Approved bool   `json:"approved"`
		}
		if json.Unmarshal(target.Payload, &request) != nil || json.Unmarshal(value, &answer) != nil {
			return fmt.Errorf("invalid directory approval")
		}
		if answer.Approved {
			if strings.TrimSpace(answer.Selected) == "" {
				answer.Selected = request.Proposed
			}
			selected := filepath.Clean(answer.Selected)
			if selected != filepath.Clean(request.Proposed) && selected != filepath.Clean(request.Requested) {
				return fmt.Errorf("remote approval must use the requested or proposed path")
			}
			answer.Selected = selected
		}
		value, _ = json.Marshal(answer)
	case session.InteractionPlanDecision:
		var decision string
		if json.Unmarshal(value, &decision) != nil || decision != "implement" && decision != "stay" {
			return fmt.Errorf("invalid plan decision")
		}
	case session.InteractionLearningApproval:
		var approved bool
		if json.Unmarshal(value, &approved) != nil {
			return fmt.Errorf("invalid learning approval")
		}
	default:
		return fmt.Errorf("unsupported interaction kind %q", target.Kind)
	}
	err := controller.ResolveInteraction(session.Resolution{InteractionID: id, Value: value, ResolvedBy: actor})
	if err == nil {
		u.signalPresentation()
	}
	return err
}

var remoteLineBreak = regexp.MustCompile(`[\r\n]`)

// SubmitRemote sends browser input through the existing command loop, so the
// local terminal observes and executes the same operation.
func (u *UI) SubmitRemote(actor, line string) error {
	line = strings.TrimSpace(remoteLineBreak.ReplaceAllString(line, " "))
	if line == "" {
		return fmt.Errorf("command must not be empty")
	}
	if line == "/exit" || line == "/quit" || strings.HasPrefix(line, "/remote") {
		return fmt.Errorf("that command is restricted to the local terminal")
	}
	u.printSystemMessage(dim + "Remote " + sanitizeDiffLine(actor, "<ESC>") + ": " + sanitizeDiffLine(line, "<ESC>") + reset)
	u.dismissRemoteMenuForInput()
	// Clear any partially typed local draft before injecting one complete remote
	// command. interruptReader serializes injected batches from concurrent clients.
	u.input.inject(append([]byte{ctrlU}, []byte(line+"\r")...))
	return nil
}

// RemoteConnection records browser connection lifecycle events only when
// verbose tracing is enabled. The event is written through the active display
// so it remains visible in the terminal and in the remote presentation.
func (u *UI) RemoteConnection(actor string, connected bool) {
	u.screenMu.Lock()
	verbose := u.verbose
	u.screenMu.Unlock()
	if !verbose {
		return
	}
	event := "disconnect"
	if connected {
		event = "connect"
	}
	u.printSystemMessage(dim + "Remote " + event + ": " + sanitizeDiffLine(actor, "<ESC>") + reset)
}

// RemoteRequestRejected records a remote request that failed before reaching a
// route. It is useful for diagnosing missing Tailscale identity headers and
// cross-origin browser configuration while verbose tracing is enabled.
func (u *UI) RemoteRequestRejected(method, path, reason string) {
	u.screenMu.Lock()
	verbose := u.verbose
	u.screenMu.Unlock()
	if !verbose {
		return
	}
	u.printSystemMessage(dim + "Remote reject: " + sanitizeDiffLine(reason, "<ESC>") + " (" + sanitizeDiffLine(method, "<ESC>") + " " + sanitizeDiffLine(path, "<ESC>") + ")" + reset)
}
