package llm

import (
	"strings"
)

// chatGPTCacheInput builds the Codex-parity input: leading developer
// instructions followed by the plain Responses encoding of history. It
// allocates fresh wire items only and never modifies durable messages,
// replayed assistant items, or other Responses providers. No explicit
// prompt_cache_breakpoint markers are added; the server selects implicit
// writes and lookup boundaries within its documented limits.
func chatGPTCacheInput(instructions string, messages []Message) []any {
	items := responsesInput(messages)
	if strings.TrimSpace(instructions) != "" {
		initial := responsesMessage("developer", instructions, nil)
		items = append([]any{initial}, items...)
	}
	return items
}
