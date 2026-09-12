package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"qcode/internal/question"
	"qcode/internal/session"
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
	Views        []RemoteAgentView     `json:"views"`
	Agents       []session.Summary     `json:"agents"`
	Interactions []session.Interaction `json:"interactions,omitempty"`
}

type RemoteService interface {
	Start(context.Context) (string, error)
	Stop() error
	Status() (bool, string, int)
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
	if len(fields) > 2 {
		u.printSystemMessage(yellow + "Usage: /remote [status|off]" + reset)
		return
	}
	if len(fields) == 2 && fields[1] == "off" {
		if err := u.remoteService.Stop(); err != nil {
			u.printSystemMessage(yellow + "Unable to stop remote control: " + sanitizeDiffLine(err.Error(), "<ESC>") + reset)
			return
		}
		u.printSystemMessage(green + "Remote control stopped." + reset)
		return
	}
	if len(fields) == 2 && fields[1] != "status" {
		u.printSystemMessage(yellow + "Usage: /remote [status|off]" + reset)
		return
	}
	if running, url, clients := u.remoteService.Status(); running {
		u.printSystemMessage(fmt.Sprintf("%sRemote control: %s · %d connected%s", green, sanitizeDiffLine(url, "<ESC>"), clients, reset))
		return
	} else if len(fields) == 2 {
		u.printSystemMessage(dim + "Remote control is off." + reset)
		return
	}
	url, err := u.remoteService.Start(ctx)
	if err != nil {
		u.printSystemMessage(yellow + "Unable to start remote control: " + sanitizeDiffLine(err.Error(), "<ESC>") + reset)
		return
	}
	u.printSystemMessage(green + "Remote control available at " + sanitizeDiffLine(url, "<ESC>") + reset)
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
	result := RemotePresentation{Sequence: sequence, Active: active, Agents: agents}
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
	// Clear any partially typed local draft before injecting one complete remote
	// command. interruptReader serializes injected batches from concurrent clients.
	u.input.inject(append([]byte{ctrlU}, []byte(line+"\r")...))
	return nil
}
