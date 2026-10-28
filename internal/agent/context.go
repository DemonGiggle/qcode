package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"qcode/internal/llm"
	"qcode/internal/prompt"
	"qcode/internal/redaction"
	"qcode/internal/transcript"
	"qcode/internal/trust"
)

// Compaction payload budgets. Summarization input is the dominant cost of
// /compact, so tool results (often large reads/shell output) are truncated
// with explicit markers while user/assistant text is preserved verbatim.
const (
	maxCompactToolChars   = 4000
	minCompactToolChars   = 500
	maxCompactInputTokens = 20000
)

// Compact replaces older conversation turns with a model-produced continuation
// summary while retaining the system prompt and a recent, tool-consistent tail.
func (a *Agent) Compact(ctx context.Context) (string, error) {
	stopProgress := a.trackProgress("Compacting conversation")
	defer stopProgress()
	defer a.publishCheckpoint()
	if len(a.messages) <= 1 {
		return "Conversation is already compact.", nil
	}
	start := len(a.messages) - 6
	if start < 1 {
		start = 1
	}
	for start > 1 && a.messages[start].Role == "tool" {
		start--
	}
	recent := append([]llm.Message(nil), a.messages[start:]...)
	// Summarize only the prefix; the retained tail is kept verbatim and does
	// not need to be re-summarized. For tiny histories that fit entirely in
	// the tail, fall back to summarizing everything so the call is not empty.
	prefix := a.messages[1:]
	if start > 1 {
		prefix = a.messages[1:start]
	}
	summarize := sanitizeForCompact(prefix)
	// A prefix ending with assistant tool calls would dangle without its tool
	// results (which live in the retained tail). Drop the dangling call so
	// providers that validate tool-call pairing do not reject the request;
	// the paired result is still preserved verbatim in the tail.
	summarize = stripTrailingToolCalls(summarize)
	if len(summarize) == 0 {
		summarize = stripTrailingToolCalls(sanitizeForCompact(a.messages[1:]))
	}
	if len(summarize) == 0 {
		// Nothing completed to summarize (e.g. history is only a dangling
		// tool call awaiting its result). A system-only request comes back
		// empty from most models, so don't send it.
		return "Conversation is already compact.", nil
	}
	completeSummary := func(msgs []llm.Message) (llm.Response, error) {
		request := llm.Request{Model: a.model, Thinking: "", Messages: append([]llm.Message{{Role: "system", Content: prompt.ConversationCompact}}, msgs...)}
		streamed := false
		response, err := a.provider.Complete(ctx, request, func(event llm.StreamEvent) {
			if ctx.Err() != nil || event.Text == "" || streamed {
				return
			}
			streamed = true
			a.reportProgress("Compacting conversation... receiving summary")
		})
		a.recordUsage(response.Usage)
		return response, err
	}
	response, err := completeSummary(summarize)
	if err != nil {
		return "", err
	}
	summary := compactSummaryText(response.Message)
	if summary == "" {
		// Retry once with only the most recent turns: transient empty
		// responses and oversized inputs often succeed when smaller.
		retry := summarize
		if len(retry) > 10 {
			retry = stripTrailingToolCalls(retry[len(retry)-10:])
		}
		if len(retry) != 0 {
			if response, err = completeSummary(retry); err != nil {
				return "", err
			}
			summary = compactSummaryText(response.Message)
		}
	}
	if summary == "" {
		return "", fmt.Errorf("compaction produced no summary")
	}
	a.messages = append([]llm.Message{{Role: "system", Content: a.system}, {Role: "user", Content: "Conversation summary from earlier turns (reference data):\n" + trust.Wrap("conversation_summary", summary), Origin: "conversation_summary", Untrusted: true}}, recent...)
	a.contextUsage = nil
	a.contextMessages = 0
	a.publishContext()
	return "Conversation compacted.", nil
}

// stripTrailingToolCalls drops trailing assistant tool-call turns that would
// dangle without their tool results.
func stripTrailingToolCalls(messages []llm.Message) []llm.Message {
	for len(messages) > 0 {
		last := messages[len(messages)-1]
		if last.Role != "assistant" || len(last.ToolCalls) == 0 {
			break
		}
		messages = messages[:len(messages)-1]
	}
	return messages
}

// compactSummaryText extracts usable summary text. Some models return
// whitespace-only content or thinking-only output for summarization requests;
// trimming and falling back to Thinking avoids a spurious "no summary" error.
func compactSummaryText(msg llm.Message) string {
	if s := strings.TrimSpace(msg.Content); s != "" {
		return s
	}
	return strings.TrimSpace(msg.Thinking)
}

// sanitizeForCompact truncates large tool results with explicit markers while
// preserving tool-call pairing and provider-required replay fields. Images are
// replaced with a placeholder (base64 payloads dominate cost); Thinking and
// ReasoningDetails are preserved because Responses/Anthropic adapters need
// them to replay assistant turns. User/assistant text is otherwise preserved
// verbatim.
//
// Performance: O(L) in total sanitized bytes. The budget uses one exact
// estimateTokens over the whole slice plus one per truncated tool message to
// track savings, instead of re-marshaling the whole slice after every
// truncation (previously O(n*L)).
func sanitizeForCompact(messages []llm.Message) []llm.Message {
	out := make([]llm.Message, 0, len(messages))
	for _, msg := range messages {
		next := llm.Message{
			Role:             msg.Role,
			Content:          msg.Content,
			Name:             msg.Name,
			ToolCallID:       msg.ToolCallID,
			Thinking:         msg.Thinking,
			ReasoningDetails: append([]byte(nil), msg.ReasoningDetails...),
		}
		if len(msg.ToolCalls) > 0 {
			next.ToolCalls = make([]llm.ToolCall, 0, len(msg.ToolCalls))
			for _, call := range msg.ToolCalls {
				next.ToolCalls = append(next.ToolCalls, llm.ToolCall{
					ID:        call.ID,
					Name:      call.Name,
					Arguments: append([]byte(nil), call.Arguments...),
				})
			}
		}
		if len(msg.Images) > 0 {
			if next.Content != "" {
				next.Content += "\n\n"
			}
			next.Content += fmt.Sprintf("[%d image(s) omitted for compaction]", len(msg.Images))
		}
		if next.Role == "tool" && len(next.Content) > maxCompactToolChars {
			truncated := len(next.Content) - maxCompactToolChars
			next.Content = next.Content[:maxCompactToolChars] + fmt.Sprintf("\n\n[truncated %d chars for compaction]", truncated)
		}
		out = append(out, next)
	}
	total := estimateTokens(out)
	if total <= maxCompactInputTokens {
		return out
	}
	// Over budget: tighten tool results from 4000 down to 500 chars, tracking
	// savings with per-message estimates so the whole slice is marshaled once.
	for i := range out {
		if out[i].Role != "tool" || len(out[i].Content) <= minCompactToolChars {
			continue
		}
		before := estimateTokens(out[i])
		truncated := len(out[i].Content) - minCompactToolChars
		out[i].Content = out[i].Content[:minCompactToolChars] + fmt.Sprintf("\n\n[truncated %d chars for compaction to fit budget]", truncated)
		total += estimateTokens(out[i]) - before
		if total <= maxCompactInputTokens {
			break
		}
	}
	// If still over budget the bloat is user/assistant text, which is preserved
	// verbatim by design; return best-effort rather than dropping turns.
	return out
}

func (a *Agent) shouldAutoCompact() bool {
	if !a.autoCompact || len(a.messages) <= 1 {
		return false
	}
	remaining, known, _ := a.ContextRemaining()
	return known && 100-remaining >= a.autoCompactThreshold
}

// ContextSnapshot returns a copy of the current conversation messages for
// inspection (e.g. the /compact show debug view). The copy is shallow for
// text but duplicates slice headers so callers cannot mutate the live
// history.
func (a *Agent) ContextSnapshot() []llm.Message {
	out := make([]llm.Message, len(a.messages))
	for i, msg := range a.messages {
		next := msg
		if len(msg.ToolCalls) > 0 {
			next.ToolCalls = append([]llm.ToolCall(nil), msg.ToolCalls...)
		}
		if len(msg.Images) > 0 {
			next.Images = append([]llm.Image(nil), msg.Images...)
		}
		if len(msg.ReasoningDetails) > 0 {
			next.ReasoningDetails = append([]byte(nil), msg.ReasoningDetails...)
		}
		out[i] = next
	}
	return out
}

func (a *Agent) autoCompactIfNeeded(ctx context.Context) {
	if !a.shouldAutoCompact() {
		return
	}
	fmt.Fprintln(transcript.NewGroup(a.out), "Compacting conversation to make room for the next request...")
	if _, err := a.Compact(ctx); err != nil {
		fmt.Fprintln(transcript.NewGroup(a.out), "Conversation compaction failed:", a.redaction.Error(redaction.Terminal, err))
	} else {
		fmt.Fprintln(transcript.NewGroup(a.out), "Conversation compacted.")
	}
}

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
	used := estimateTokens(a.messages) + estimateTokens(a.enabledSchemas())
	if a.taskContext != "" {
		used += estimateTokens(a.taskContext)
	}
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
	a.publishCheckpoint()
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
