package agent

import "qcode/internal/llm"

// SessionUsage is safe to read while this agent is running in another tab.
func (a *Agent) SessionUsage() llm.SessionUsage {
	a.stateMu.RLock()
	defer a.stateMu.RUnlock()
	return a.sessionUsage
}

func (a *Agent) recordUsage(usage *llm.Usage) {
	defer a.publishCheckpoint()
	a.stateMu.Lock()
	a.sessionUsage.Add(usage)
	total := a.sessionUsage
	a.stateMu.Unlock()
	a.trace.Usage(a.provider.Name(), a.model, llm.NormalizeUsage(usage), total)
}
