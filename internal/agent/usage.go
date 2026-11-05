package agent

import "qcode/internal/llm"

// SessionUsage is safe to read while this agent is running in another tab.
func (a *Agent) SessionUsage() llm.SessionUsage {
	a.stateMu.RLock()
	defer a.stateMu.RUnlock()
	return a.sessionUsage
}

// recordUsage aggregates exactly one provider completion into the session
// total. Callers invoke it once per Complete result (normal turns, each
// compaction attempt, learning calls); stream snapshots never aggregate
// separately. A failed call with no usable usage counts as missing, while a
// failed call that still carried valid usage keeps that usage. Only one
// provider-level completion is promised: internal provider retries (for
// example auth refreshes) are not visible here and carry no billing claim.
// Per-request Usage pointers are treated as immutable once published and are
// never mutated; the session snapshot copied for trace/UI readers is a plain
// value struct with no shared maps or pointers.
func (a *Agent) recordUsage(usage *llm.Usage) {
	defer a.publishCheckpoint()
	a.stateMu.Lock()
	a.sessionUsage.Add(usage)
	total := a.sessionUsage
	a.stateMu.Unlock()
	a.trace.Usage(a.provider.Name(), a.model, llm.NormalizeUsage(usage), total)
}
