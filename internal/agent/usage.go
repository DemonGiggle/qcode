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
// separately. Failures without usage are recorded separately. A
// failed call that still carried valid usage keeps that usage. Only one
// provider-level completion is promised: internal provider retries (for
// example auth refreshes) are not visible here and carry no billing claim.
// Per-request Usage pointers are treated as immutable once published and are
// never mutated; the session snapshot copied for trace/UI readers is a plain
// value struct with no shared maps or pointers.
func (a *Agent) recordUsage(usage *llm.Usage, err error) {
	defer a.publishCheckpoint()
	a.stateMu.Lock()
	a.sessionUsage.AddResult(usage, err)
	total := a.sessionUsage
	a.stateMu.Unlock()
	a.trace.Usage(a.provider.Name(), a.model, llm.NormalizeUsage(usage), total)
}

// recordResponsesResult records usage plus opt-in Responses observability
// (issue #116): the completed response id for future previous_response_id /
// comparison flows, and prompt_cache_diagnostics for the debug trace.
// Call only for completed responses; truncated/failed partials must not
// seed the baseline. Diagnostics never affect billing; usage remains the
// source of truth.
func (a *Agent) recordResponsesResult(response llm.Response) {
	a.recordUsage(response.Usage, nil)
	provider := ""
	model := ""
	if a.provider != nil {
		provider = a.provider.Name()
	}
	model = a.model
	a.stateMu.Lock()
	a.lastResponseID = response.ResponseID
	a.lastDiagnostics = response.CacheDiagnostics
	a.stateMu.Unlock()
	a.trace.CacheDiagnostics(provider, model, response.ResponseID, response.CacheDiagnostics)
}

// LastResponseID returns the most recent completed Responses id, or "" when
// the last completed response did not report one. Truncated/failed partials
// never update it.
func (a *Agent) LastResponseID() string {
	a.stateMu.RLock()
	defer a.stateMu.RUnlock()
	return a.lastResponseID
}

// LastDiagnostics returns the most recent completed prompt_cache_diagnostics,
// or nil when the last completed response did not report any.
func (a *Agent) LastDiagnostics() *llm.ResponsesCacheDiagnostics {
	a.stateMu.RLock()
	defer a.stateMu.RUnlock()
	return a.lastDiagnostics
}
