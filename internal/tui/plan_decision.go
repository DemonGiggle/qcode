package tui

import (
	"context"
	"encoding/json"
	"errors"

	"qcode/internal/question"
	"qcode/internal/session"
)

type planDecisionRequest struct {
	agentID string
	plan    string
	waiter  session.InteractionWaiter
}

type planDecisionRunner interface {
	TakePlanDecision() (string, bool)
}

var planDecisionOptions = []string{
	"Start implementation (same as /plan act)",
	"Stay in Plan mode",
}

// queuePlanDecision records a completed plan for the UI goroutine to present.
// The event watcher must not read from the terminal itself.
func (u *UI) queuePlanDecision(id string) {
	if u.manager == nil {
		return
	}
	value, ok := u.manager.Runner(id)
	if !ok {
		return
	}
	controller, ok := value.(planController)
	if !ok || !controller.PlanMode() {
		return
	}
	reader, ok := value.(planDecisionRunner)
	if !ok {
		return
	}
	plan, ready := reader.TakePlanDecision()
	if !ready {
		return
	}
	request := &planDecisionRequest{agentID: id, plan: plan}
	if broker, ok := u.manager.(interactionController); ok {
		payload, _ := json.Marshal(map[string]string{"plan": plan})
		waiter, err := broker.BeginInteraction(session.Interaction{AgentID: id, Kind: session.InteractionPlanDecision, Payload: payload})
		if err == nil {
			request.waiter = waiter
			u.signalPresentation()
		}
	}
	u.planDecisionMu.Lock()
	u.planDecisions = append(u.planDecisions, request)
	u.planDecisionMu.Unlock()
	u.signalUIEvent()
	u.screenMu.Lock()
	active := u.activeAgent == id
	u.screenMu.Unlock()
	if active && u.input != nil {
		u.input.interruptLine()
	}
}

// handlePendingPlanDecision is called only by the UI goroutine, so all
// terminal reads remain serialized with the line editor.
func (u *UI) handlePendingPlanDecision(ctx context.Context) {
	u.screenMu.Lock()
	activeID := u.activeAgent
	manager := u.manager
	u.screenMu.Unlock()
	if manager == nil {
		return
	}
	u.planDecisionMu.Lock()
	var request *planDecisionRequest
	for i, candidate := range u.planDecisions {
		if candidate.agentID != activeID {
			continue
		}
		request = candidate
		u.planDecisions = append(u.planDecisions[:i], u.planDecisions[i+1:]...)
		break
	}
	u.planDecisionMu.Unlock()
	if request == nil {
		return
	}

	value, ok := manager.Runner(activeID)
	if !ok {
		return
	}
	controller, ok := value.(planController)
	if !ok || !controller.PlanMode() {
		return
	}
	if _, exists := controller.LatestPlanText(); !exists {
		return
	}

	questions := []question.Question{{
		Text:    "The plan is ready. What would you like to do? Use /plan show after staying in Plan mode if you want to review it first.",
		Options: planDecisionOptions,
	}}
	var decision string
	var err error
	if request.waiter == nil {
		var answers []string
		answers, err = u.runQuestionnaireWithFooter(ctx, questions, "Ctrl+C keeps Plan mode active.")
		if len(answers) > 0 {
			decision = answers[0]
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
			answers, err := u.runQuestionnaireWithFooter(localCtx, questions, "Ctrl+C keeps Plan mode active.")
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
				if local.answers[0] == planDecisionOptions[0] {
					decision = "implement"
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
			u.printSystemMessage(dim + "Plan decision cancelled; staying in Plan mode." + reset)
		}
		return
	}
	if decision == "" {
		return
	}
	switch decision {
	case "implement", planDecisionOptions[0]:
		u.handlePlanCommand(ctx, []string{"/plan", "act"})
	case "stay", planDecisionOptions[1]:
		u.printSystemMessage(green + "Plan saved; staying in Plan mode." + reset)
	}
}
