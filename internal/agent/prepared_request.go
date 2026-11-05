package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"qcode/internal/learning"
	"qcode/internal/llm"
	"qcode/internal/prompt"
	"qcode/internal/redaction"
	"qcode/internal/transcript"
	"qcode/internal/trust"
)

// Prepared-request estimation for epic #70 issue #79.
//
// The normal model request is built once per turn on the agent's owning
// goroutine by prepareNormalRequest. It resolves the two ephemeral sources
// (learning search and requestContext) exactly once, applies every
// request-only addition to a byte-for-byte copy of the durable history in the
// long-standing order, captures the enabled tool schemas once, and estimates
// the exact Messages+Tools that Complete will receive with the existing
// approximate estimator (estimateTokens).
//
// Approximation limitations (documented for the meter and auto-compaction):
//   - Images are estimated from their serialized envelope, not from
//     provider-specific image token costs.
//   - Opaque ReasoningDetails bytes are estimated by serialized length; the
//     provider may bill reasoning that is never replayed.
//   - All context-window remaining values are therefore approximate. Cache
//     breakdowns (reads/writes) are subsets of the inclusive input count and
//     are never subtracted from logical input occupancy.
//
// Calibration: the previous completion's reported InputTokens is reused as an
// exact baseline only when complete (KnownInput) and when the request prefix
// fingerprint (model/backend/thinking, tool selection, mode, cwd,
// learning/dynamic/work context, question-budget notice, recovery notice) is
// unchanged. Newly appended durable material (including assistant replay) is
// estimated locally; reported output/reasoning counts are never treated as
// replay sizes. Any prefix change, compaction, restore, or missing input
// falls back to the full local estimate.

// preparedRequest is the exact request Complete receives plus estimate
// metadata. Messages/Tools are immutable snapshots owned by the caller.
type preparedRequest struct {
	Request  llm.Request
	Estimate int

	// Fingerprints for calibration and prefix-stability checks.
	ToolsHash  string
	PrefixHash string
	Backend    string
	Model      string
	Thinking   string
	Mode       string

	// Request-only instruction content (for invalidation comparisons).
	Cwd      string
	Learning string
	Dynamic  string
	Task     string
	Notice   string
	Recovery string

	// DurableCount is len(a.messages) at preparation time.
	DurableCount int
}

// preparedSnapshot is the cached immutable estimate publishContext reads.
// It never triggers learning search or requestContext.
type preparedSnapshot struct {
	valid        bool
	estimate     int
	prefixHash   string
	toolsHash    string
	backend      string
	model        string
	thinking     string
	mode         string
	cwd          string
	learning     string
	dynamic      string
	task         string
	notice       string
	recovery     string
	durableCount int
}

// requestCalibration reuses the last reported complete InputTokens.
type requestCalibration struct {
	valid        bool
	inputTokens  int
	prefixHash   string
	toolsHash    string
	backend      string
	model        string
	thinking     string
	mode         string
	cwd          string
	learning     string
	dynamic      string
	task         string
	notice       string
	recovery     string
	durableCount int // durable history length of the calibrated request
}

func executionModeName(a *Agent) string {
	switch {
	case a.PlanMode():
		return "plan"
	case a.SkillPlanMode():
		return "skillplan"
	default:
		return "act"
	}
}

func hashToolsForEstimate(tools []llm.Tool) string {
	data, _ := json.Marshal(tools)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// hashRequestPrefix covers every request-only instruction byte plus the
// identity that decides them. Durable history is covered separately by count
// and byte comparisons; the prefix hash must change on any dynamic-only or
// notice change even when history length is unchanged.
func hashRequestPrefix(systemContent, toolsHash, backend, model, thinking, mode, cwd, learningText, dynamic, task, notice, recovery string) string {
	h := sha256.New()
	for _, s := range []string{systemContent, toolsHash, backend, model, thinking, mode, cwd, learningText, dynamic, task, notice, recovery} {
		fmt.Fprintf(h, "%d\x00%s\x00", len(s), s)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// questionBudgetNotice returns the ephemeral interactive notice for the
// current turn, or "" when inapplicable. It is part of the prepared prefix.
func (a *Agent) questionBudgetNotice() string {
	if a.InteractiveMode() && a.interactiveAvailable.Load() && !a.PlanMode() && !a.SkillPlanMode() {
		return fmt.Sprintf("\n\nInteractive questions remaining for this request: %d.", max(0, 3-a.questionsAsked))
	}
	return ""
}

// resolveLearningOnce performs exactly one learning search for this
// preparation and records resolve counts for tests. It updates
// a.learningContext (invalidating calibration when changed) without mutating
// durable history.
func (a *Agent) resolveLearningOnce(ctx context.Context) string {
	a.learningResolves++
	extra := ""
	if a.learningStore != nil && a.learningBudget > 0 {
		query := ""
		for i := len(a.messages) - 1; i >= 0; i-- {
			if a.messages[i].Role == "user" {
				query = a.messages[i].Content
				break
			}
		}
		budget := a.learningBudget - learning.EstimatedTokens(prompt.LearningReference)
		if budget > 0 && query != "" {
			items, err := a.learningStore.Search(ctx, query, budget)
			if err != nil {
				fmt.Fprintln(transcript.NewGroup(a.out), "Learning warning:", a.redaction.Error(redaction.Terminal, err))
			} else if len(items) > 0 {
				extra = prompt.LearningReference + trust.Wrap("user_approved_learning", learning.Context(items))
			}
		}
	}
	if a.learningContext != extra {
		a.contextUsage = nil
		a.calibration.valid = false
	}
	a.learningContext = extra
	return extra
}

// resolveDynamicOnce invokes the ephemeral requestContext source exactly once
// for this preparation.
func (a *Agent) resolveDynamicOnce() string {
	a.contextResolves++
	a.stateMu.RLock()
	source := a.requestContext
	a.stateMu.RUnlock()
	dynamic := ""
	if source != nil {
		dynamic = source()
	}
	a.dynamicContext = dynamic
	return dynamic
}

// prepareNormalRequest builds the exact normal-turn request on the owning
// goroutine. Durable a.messages are preserved byte-for-byte; only the copy's
// leading system message gains the ephemeral suffix in existing order:
// cwd, restored-history reference, learning, dynamic context, work history,
// question-budget notice, recovery instruction. Tool schemas are captured
// once and never reordered for speculative caching gain.
func (a *Agent) prepareNormalRequest(ctx context.Context, recoveryInstruction string) preparedRequest {
	extra := a.resolveLearningOnce(ctx)
	dynamic := a.resolveDynamicOnce()

	// Byte-for-byte durable copy (duplicate slice headers and replay
	// fields so callers cannot mutate live history).
	messages := make([]llm.Message, len(a.messages))
	for i, msg := range a.messages {
		next := msg
		if len(msg.ToolCalls) > 0 {
			next.ToolCalls = append([]llm.ToolCall(nil), msg.ToolCalls...)
			for j := range msg.ToolCalls {
				if len(msg.ToolCalls[j].Arguments) > 0 {
					next.ToolCalls[j].Arguments = append([]byte(nil), msg.ToolCalls[j].Arguments...)
				}
			}
		}
		if len(msg.Images) > 0 {
			next.Images = append([]llm.Image(nil), msg.Images...)
			for j := range msg.Images {
				if len(msg.Images[j].Data) > 0 {
					next.Images[j].Data = append([]byte(nil), msg.Images[j].Data...)
				}
			}
		}
		if len(msg.ReasoningDetails) > 0 {
			next.ReasoningDetails = append([]byte(nil), msg.ReasoningDetails...)
		}
		messages[i] = next
	}
	var restoredPositions []string
	for i := 1; i < len(messages); i++ {
		if messages[i].Untrusted && strings.HasPrefix(messages[i].Origin, "restored_") {
			restoredPositions = append(restoredPositions, strconv.Itoa(i))
		}
	}
	cwd := a.WorkingDirectory()
	if len(messages) > 0 {
		if cwd != "" {
			messages[0].Content += "\n\nCurrent agent working directory: " + strconv.Quote(cwd)
		}
		if len(restoredPositions) > 0 {
			messages[0].Content += fmt.Sprintf(prompt.RestoredHistoryReference, strings.Join(restoredPositions, ", "))
		}
		messages[0].Content += extra
		if dynamic != "" {
			messages[0].Content += "\n\nAdditional context: " + trust.Wrap("request_context", dynamic)
		}
		if a.taskContext != "" {
			messages[0].Content += "\n\nWork history: " + trust.Wrap("work_history", a.taskContext)
		}
	}
	tools := a.enabledSchemas()
	// Defensive copy so later toggles cannot mutate the prepared snapshot.
	toolsCopy := append([]llm.Tool(nil), tools...)
	notice := a.questionBudgetNotice()
	if notice != "" && len(messages) > 0 {
		messages[0].Content += notice
	}
	recovery := ""
	if recoveryInstruction != "" && len(messages) > 0 && messages[0].Role == "system" {
		messages[0].Content += "\n\n" + recoveryInstruction
		recovery = recoveryInstruction
	}
	estimate := estimateTokens(messages) + estimateTokens(toolsCopy)
	backend := ""
	if a.provider != nil {
		backend = a.provider.Name()
	}
	mode := executionModeName(a)
	toolsHash := hashToolsForEstimate(toolsCopy)
	systemContent := ""
	if len(messages) > 0 {
		systemContent = messages[0].Content
	}
	prefixHash := hashRequestPrefix(systemContent, toolsHash, backend, a.model, a.thinking, mode, cwd, extra, dynamic, a.taskContext, notice, recovery)
	prep := preparedRequest{
		Request:      llm.Request{Model: a.model, Thinking: a.thinking, Messages: messages, Tools: toolsCopy},
		Estimate:     estimate,
		ToolsHash:    toolsHash,
		PrefixHash:   prefixHash,
		Backend:      backend,
		Model:        a.model,
		Thinking:     a.thinking,
		Mode:         mode,
		Cwd:          cwd,
		Learning:     extra,
		Dynamic:      dynamic,
		Task:         a.taskContext,
		Notice:       notice,
		Recovery:     recovery,
		DurableCount: len(a.messages),
	}
	a.cachePreparedSnapshot(prep)
	return prep
}

// cachePreparedSnapshot stores the immutable estimate publishContext reads.
func (a *Agent) cachePreparedSnapshot(prep preparedRequest) {
	a.prepared = preparedSnapshot{
		valid: true, estimate: prep.Estimate,
		prefixHash: prep.PrefixHash, toolsHash: prep.ToolsHash,
		backend: prep.Backend, model: prep.Model, thinking: prep.Thinking, mode: prep.Mode,
		cwd: prep.Cwd, learning: prep.Learning, dynamic: prep.Dynamic,
		task: prep.Task, notice: prep.Notice, recovery: prep.Recovery,
		durableCount: prep.DurableCount,
	}
	a.publishContext()
}

// invalidatePreparedCache drops the cached estimate and calibration after
// history-changing operations (compaction, restore, reset, backend/model
// switches, tool/mode/cwd changes). Callers re-publish the fallback meter.
func (a *Agent) invalidatePreparedCache() {
	a.prepared.valid = false
	a.calibration.valid = false
}

// updateCalibration records the reported complete InputTokens for the request
// just sent. Partial input (Anthropic breakdowns without complete input)
// yields no baseline and falls back to local estimates. Cache breakdowns are
// never subtracted: a cached 800 of 1000 input still occupies 1000 tokens.
func (a *Agent) updateCalibration(prep preparedRequest, usage *llm.Usage) {
	input, ok := usage.KnownInput()
	if !ok {
		a.calibration.valid = false
		return
	}
	a.calibration = requestCalibration{
		valid: true, inputTokens: input,
		prefixHash: prep.PrefixHash, toolsHash: prep.ToolsHash,
		backend: prep.Backend, model: prep.Model, thinking: prep.Thinking, mode: prep.Mode,
		cwd: prep.Cwd, learning: prep.Learning, dynamic: prep.Dynamic,
		task: prep.Task, notice: prep.Notice, recovery: prep.Recovery,
		durableCount: prep.DurableCount,
	}
}

// calibrationMatches reports whether the cached prepared prefix is identical
// to the calibrated request prefix.
func (a *Agent) calibrationMatches() bool {
	if !a.calibration.valid || !a.prepared.valid {
		return false
	}
	c, p := a.calibration, a.prepared
	return c.prefixHash == p.prefixHash && c.toolsHash == p.toolsHash &&
		c.backend == p.backend && c.model == p.model && c.thinking == p.thinking && c.mode == p.mode &&
		c.cwd == p.cwd && c.learning == p.learning && c.dynamic == p.dynamic &&
		c.task == p.task && c.notice == p.notice && c.recovery == p.recovery
}

// durableAppendedEstimate estimates newly appended durable material since the
// calibration request, including assistant replay. Reported output/reasoning
// counts are never used as replay sizes.
func (a *Agent) durableAppendedEstimate() int {
	if !a.calibration.valid {
		return 0
	}
	if len(a.messages) <= a.calibration.durableCount {
		return 0
	}
	// Verify the stable prefix is byte-identical; any rewrite invalidates.
	// The prepared snapshot does not retain full messages, so compare counts
	// only here and rely on prefix-hash invalidation for content changes to
	// the leading system message. Durable appends after the calibrated count
	// are estimated locally.
	return estimateTokens(a.messages[a.calibration.durableCount:])
}

// currentUsedTokens resolves the meter numerator: calibrated input plus local
// delta when the prefix is stable, otherwise the full prepared estimate,
// otherwise the durable fallback. Every path is approximate.
func (a *Agent) currentUsedTokens() int {
	if a.calibrationMatches() {
		return a.calibration.inputTokens + a.durableAppendedEstimate()
	}
	if a.prepared.valid {
		// Prepared estimate already includes the request-only additions
		// exactly once plus durable history; add any durable appends since
		// preparation (e.g. tool results added after the snapshot).
		extra := 0
		if len(a.messages) > a.prepared.durableCount {
			extra = estimateTokens(a.messages[a.prepared.durableCount:])
		}
		return a.prepared.estimate + extra
	}
	used := estimateTokens(a.messages) + estimateTokens(a.enabledSchemas())
	if a.taskContext != "" {
		used += estimateTokens(a.taskContext)
	}
	if a.learningContext != "" {
		used += (len(a.learningContext) + 3) / 4
	}
	return used
}

// shouldAutoCompactForEstimate validates the prepared estimate against the
// known window before sending.
func (a *Agent) shouldAutoCompactForEstimate(estimate int) bool {
	if !a.autoCompact || len(a.messages) <= 1 {
		return false
	}
	limit := a.contextWindow
	if a.contextOverride > 0 {
		limit = a.contextOverride
	}
	if limit <= 0 {
		return false
	}
	used := estimate
	if a.calibrationMatches() {
		// Calibrated occupancy reflects the provider's inclusive input
		// count plus local estimates of appended material. The prepared
		// estimate double-counts durable history already in the baseline
		// when the prefix is stable, so prefer the calibrated value; any
		// request-only growth that changes the prefix invalidates the
		// baseline and falls back to the prepared estimate below.
		used = a.calibration.inputTokens + a.durableAppendedEstimate()
		_ = estimate
	}
	remaining := limit - min(limit, max(0, used))
	percent := int(float64(remaining) * 100 / float64(limit))
	return 100-percent >= a.autoCompactThreshold
}

// maybeAutoCompactForPrepared compacts once when the prepared request crosses
// the threshold. It returns true when history changed; the caller must rebuild
// preparation once (preserving the pending one-shot recovery notice) and must
// not loop.
func (a *Agent) maybeAutoCompactForPrepared(ctx context.Context, estimate int) bool {
	if !a.shouldAutoCompactForEstimate(estimate) {
		return false
	}
	before := len(a.messages)
	fmt.Fprintln(transcript.NewGroup(a.out), "Compacting conversation to make room for the next request...")
	if _, err := a.Compact(ctx); err != nil {
		fmt.Fprintln(transcript.NewGroup(a.out), "Conversation compaction failed:", a.redaction.Error(redaction.Terminal, err))
		return false
	}
	fmt.Fprintln(transcript.NewGroup(a.out), "Conversation compacted.")
	return len(a.messages) != before
}

// stablePrefixEqual reports byte-identical durable history for golden tests:
// identical deterministic inputs produce identical normalized messages/tools
// and the base system content stays leading before ephemeral additions.
func stablePrefixEqual(a, b []llm.Message) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Role != b[i].Role || a[i].Content != b[i].Content ||
			a[i].Name != b[i].Name || a[i].ToolCallID != b[i].ToolCallID ||
			a[i].Origin != b[i].Origin || a[i].Untrusted != b[i].Untrusted ||
			a[i].Thinking != b[i].Thinking || string(a[i].ReasoningDetails) != string(b[i].ReasoningDetails) {
			return false
		}
		if len(a[i].ToolCalls) != len(b[i].ToolCalls) || len(a[i].Images) != len(b[i].Images) {
			return false
		}
		for j := range a[i].ToolCalls {
			if a[i].ToolCalls[j].ID != b[i].ToolCalls[j].ID || a[i].ToolCalls[j].Name != b[i].ToolCalls[j].Name ||
				string(a[i].ToolCalls[j].Arguments) != string(b[i].ToolCalls[j].Arguments) {
				return false
			}
		}
	}
	return true
}

func sortedToolNames(tools []llm.Tool) []string {
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.Name)
	}
	sort.Strings(names)
	return names
}
