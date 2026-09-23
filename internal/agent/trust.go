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

func (a *Agent) approveInjectionAction(ctx context.Context, call llm.ToolCall) error {
	a.stateMu.RLock()
	questioner := a.questioner
	a.stateMu.RUnlock()
	if questioner == nil {
		return fmt.Errorf("action blocked after a prompt-injection warning: interactive approval is unavailable")
	}
	args := compactJSON(call.Arguments)
	if len(args) > 2048 {
		return fmt.Errorf("action blocked after a prompt-injection warning: arguments are too long for review")
	}
	question := fmt.Sprintf("🚨 Possible prompt injection was found in untrusted content. Approve this exact %s call?\n%s", call.Name, args)
	answers, err := questioner(ctx, []Question{{Text: question, Options: []string{"Deny", "Approve once"}, AllowCustom: false}})
	if err != nil {
		return fmt.Errorf("prompt-injection approval failed: %w", err)
	}
	if len(answers) != 1 || strings.TrimSpace(answers[0]) != "Approve once" {
		return fmt.Errorf("action denied after a prompt-injection warning")
	}
	return nil
}
