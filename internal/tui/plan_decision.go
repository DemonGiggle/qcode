package tui

import (
	"context"
	"encoding/json"
	"errors"

	"qcode/internal/question"
	"qcode/internal/session"
)

type modeDecisionRequest struct {
	agentID  string
	artifact string
	kind     session.InteractionKind
	waiter   session.InteractionWaiter
}

type planDecisionRunner interface {
	TakePlanDecision() (string, bool)
}

type skillPlanDecisionRunner interface {
	TakeSkillPlanDecision() (string, bool)
}

var planDecisionOptions = []string{
	"Start implementation (same as /plan act)",
	"Stay in Plan mode",
}

var skillPlanDecisionOptions = []string{
	"Create skill (same as /skillplan create)",
	"Stay in Skill Plan mode",
}

// queueModeDecision records a completed plan or skill draft for the UI
// goroutine to present. The event watcher must not read from the terminal.
func (u *UI) queueModeDecision(id string) {
	if u.manager == nil {
		return
	}
	value, ok := u.manager.Runner(id)
	if !ok {
		return
	}

	var request *modeDecisionRequest
	if controller, ok := value.(planController); ok && controller.PlanMode() {
		reader, ok := value.(planDecisionRunner)
		if !ok {
			return
		}
		plan, ready := reader.TakePlanDecision()
		if !ready {
			return
		}
		request = &modeDecisionRequest{agentID: id, artifact: plan, kind: session.InteractionPlanDecision}
	} else if controller, ok := value.(skillPlanController); ok && controller.SkillPlanMode() {
		reader, ok := value.(skillPlanDecisionRunner)
		if !ok {
			return
		}
		draft, ready := reader.TakeSkillPlanDecision()
		if !ready {
			return
		}
		request = &modeDecisionRequest{agentID: id, artifact: draft, kind: session.InteractionSkillPlanDecision}
	}
	if request == nil {
		return
	}

	if broker, ok := u.manager.(interactionController); ok {
		payloadKey := "plan"
		if request.kind == session.InteractionSkillPlanDecision {
			payloadKey = "draft"
		}
		payload, _ := json.Marshal(map[string]string{payloadKey: request.artifact})
		waiter, err := broker.BeginInteraction(session.Interaction{AgentID: id, Kind: request.kind, Payload: payload})
		if err == nil {
			request.waiter = waiter
			u.signalPresentation()
		}
	}
	u.modeDecisionMu.Lock()
	u.modeDecisions = append(u.modeDecisions, request)
	u.modeDecisionMu.Unlock()
	u.signalUIEvent()
	u.screenMu.Lock()
	active := u.activeAgent == id
	u.screenMu.Unlock()
	if active && u.input != nil {
		u.input.interruptLine()
	}
}

// handlePendingModeDecision is called only by the UI goroutine, so all
// terminal reads remain serialized with the line editor.
func (u *UI) handlePendingModeDecision(ctx context.Context) {
	u.screenMu.Lock()
	activeID := u.activeAgent
	manager := u.manager
	u.screenMu.Unlock()
	if manager == nil {
		return
	}
	u.modeDecisionMu.Lock()
	var request *modeDecisionRequest
	for i, candidate := range u.modeDecisions {
		if candidate.agentID != activeID {
			continue
		}
		request = candidate
		u.modeDecisions = append(u.modeDecisions[:i], u.modeDecisions[i+1:]...)
		break
	}
	u.modeDecisionMu.Unlock()
	if request == nil {
		return
	}

	value, ok := manager.Runner(activeID)
	if !ok {
		return
	}
	var options []string
	var promptText, cancelMessage, action string
	switch request.kind {
	case session.InteractionPlanDecision:
		controller, ok := value.(planController)
		if !ok || !controller.PlanMode() {
			return
		}
		if _, exists := controller.LatestPlanText(); !exists {
			return
		}
		options = planDecisionOptions
		promptText = "The plan is ready. What would you like to do? Use /plan show after staying in Plan mode if you want to review it first."
		cancelMessage = "Plan decision cancelled; staying in Plan mode."
		action = "implement"
	case session.InteractionSkillPlanDecision:
		controller, ok := value.(skillPlanController)
		if !ok || !controller.SkillPlanMode() {
			return
		}
		if _, exists := controller.LatestSkillDraftText(); !exists {
			return
		}
		options = skillPlanDecisionOptions
		promptText = "The skill draft is ready. Create it now, or stay in Skill Plan mode to review it with /skillplan show or request refinements?"
		cancelMessage = "Skill draft decision cancelled; staying in Skill Plan mode."
		action = "create"
	default:
		return
	}

	questions := []question.Question{{Text: promptText, Options: options}}
	var decision string
	var err error
	if request.waiter == nil {
		var answers []string
		answers, err = u.runQuestionnaireWithFooter(ctx, questions, "Ctrl+C keeps the current mode active.")
		if len(answers) > 0 {
			decision = "stay"
			if answers[0] == options[0] {
				decision = action
			}
		}
	} else {
		localCtx, cancelLocal := context.WithCancel(ctx)
		defer cancelLocal()
		brokerCtx, cancelBroker := context.WithCancel(ctx)
		defer cancelBroker()
		type localAnswer struct {
			answers []string
			err     error
		}
		localResult := make(chan localAnswer, 1)
		go func() {
			answers, err := u.runQuestionnaireWithFooter(localCtx, questions, "Ctrl+C keeps the current mode active.")
			localResult <- localAnswer{answers, err}
		}()
		type remoteAnswer struct {
			resolution session.Resolution
			err        error
		}
		remoteResult := make(chan remoteAnswer, 1)
		go func() {
			resolution, err := request.waiter.Wait(brokerCtx)
			remoteResult <- remoteAnswer{resolution, err}
		}()
		select {
		case local := <-localResult:
			err = local.err
			if len(local.answers) > 0 {
				decision = "stay"
				if local.answers[0] == options[0] {
					decision = action
				}
				value, _ := json.Marshal(decision)
				if broker, ok := u.manager.(interactionController); ok {
					_ = broker.ResolveInteraction(session.Resolution{InteractionID: request.waiter.InteractionInfo().ID, Value: value, ResolvedBy: "local-tui"})
					u.signalPresentation()
				}
				resolved := <-remoteResult
				if resolved.err == nil {
					_ = json.Unmarshal(resolved.resolution.Value, &decision)
				}
			}
		case remote := <-remoteResult:
			cancelLocal()
			<-localResult
			err = remote.err
			if err == nil {
				err = json.Unmarshal(remote.resolution.Value, &decision)
			}
		}
	}
	if err != nil {
		if errors.Is(err, context.Canceled) && ctx.Err() == nil {
			u.printSystemMessage(dim + cancelMessage + reset)
		}
		return
	}
	if decision == "" {
		return
	}
	switch request.kind {
	case session.InteractionPlanDecision:
		switch decision {
		case "implement", planDecisionOptions[0]:
			u.handlePlanCommand(ctx, []string{"/plan", "act"})
		case "stay", planDecisionOptions[1]:
			u.printSystemMessage(green + "Plan saved; staying in Plan mode." + reset)
		}
	case session.InteractionSkillPlanDecision:
		switch decision {
		case "create", skillPlanDecisionOptions[0]:
			u.handleSkillPlanCommand(ctx, []string{"/skillplan", "create"})
		case "stay", skillPlanDecisionOptions[1]:
			u.printSystemMessage(green + "Skill draft saved; staying in Skill Plan mode." + reset)
		}
	}
}
