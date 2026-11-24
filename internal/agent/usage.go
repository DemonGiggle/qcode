package agent

import (
	"context"
	"time"

	"qcode/internal/llm"
)

type runUsageKey struct{}

type runUsage struct {
	usage         llm.SessionUsage
	modelDuration time.Duration
}

// RunWithUsage is Run with the prompt's output token count and cumulative
// model call duration. Output tokens are nil if the run fails, makes no model
// calls, or any provider call omits its output count.
func (a *Agent) RunWithUsage(ctx context.Context, userText string) (*int, time.Duration, error) {
	return a.runWithUsage(ctx, userText, nil)
}

func (a *Agent) runWithUsage(ctx context.Context, userText string, input *taskInput) (*int, time.Duration, error) {
	var run runUsage
	err := a.runWithInput(context.WithValue(ctx, runUsageKey{}, &run), userText, input)
	if err != nil || run.usage.Requests == 0 || run.usage.OutputReported != run.usage.Requests || run.modelDuration <= 0 {
		return nil, 0, err
	}
	return llm.IntPtr(run.usage.OutputTokens), run.modelDuration, nil
}

// completeWithRunUsage collects only calls made with this prompt's context,
// including automatic compaction. Session-wide accounting is unchanged.
func (a *Agent) completeWithRunUsage(ctx context.Context, request llm.Request, stream llm.StreamCallback) (llm.Response, error) {
	started := time.Now()
	response, err := a.provider.Complete(ctx, request, stream)
	elapsed := time.Since(started)
	if run, ok := ctx.Value(runUsageKey{}).(*runUsage); ok {
		run.usage.AddResult(response.Usage, err)
		run.modelDuration += elapsed
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
