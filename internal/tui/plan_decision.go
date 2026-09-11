package tui

import (
	"context"
	"errors"

	"qcode/internal/question"
)

type planDecisionRequest struct {
	agentID string
	plan    string
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
	u.planDecisionMu.Lock()
	u.planDecisions = append(u.planDecisions, &planDecisionRequest{agentID: id, plan: plan})
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

	answers, err := u.runQuestionnaireWithFooter(ctx, []question.Question{{
		Text:    "The plan is ready. What would you like to do? Use /plan show after staying in Plan mode if you want to review it first.",
		Options: planDecisionOptions,
	}}, "Ctrl+C keeps Plan mode active.")
	if err != nil {
		if errors.Is(err, context.Canceled) && ctx.Err() == nil {
			u.printSystemMessage(dim + "Plan decision cancelled; staying in Plan mode." + reset)
		}
		return
	}
	if len(answers) == 0 {
		return
	}
	switch answers[0] {
	case planDecisionOptions[0]:
		u.handlePlanCommand(ctx, []string{"/plan", "act"})
	case planDecisionOptions[1]:
		u.printSystemMessage(green + "Plan saved; staying in Plan mode." + reset)
	}
}
