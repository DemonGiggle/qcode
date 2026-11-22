package agent

import (
	"context"

	"qcode/internal/llm"
)

type runUsageKey struct{}

// RunWithUsage is Run with the prompt's total input and output tokens. The
// total is nil if the run fails or any provider call omits either count.
func (a *Agent) RunWithUsage(ctx context.Context, userText string) (*int, error) {
	return a.runWithUsage(ctx, userText, nil)
}

func (a *Agent) runWithUsage(ctx context.Context, userText string, input *taskInput) (*int, error) {
	var usage llm.SessionUsage
	err := a.runWithInput(context.WithValue(ctx, runUsageKey{}, &usage), userText, input)
	if err != nil || usage.Requests == 0 || usage.InputReported != usage.Requests || usage.OutputReported != usage.Requests {
		return nil, err
	}
	return llm.IntPtr(usage.TotalTokens), nil
}

// completeWithRunUsage collects only calls made with this prompt's context,
// including automatic compaction. Session-wide accounting is unchanged.
func (a *Agent) completeWithRunUsage(ctx context.Context, request llm.Request, stream llm.StreamCallback) (llm.Response, error) {
	response, err := a.provider.Complete(ctx, request, stream)
	if usage, ok := ctx.Value(runUsageKey{}).(*llm.SessionUsage); ok {
		usage.AddResult(response.Usage, err)
	}
	return response, err
}

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
	a.lastComparisonStatus = response.ComparisonStatus
	a.stateMu.Unlock()
	a.trace.CacheDiagnostics(provider, model, response.ResponseID, response.ComparisonStatus, response.CacheDiagnostics)
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
	_, diag := a.LastCacheComparison()
	return diag
}

// LastCacheComparison returns the request path and server report from the
// same completed turn, including when the server omitted its report.
func (a *Agent) LastCacheComparison() (llm.CacheComparisonStatus, *llm.ResponsesCacheDiagnostics) {
	a.stateMu.RLock()
	defer a.stateMu.RUnlock()
	return a.lastComparisonStatus, a.lastDiagnostics
}
