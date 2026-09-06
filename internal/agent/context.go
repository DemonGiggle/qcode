package agent

import (
	"context"
	"encoding/json"
	"time"

	"qcode/internal/llm"
)

type contextStatus struct {
	remaining        int
	known, estimated bool
}

// ContextRemaining is safe to read while the terminal is paging during a run.
func (a *Agent) ContextRemaining() (int, bool, bool) {
	if s := a.contextStatus.Load(); s != nil {
		return s.remaining, s.known, s.estimated
	}
	return 0, false, false
}

// SetContextWindow overrides capacity for the selected model, for display only.
func (a *Agent) SetContextWindow(tokens int) {
	a.contextOverride = max(0, tokens)
	a.publishContext()
}

// RefreshContext performs bounded, optional discovery. Ollama can only report
// allocation after a model is loaded, so retry after completions as well.
func (a *Agent) RefreshContext(ctx context.Context) {
	if a.contextOverride == 0 {
		if p, ok := a.provider.(llm.ContextWindowProvider); ok {
			lookup, cancel := context.WithTimeout(ctx, 2*time.Second)
			limit, err := p.ContextWindow(lookup, a.model)
			cancel()
			if err == nil {
				a.contextWindow = max(0, limit)
			}
		}
	}
	a.publishContext()
}

func (a *Agent) publishContext() {
	limit := a.contextWindow
	if a.contextOverride > 0 {
		limit = a.contextOverride
	}
	state := &contextStatus{known: limit > 0, estimated: true}
	used := estimateTokens(a.messages) + estimateTokens(a.tools.EnabledSchemas())
	if a.learningContext != "" {
		used += (len(a.learningContext) + 3) / 4
	}
	if a.contextUsage != nil {
		used = a.contextUsage.InputTokens + a.contextUsage.OutputTokens
		state.estimated = false
		if a.contextMessages < len(a.messages) {
			used += estimateTokens(a.messages[a.contextMessages:])
			state.estimated = true
		}
	}
	if limit > 0 {
		remaining := limit - min(limit, max(0, used))
		state.remaining = int(float64(remaining) * 100 / float64(limit))
	}
	state.remaining = max(0, min(100, state.remaining))
	a.contextStatus.Store(state)
}

// Local estimates include serialized tool definitions and message overhead.
// Images have provider-specific token costs, so these values remain approximate.
func estimateTokens(value any) int {
	data, _ := json.Marshal(value)
	return (len(data) + 3) / 4
}

func (a *Agent) invalidateContextUsage() {
	a.contextUsage = nil
	a.publishContext()
}
