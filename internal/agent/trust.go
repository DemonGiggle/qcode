package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
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
	question := fmt.Sprintf("Warning: earlier untrusted content matched a possible prompt-injection pattern. This approval is required because of that earlier match, not an assessment that this tool call is malicious. Approve this %s call?\n%s\nDenying remembers this exact action for this session. Approving for this run also permits subsequent tool calls without injection approval, including calls that change or send data.", call.Name, args)
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

// suspiciousKey identifies one distinct suspicious payload so re-scanning the
// same history does not re-warn. Trust envelopes are unwrapped first so a
// tool result warned raw matches the wrapped copy stored in history.
// Content hashes keep memory bounded even for large tool outputs.
func suspiciousKey(content string) string {
	inner := content
	for {
		var envelope map[string]json.RawMessage
		if json.Unmarshal([]byte(inner), &envelope) != nil || len(envelope) != 4 {
			break
		}
		raw, ok := envelope["content"]
		if !ok {
			break
		}
		var text string
		if json.Unmarshal(raw, &text) != nil {
			break
		}
		inner = text
	}
	sum := sha256.Sum256([]byte(inner))
	return fmt.Sprintf("%x", sum[:16])
}

// noteSuspicious records contentHash->source once per agent lifetime and
// reports whether this is the first time it was seen. Callers only warn and
// gate approvals on first sightings so persistent history does not re-prompt
// every follow-up action.
func (a *Agent) noteSuspicious(source, content string) bool {
	key := suspiciousKey(content)
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	if a.warnedSuspicious == nil {
		a.warnedSuspicious = make(map[string]string)
	}
	if _, ok := a.warnedSuspicious[key]; ok {
		return false
	}
	a.warnedSuspicious[key] = source
	return true
}

// denyFingerprint persists a denied exact action for the agent lifetime so a
// later run with the same history does not ask again.
func (a *Agent) denyFingerprint(fingerprint string) {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	if a.deniedFingerprints == nil {
		a.deniedFingerprints = make(map[string]bool)
	}
	a.deniedFingerprints[fingerprint] = true
}

func (a *Agent) deniedFingerprint(fingerprint string) bool {
	a.stateMu.RLock()
	defer a.stateMu.RUnlock()
	return a.deniedFingerprints[fingerprint]
}
