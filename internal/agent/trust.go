package agent

import (
	"context"
	"fmt"
	"strings"

	"qcode/internal/llm"
)

// File and search tools can expose credentials even though they do not mutate
// state. After an injection warning, only direct user interaction and plan
// submission are available without a fresh approval.
func requiresInjectionApproval(name string) bool {
	switch name {
	case "ask_questions", "propose_plan", "propose_skill":
		return false
	default:
		return true
	}
}

func (a *Agent) approveInjectionAction(ctx context.Context, call llm.ToolCall) (bool, error) {
	a.stateMu.RLock()
	questioner := a.questioner
	a.stateMu.RUnlock()
	if questioner == nil {
		return false, fmt.Errorf("action blocked after a prompt-injection warning: interactive approval is unavailable")
	}
	args := compactJSON(call.Arguments)
	if len(args) > 2048 {
		return false, fmt.Errorf("action blocked after a prompt-injection warning: arguments are too long for review")
	}
	question := fmt.Sprintf("Warning: possible prompt injection was found in untrusted content. Approve this %s call?\n%s\nApproving for this run also permits subsequent tool calls without injection approval, including calls that change or send data.", call.Name, args)
	answers, err := questioner(ctx, []Question{{Text: question, Options: []string{"Deny", "Approve once", "Approve for this run"}, AllowCustom: false}})
	if err != nil {
		return false, fmt.Errorf("prompt-injection approval failed: %w", err)
	}
	if len(answers) != 1 {
		return false, fmt.Errorf("action denied after a prompt-injection warning")
	}
	switch strings.TrimSpace(answers[0]) {
	case "Approve once":
		return false, nil
	case "Approve for this run":
		return true, nil
	default:
		return false, fmt.Errorf("action denied after a prompt-injection warning")
	}
}
